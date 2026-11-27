package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testBackgroundEndpointReview(t *testing.T, ctx context.Context, s PlatformStore) {
	team, user := postpaidFixture(t, ctx, s, "endpoint-review")
	id := uuid.NewString()
	if decision, err := s.AdmitUsage(ctx, ReserveQuotaInput{ID: id, TeamID: team.ID, UserID: user.ID, RequestID: id}); err != nil || decision != ReserveReserved {
		t.Fatalf("admit: %s %v", decision, err)
	}
	at := time.Now().UTC().Truncate(time.Millisecond)
	// provider_id 在历史任务里是无外键快照，目标目录可能已删除相应行。
	missingProviderID := uuid.NewString()
	job := BackgroundGenerationJob{ReservationID: id, ResponseID: "resp_" + id, ProviderID: missingProviderID,
		BaseURL: "https://previous.example", TeamID: team.ID, UserID: user.ID, NextPollAt: at, CreatedAt: at,
		Record: UsageRecord{ID: uuid.NewString(), RequestID: id, TeamID: team.ID, UserID: user.ID, ProviderID: missingProviderID,
			Model: "model", UpstreamModel: "model", CreatedAt: at}}
	if err := s.CreateBackgroundGeneration(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := s.HoldBackgroundGenerationForReview(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryBackgroundGeneration(ctx, id, at, ""); err != nil {
		t.Fatal(err)
	}
	held, err := s.FindBackgroundGeneration(ctx, team.ID, job.ResponseID)
	if err != nil || held == nil || held.LastError != BackgroundEndpointUnverified || held.CompletedAt != nil {
		t.Fatalf("搁置状态被迟到重试覆盖: %+v %v", held, err)
	}
	rows, err := s.ListPendingSettlements(ctx, PendingSettlementFilter{Status: PendingSettlementFailed})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.ReservationID == id {
			found = row.Record.BackgroundResponseID == job.ResponseID && row.Record.ProviderID == "" && row.Record.CreditsMicro == 0
		}
	}
	if !found {
		t.Fatal("人工核对项缺少已知上游任务身份")
	}
	due, err := s.ClaimBackgroundGenerations(ctx, at.Add(time.Second), at.Add(time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range due {
		if item.ReservationID == id {
			t.Fatal("等待人工核对时不应再次领取")
		}
	}

	invalid := job.Record
	invalid.CreditsMicro = -1
	if err := s.FinishBackgroundGeneration(ctx, id, invalid, []byte("invalid"), 200, "application/json", at); !errors.Is(err, ErrInvalidCreditCharge) {
		t.Fatalf("账本失败应整笔回滚: %v", err)
	}
	stillHeld, err := s.FindBackgroundGeneration(ctx, team.ID, job.ResponseID)
	if err != nil || stillHeld == nil || stillHeld.CompletedAt != nil || !stillHeld.AwaitingEndpointReview() {
		t.Fatalf("结算回滚后应继续保留核对项: %+v %v", stillHeld, err)
	}
	// 两个改址前发出的查询相继给出终态。余额、流水与留存结果必须来自同一个获胜事务。
	done := make(chan error, 2)
	for charge := int64(1); charge <= 2; charge++ {
		go func() {
			record := job.Record
			record.Status, record.CreditsMicro = UsageStatusOk, charge
			body := []byte(fmt.Sprintf(`{"charge":%d}`, charge))
			done <- s.FinishBackgroundGeneration(ctx, id, record, body, 200, "application/json", at)
		}()
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	closed, err := s.FindBackgroundGeneration(ctx, team.ID, job.ResponseID)
	if err != nil || closed == nil || closed.CompletedAt == nil {
		t.Fatalf("权威终态未留存: %+v %v", closed, err)
	}
	usage, err := s.ListUsage(ctx, UsageFilter{TeamID: team.ID, RequestID: id})
	if err != nil || len(usage) != 1 || usage[0].CreditsMicro < 1 || usage[0].CreditsMicro > 2 ||
		usage[0].ProviderID != "" || fmt.Sprintf(`{"charge":%d}`, usage[0].CreditsMicro) != string(closed.FinalResponse) {
		t.Fatalf("流水与首次留存结果不一致: %+v %s %v", usage, closed.FinalResponse, err)
	}
	account, err := s.GetTeam(ctx, team.ID)
	if err != nil || account == nil || account.UsedCredits != creditMicroString(usage[0].CreditsMicro) {
		t.Fatalf("已记流水与余额不一致: %+v %v", account, err)
	}
	resolved, err := s.ListPendingSettlements(ctx, PendingSettlementFilter{Status: PendingSettlementApplied})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range resolved {
		if item.ReservationID == id {
			return
		}
	}
	t.Fatal("首次合法终态未关闭原核对项")
}

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
	actual := again.Record
	actual.Status = UsageStatusOk
	if err := s.FinishBackgroundGeneration(ctx, id, actual, []byte(`{"status":"completed"}`), 200, "application/json", at); err != nil {
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
