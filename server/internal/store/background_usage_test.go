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
	budget := int64(8192)
	job := BackgroundGenerationJob{ReservationID: id, ResponseID: "resp_" + id, BaseURL: "https://provider.test", TeamID: team.ID, UserID: user.ID,
		Record: UsageRecord{ID: uuid.NewString(), RequestID: id, TeamID: team.ID, UserID: user.ID, Model: "m", UpstreamModel: "m", CreditsMicro: 999_999, ReasoningEffort: "high", ReasoningBudgetTokens: &budget, CreatedAt: at}, NextPollAt: at, CreatedAt: at}
	if err := s.CreateBackgroundGeneration(ctx, job); err != nil {
		t.Fatal(err)
	}
	// 首次请求参数属于任务模板；调用方改指针及重复创建均不能改写它。
	budget = 1
	replayed := job
	replayed.Record.ReasoningEffort = "low"
	if err := s.CreateBackgroundGeneration(ctx, replayed); err != nil {
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
			record.ReasoningEffort = "low"
			record.ReasoningBudgetTokens = nil
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
	if got.Record.ReasoningEffort != "high" || got.Record.ReasoningBudgetTokens == nil || *got.Record.ReasoningBudgetTokens != 8192 {
		t.Fatalf("异步用量观察不能覆盖首次请求的思考参数: %+v", got.Record)
	}
	*got.UsageObservedAt = at
	*got.Record.ReasoningBudgetTokens = 2
	again, _ := s.FindBackgroundGeneration(ctx, team.ID, job.ResponseID)
	if !again.UsageObservedAt.Equal(at.Add(8*time.Millisecond)) || again.Record.ReasoningBudgetTokens == nil || *again.Record.ReasoningBudgetTokens != 8192 {
		t.Fatal("读取观察时间及思考预算必须返回副本")
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
