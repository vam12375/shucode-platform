package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func serviceWithStoredProject(t *testing.T, raw string) *Service {
	t.Helper()
	ctx := context.Background()
	st, err := store.NewMemoryStore(ctx, store.MemoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	repo := Repository{Store: st}
	key := ProjectKey("team", "project")
	if err := st.SetConfigValue(ctx, key, []byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Index(ctx, key); err != nil {
		t.Fatal(err)
	}
	return &Service{Repo: repo, WorkerID: "worker"}
}

func TestHistoricalProjectFirstEditAndConcurrentWrite(t *testing.T) {
	// 旧 Project JSON 没有 analysisChargedMicro；直接读历史形状才能捕捉重编码导致的假冲突。
	s := serviceWithStoredProject(t, `{"id":"project","teamId":"team","userId":"user","version":3,"status":"draft",
		"brief":{"mode":"auto","maxCreditsMicro":0},"shots":[],"models":[],"quoteMicro":0,"chargedMicro":0,"cancelRequested":false}`)
	ctx := context.Background()
	_, err := s.Mutate(ctx, "team", "project", 3, func(p *Project) error {
		if _, err := s.Action(ctx, "team", "project", 3, "cancel"); err != nil {
			t.Fatalf("历史快照的首次编辑失败: %v", err)
		}
		p.Status = "analyzing"
		return nil
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("旧写入应被存储 CAS 拒绝，实际 %v", err)
	}
	p, err := s.Repo.Get(ctx, "team", "project")
	if err != nil || p == nil || p.Version != 4 || p.Status != "cancelled" || !p.CancelRequested {
		t.Fatalf("并发写入覆盖了已保存的取消: %+v %v", p, err)
	}
}

func TestWorkerResumesHistoricalSubmittingShot(t *testing.T) {
	s := serviceWithStoredProject(t, `{"id":"project","teamId":"team","userId":"user","version":7,"status":"running",
		"brief":{"mode":"auto"},"models":[],"shots":[{"plan":{"duration":5},"attempt":1,"quoteMicro":10,
		"chargedMicro":0,"status":"video_submitting"}],"quoteMicro":10,"chargedMicro":0,"cancelRequested":false}`)
	ctx := context.Background()
	s.Tick(ctx)
	p, err := s.Repo.Get(ctx, "team", "project")
	if err != nil || p == nil || p.Status != "reconciliation_required" || p.LeaseOwner != "" || p.Version <= 7 ||
		p.Shots[0].Status != "reconciliation_required" || p.Error != "provider_submission_unknown" || p.ChargedMicro != 0 {
		t.Fatalf("历史进行中镜头应进入核对并释放租约: %+v %v", p, err)
	}
}

func TestProjectWritePreservesUnknownSnapshotFields(t *testing.T) {
	s := serviceWithStoredProject(t, `{"id":"project","teamId":"team","version":1,"status":"draft",
		"future":{"sequence":9007199254740993},"brief":{"mode":"auto","futureHint":"keep"},
		"analysis":{"taskId":"a","inputId":"i","workspacePath":"w","futureStage":2},
		"shots":[{"status":"pending","plan":{},"futureTask":"task-identity"}]}`)
	ctx := context.Background()
	p, err := s.Mutate(ctx, "team", "project", 1, func(p *Project) error {
		p.Brief.Product = "updated"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	key := ProjectKey("team", "project")
	raw, err := s.Repo.Store.GetConfigValue(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Future struct{ Sequence int64 }
		Brief  struct{ FutureHint, Product string }
		Shots  []struct{ FutureTask string }
	}
	if err = json.Unmarshal(raw, &saved); err != nil || saved.Future.Sequence != 9007199254740993 ||
		saved.Brief.FutureHint != "keep" || saved.Brief.Product != "updated" || len(saved.Shots) != 1 || saved.Shots[0].FutureTask != "task-identity" {
		t.Fatalf("扩展字段未完整保留: %s (%v)", raw, err)
	}
	_, err = s.Mutate(ctx, "team", "project", p.Version, func(p *Project) error {
		p.Shots = append([]Shot{{Status: "pending"}}, p.Shots...)
		return nil
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("包含未知执行身份的镜头无法安全重排: %v", err)
	}
	_, err = s.Mutate(ctx, "team", "project", p.Version, func(p *Project) error {
		p.Analysis = nil
		return nil
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("清空对象会删除未知阶段: %v", err)
	}
	unchanged, err := s.Repo.Store.GetConfigValue(ctx, key)
	if err != nil || !bytes.Equal(raw, unchanged) {
		t.Fatalf("冲突后快照发生变化: %v", err)
	}
}
