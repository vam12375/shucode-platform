package compensation

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

// memoryFixture 构造内存库 + 团队/请求身份/usage，返回可直接断言的对象。
type memoryFixture struct {
	store  *memoryStoreWrapper
	userID string
	teamID string
	record store.UsageRecord
	spool  string
}

type memoryStoreWrapper struct {
	store.PlatformStore
}

func (s *memoryStoreWrapper) EnqueuePendingSettlement(_ context.Context, _ string, _ store.UsageRecord) error {
	return errors.New("database unavailable")
}

func newMemoryFixture(t *testing.T) *memoryFixture {
	t.Helper()
	ctx := context.Background()
	mem, err := store.NewMemoryStore(ctx, store.MemoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mem.Dispose(ctx) })
	user, err := mem.CreateUser(ctx, store.UserInput{
		Email: "settle@test.local", PasswordHash: "x", DisplayName: "结算", Role: store.RoleUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	team, err := mem.CreateTeam(ctx, store.CreateTeamInput{Name: "结算团队", QuotaCredits: "10.000000"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.AddMember(ctx, team.ID, user.ID, store.MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	reservationID := "00000000-0000-4000-8000-000000000801"
	now := time.Now().UTC().Truncate(time.Millisecond)
	if result, err := mem.AdmitUsage(ctx, store.ReserveQuotaInput{
		ID: reservationID, TeamID: team.ID, UserID: user.ID, RequestID: "settle-request",
		Micro: 1_000, TTL: time.Hour, CreatedAt: now,
	}); err != nil || result != store.ReserveReserved {
		t.Fatalf("准入失败: %s %v", result, err)
	}
	record := store.UsageRecord{
		ID: "00000000-0000-4000-8000-000000000802", RequestID: "settle-request",
		UserID: user.ID, TeamID: team.ID, Model: "m", UpstreamModel: "m",
		InputTokens: 10, OutputTokens: 5, CreditsMicro: 500,
		Status: store.UsageStatusError, ErrorType: "upstream_idle_timeout",
		CreatedAt: now.Add(time.Second),
	}
	return &memoryFixture{
		store:  &memoryStoreWrapper{PlatformStore: mem},
		userID: user.ID, teamID: team.ID, record: record,
		spool: filepath.Join(t.TempDir(), "pending"),
	}
}

func (f *memoryFixture) compensator() *SettlementCompensator {
	return NewSettlementCompensator(f.store.PlatformStore, f.spool,
		slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
}

func TestPersistFallsBackToSpoolAndReconcileSettles(t *testing.T) {
	ctx := context.Background()
	f := newMemoryFixture(t)
	comp := f.compensator()
	// 包一层让数据库入队失败，强制走本地 spool。
	failing := NewSettlementCompensator(f.store, f.spool,
		slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
	if err := failing.Persist(ctx, "00000000-0000-4000-8000-000000000801", f.record); err == nil {
		t.Fatal("数据库入队失败时应返回错误并提示已落 spool")
	}
	if _, err := os.Stat(filepath.Join(f.spool, "00000000-0000-4000-8000-000000000801.json")); err != nil {
		t.Fatalf("spool 文件未写入: %v", err)
	}
	if err := comp.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	// spool 已回灌并删除。
	if _, err := os.Stat(filepath.Join(f.spool, "00000000-0000-4000-8000-000000000801.json")); !os.IsNotExist(err) {
		t.Fatalf("spool 文件应在回灌后删除: %v", err)
	}
	usage, err := f.store.PlatformStore.ListUsage(ctx, store.UsageFilter{TeamID: f.teamID})
	if err != nil || len(usage) != 1 {
		t.Fatalf("补偿后应有 1 条 usage: %d %v", len(usage), err)
	}
	if usage[0].ID != f.record.ID || usage[0].CreditsMicro != f.record.CreditsMicro {
		t.Fatalf("usage 记录不符: %+v", usage[0])
	}
	team, err := f.store.PlatformStore.GetTeam(ctx, f.teamID)
	if err != nil || team.UsedCredits != "0.000500" {
		t.Fatalf("补偿后应实扣 0.000500: %+v %v", team, err)
	}
}

func TestReconcileRetriesPendingRow(t *testing.T) {
	ctx := context.Background()
	f := newMemoryFixture(t)
	comp := f.compensator()
	// 直接走数据库待结算表。
	if err := f.store.PlatformStore.EnqueuePendingSettlement(ctx, "00000000-0000-4000-8000-000000000801", f.record); err != nil {
		t.Fatal(err)
	}
	if err := comp.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := f.store.PlatformStore.ListPendingSettlements(ctx, store.PendingSettlementFilter{
		Status: store.PendingSettlementApplied, Limit: 10,
	})
	if err != nil || len(pending) != 1 {
		t.Fatalf("补偿状态应为 applied: %d %v", len(pending), err)
	}
	usage, _ := f.store.PlatformStore.ListUsage(ctx, store.UsageFilter{TeamID: f.teamID})
	if len(usage) != 1 {
		t.Fatalf("补偿应写入 usage: %d", len(usage))
	}
}

func TestRetryBackoffIsBounded(t *testing.T) {
	comp := &SettlementCompensator{RetryBase: time.Second, RetryMax: 10 * time.Second}
	if got := comp.retryDelay(1); got != time.Second {
		t.Fatalf("首次重试延迟不符: %s", got)
	}
	if got := comp.retryDelay(100); got != 10*time.Second {
		t.Fatalf("重试延迟应有上限: %s", got)
	}
}

func TestLegacyEstimatedPendingAndSpoolRequireReviewWithoutDebit(t *testing.T) {
	markers := []struct {
		name  string
		apply func(*store.UsageRecord)
	}{
		{"snapshot", func(r *store.UsageRecord) {
			r.PriceSnapshot = &store.UsagePricingSnapshot{Cost: store.UsageCostSnapshot{UsageEstimated: true}}
		}},
		{"missing_usage_error", func(r *store.UsageRecord) { r.ErrorType = "upstream_failure:usage_missing_estimated" }},
		{"invalid_background_usage_error", func(r *store.UsageRecord) { r.ErrorType = "background_usage_invalid_estimated:invalid_tokens" }},
	}
	for _, source := range []string{"pending", "spool"} {
		for _, marker := range markers {
			t.Run(source+"/"+marker.name, func(t *testing.T) {
				ctx := context.Background()
				f := newMemoryFixture(t)
				marker.apply(&f.record)
				const identity = "00000000-0000-4000-8000-000000000801"
				comp := f.compensator()
				if source == "spool" {
					if err := comp.writeSpool(identity, f.record); err != nil {
						t.Fatal(err)
					}
				} else if err := f.store.PlatformStore.EnqueuePendingSettlement(ctx, identity, f.record); err != nil {
					t.Fatal(err)
				}
				for range 2 {
					if err := comp.Reconcile(ctx); err != nil {
						t.Fatal(err)
					}
				}
				failed, err := f.store.PlatformStore.ListPendingSettlements(ctx, store.PendingSettlementFilter{Status: store.PendingSettlementFailed, Limit: 10})
				if err != nil || len(failed) != 1 || failed[0].LastError != "legacy_estimated_usage_requires_review" || failed[0].Record.CreditsMicro != f.record.CreditsMicro || failed[0].Record.InputTokens != f.record.InputTokens || failed[0].Record.OutputTokens != f.record.OutputTokens || failed[0].Record.ErrorType != f.record.ErrorType {
					t.Fatalf("旧估算须保留原记录转人工: %+v %v", failed, err)
				}
				if !legacyEstimatedUsage(failed[0].Record) {
					t.Fatal("原始估算标记丢失")
				}
				usage, err := f.store.PlatformStore.ListUsage(ctx, store.UsageFilter{TeamID: f.teamID})
				if err != nil || len(usage) != 0 {
					t.Fatalf("旧估算不得写 usage: %+v %v", usage, err)
				}
				team, err := f.store.PlatformStore.GetTeam(ctx, f.teamID)
				if err != nil || team.UsedCredits != "0.000000" || team.ReservedCredits != "0.000000" {
					t.Fatalf("旧估算不得改变账本: %+v %v", team, err)
				}
				if source == "spool" {
					if _, err := os.Stat(filepath.Join(f.spool, identity+".json")); !os.IsNotExist(err) {
						t.Fatalf("回灌持久化后应移除 spool: %v", err)
					}
				}
			})
		}
	}
}

func TestObservedIncompleteUsageStillReplaysExactlyOnce(t *testing.T) {
	ctx := context.Background()
	f := newMemoryFixture(t)
	const identity = "00000000-0000-4000-8000-000000000801"
	// 成本估算与用量估算不同；新请求即使缺少最终 usage，也可结算已合法观察的实际 token。
	f.record.PriceSnapshot = &store.UsagePricingSnapshot{Cost: store.UsageCostSnapshot{Status: "estimated", UsageEstimated: false}}
	f.record.ErrorType = "usage_missing_observed"
	comp := f.compensator()
	if err := comp.Persist(ctx, identity, f.record); err != nil {
		t.Fatal(err)
	}
	if err := comp.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.store.PlatformStore.MarkPendingSettlementRetry(ctx, identity, 1, "replay_after_restart", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := comp.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := comp.writeSpool(identity, f.record); err != nil {
		t.Fatal(err)
	}
	if err := comp.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	applied, err := f.store.PlatformStore.ListPendingSettlements(ctx, store.PendingSettlementFilter{Status: store.PendingSettlementApplied, Limit: 10})
	if err != nil || len(applied) != 1 {
		t.Fatalf("合法观察值应完成补偿: %+v %v", applied, err)
	}
	usage, err := f.store.PlatformStore.ListUsage(ctx, store.UsageFilter{TeamID: f.teamID})
	if err != nil || len(usage) != 1 || usage[0].CreditsMicro != 500 {
		t.Fatalf("重放应只入账一次: %+v %v", usage, err)
	}
	team, err := f.store.PlatformStore.GetTeam(ctx, f.teamID)
	if err != nil || team.UsedCredits != "0.000500" || team.ReservedCredits != "0.000000" {
		t.Fatalf("合法观察值应仅实扣一次: %+v %v", team, err)
	}
}

func TestUnknownBackgroundSubmissionIsRetainedForReviewWithoutSettlement(t *testing.T) {
	for _, source := range []string{"pending", "spool"} {
		t.Run(source, func(t *testing.T) {
			ctx := context.Background()
			f := newMemoryFixture(t)
			const identity = "00000000-0000-4000-8000-000000000801"
			f.record.CreditsMicro = 0
			f.record.ErrorType = "background_submission_unknown"
			comp := f.compensator()
			var err error
			if source == "spool" {
				err = comp.writeSpool(identity, f.record)
			} else {
				err = comp.Persist(ctx, identity, f.record)
			}
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := comp.Reconcile(ctx); err != nil {
					t.Fatal(err)
				}
			}
			failed, err := f.store.PlatformStore.ListPendingSettlements(ctx, store.PendingSettlementFilter{Status: store.PendingSettlementFailed, Limit: 10})
			if err != nil || len(failed) != 1 || failed[0].LastError != "background_submission_requires_review" {
				t.Fatalf("未知后台受理应留待人工核对: %+v %v", failed, err)
			}
			request, err := f.store.PlatformStore.GetQuotaReservation(ctx, identity)
			if err != nil || request == nil || request.Status != store.ReservationReserved || request.ReservedMicro != 0 {
				t.Fatalf("不能提前关闭或占用请求身份: %+v %v", request, err)
			}
			rows, err := f.store.PlatformStore.ListUsage(ctx, store.UsageFilter{TeamID: f.teamID})
			if err != nil || len(rows) != 0 {
				t.Fatalf("未知受理不能自动重放为账单: %+v %v", rows, err)
			}
		})
	}
}
