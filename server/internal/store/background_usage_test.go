package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testBackgroundUsageObservation(t *testing.T, ctx context.Context, s PlatformStore) {
	team, user := postpaidFixture(t, ctx, s, "observed-background")
	id := uuid.NewString()
	if decision, err := s.AdmitUsage(ctx, ReserveQuotaInput{ID: id, TeamID: team.ID, UserID: user.ID, RequestID: id}); err != nil || decision != ReserveReserved {
		t.Fatalf("admit: %s %v", decision, err)
	}
	at := time.Now().UTC().Truncate(time.Millisecond)
	job := BackgroundGenerationJob{ReservationID: id, ResponseID: "resp_" + id, BaseURL: "https://provider.test", TeamID: team.ID, UserID: user.ID,
		Record: UsageRecord{ID: uuid.NewString(), RequestID: id, TeamID: team.ID, UserID: user.ID, Model: "m", UpstreamModel: "m", CreditsMicro: 999_999, CreatedAt: at}, NextPollAt: at, CreatedAt: at}
	if err := s.CreateBackgroundGeneration(ctx, job); err != nil {
		t.Fatal(err)
	}
	// 并发观察乱序到达，最终保留最新整份快照，不累加重放，也不修改余额。
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			record := job.Record
			record.InputTokens = int64(100 - i)
			record.CacheReadTokens = int64(i)
			record.OutputTokens = int64(i)
			record.CreatedAt = at.Add(time.Duration(i+1) * time.Millisecond)
			if _, err := s.UpdateBackgroundGenerationUsage(ctx, id, record); err != nil {
				t.Errorf("observe: %v", err)
			}
		}(i)
	}
	wg.Wait()
	got, err := s.FindBackgroundGeneration(ctx, team.ID, job.ResponseID)
	if err != nil || got == nil || got.UsageObservedAt == nil || !got.UsageObservedAt.Equal(at.Add(8*time.Millisecond)) || got.Record.InputTokens != 93 || got.Record.CacheReadTokens != 7 || got.Record.OutputTokens != 7 || got.Record.CreditsMicro != 0 || got.CompletedAt != nil {
		t.Fatalf("观察恢复必须整份且有序，不保留旧估算费用: %+v %v", got, err)
	}
	*got.UsageObservedAt = at
	again, _ := s.FindBackgroundGeneration(ctx, team.ID, job.ResponseID)
	if !again.UsageObservedAt.Equal(at.Add(8 * time.Millisecond)) {
		t.Fatal("读取观察时间必须返回副本")
	}
	before, _ := s.GetTeam(ctx, team.ID)
	if before.UsedCredits != "0.000000" || before.ReservedCredits != "0.000000" {
		t.Fatalf("观察不能占款或扣费: %+v", before)
	}
	invalid := job.Record
	invalid.RequestID = "wrong-owner"
	if _, err := s.UpdateBackgroundGenerationUsage(ctx, id, invalid); !errors.Is(err, ErrReservationMismatch) {
		t.Fatalf("观察不得修改任务归属: %v", err)
	}
	invalid.RequestID = id
	invalid.InputTokens = -1
	if _, err := s.UpdateBackgroundGenerationUsage(ctx, id, invalid); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("非法观察不得入库: %v", err)
	}
	if err := s.FinishBackgroundGeneration(ctx, id, []byte(`{"status":"completed"}`), 200, "application/json", at); err != nil {
		t.Fatal(err)
	}
	late := job.Record
	late.InputTokens = 500
	late.CreatedAt = at.Add(time.Hour)
	finished, err := s.UpdateBackgroundGenerationUsage(ctx, id, late)
	if err != nil || finished.Record.InputTokens != 93 || finished.CompletedAt == nil {
		t.Fatalf("终态不接受迟到观察: %+v %v", finished, err)
	}
}
