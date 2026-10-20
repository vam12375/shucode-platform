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

// memoryFixture 构造内存库 + 团队/预留/usage，返回可直接断言的对象。
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
	if result, err := mem.ReserveQuota(ctx, store.ReserveQuotaInput{
		ID: reservationID, TeamID: team.ID, UserID: user.ID, RequestID: "settle-request",
		Micro: 1_000, TTL: time.Hour, CreatedAt: now,
	}); err != nil || result != store.ReserveReserved {
		t.Fatalf("预留失败: %s %v", result, err)
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
