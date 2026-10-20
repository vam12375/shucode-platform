package audit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

type failingAuditStore struct {
	store.PlatformStore
}

func (s *failingAuditStore) InsertAuditLog(context.Context, store.InsertAuditLogInput) error {
	return errors.New("database unavailable")
}

func TestAuditOutboxSpoolsAndReplaysIdempotently(t *testing.T) {
	ctx := context.Background()
	mem, err := store.NewMemoryStore(ctx, store.MemoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer mem.Dispose(ctx)
	user, err := mem.CreateUser(ctx, store.UserInput{
		Email: "audit@test.local", PasswordHash: "x", DisplayName: "审计", Role: store.RoleUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "audit")
	entry := store.InsertAuditLogInput{
		EventID:     "00000000-0000-4000-8000-000000000901",
		ActorUserID: user.ID,
		Action:      "team.update",
		TargetType:  "team",
		TargetID:    "00000000-0000-4000-8000-000000000902",
		Detail:      []byte(`{"fields":["quota"]}`),
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	failing := NewSink(&failingAuditStore{PlatformStore: mem}, dir, logger)
	spooled, err := failing.Write(ctx, entry)
	if err != nil || !spooled {
		t.Fatalf("入库失败时应落本地 outbox: spooled=%v err=%v", spooled, err)
	}
	if _, err := os.Stat(filepath.Join(dir, entry.EventID+".json")); err != nil {
		t.Fatalf("审计 outbox 文件未写入: %v", err)
	}
	if count, err := failing.PendingCount(); err != nil || count != 1 {
		t.Fatalf("审计 outbox 计数不符: %d %v", count, err)
	}
	real := NewSink(mem, dir, logger)
	if err := real.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, entry.EventID+".json")); !os.IsNotExist(err) {
		t.Fatalf("重放后应删除 outbox 文件: %v", err)
	}
	if count, err := real.PendingCount(); err != nil || count != 0 {
		t.Fatalf("重放后 outbox 计数应清零: %d %v", count, err)
	}
	limit := int64(10)
	logs, err := mem.ListAuditLogs(ctx, store.AuditLogFilter{Limit: &limit})
	if err != nil || len(logs) != 1 || logs[0].Action != "team.update" {
		t.Fatalf("审计重放结果不符: %+v %v", logs, err)
	}
	// 同一 event_id 再次写入（例如入库成功但客户端看到错误）必须幂等。
	if err := mem.InsertAuditLog(ctx, entry); err != nil {
		t.Fatal(err)
	}
	logs, _ = mem.ListAuditLogs(ctx, store.AuditLogFilter{Limit: &limit})
	if len(logs) != 1 {
		t.Fatalf("event_id 应保证重放幂等: %d", len(logs))
	}
}

func TestAuditOutboxCorruptFileIsKept(t *testing.T) {
	ctx := context.Background()
	mem, err := store.NewMemoryStore(ctx, store.MemoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer mem.Dispose(ctx)
	dir := filepath.Join(t.TempDir(), "audit")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "00000000-0000-4000-8000-000000000903.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := NewSink(mem, dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := sink.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("损坏文件应保留待人工处理: %v", err)
	}
}
