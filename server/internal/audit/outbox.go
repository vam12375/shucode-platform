// Package audit:敏感操作审计的可靠落库队列。
//
// 业务操作与审计仍尽量在同一事务（如团队额度调整）；无法同事务的调用方
// 在 InsertAuditLog 失败时把完整事件写入本地 outbox，数据库恢复后按
// event_id 幂等重放。这样不会出现“操作成功、审计只留在日志里”的静默缺口。
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

const defaultInterval = 30 * time.Second

var safeEventID = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

type Sink struct {
	Store  store.PlatformStore
	Dir    string
	Logger *slog.Logger
}

func NewSink(st store.PlatformStore, dir string, logger *slog.Logger) *Sink {
	if logger == nil {
		logger = slog.Default()
	}
	return &Sink{Store: st, Dir: strings.TrimSpace(dir), Logger: logger}
}

// Write 先直接入库；失败则写本地 outbox。spooled=true 表示事件已持久化
// 但尚未进数据库，调用方应记录告警而不是当作完全成功。
func (s *Sink) Write(ctx context.Context, entry store.InsertAuditLogInput) (spooled bool, err error) {
	if s == nil || s.Store == nil {
		return false, errors.New("审计 sink 未配置")
	}
	if entry.EventID == "" {
		entry.EventID = uuid.NewString()
	}
	if err := s.Store.InsertAuditLog(ctx, entry); err == nil {
		_ = s.remove(entry.EventID)
		return false, nil
	} else if spoolErr := s.write(entry); spoolErr != nil {
		return false, fmt.Errorf("审计入库失败（%v），本地 outbox 也失败: %w", err, spoolErr)
	}
	return true, nil
}

func (s *Sink) Run(ctx context.Context, interval time.Duration) {
	if s == nil || s.Store == nil {
		return
	}
	if interval <= 0 {
		interval = defaultInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := s.Reconcile(ctx); err != nil {
				s.Logger.Error("审计 outbox 重放失败", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// PendingCount 返回尚未重放的事件数，供运维大盘/告警观察。
func (s *Sink) PendingCount() (int64, error) {
	if s == nil || s.Dir == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var count int64
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	return count, nil
}

func (s *Sink) Reconcile(ctx context.Context) error {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取审计 outbox: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.Dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			s.Logger.Warn("读取审计 outbox 文件失败", "file", path, "error", err)
			continue
		}
		var item store.InsertAuditLogInput
		if err := json.Unmarshal(raw, &item); err != nil {
			s.Logger.Error("审计 outbox 文件损坏，保留待人工处理", "file", path, "error", err)
			continue
		}
		if !safeEventID.MatchString(item.EventID) {
			s.Logger.Error("审计 outbox event_id 非法，保留待人工处理", "file", path)
			continue
		}
		if err := s.Store.InsertAuditLog(ctx, item); err != nil {
			return fmt.Errorf("重放审计事件 %s: %w", item.EventID, err)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			s.Logger.Warn("删除已重放的审计 outbox 失败", "file", path, "error", err)
		}
	}
	return nil
}

func (s *Sink) write(entry store.InsertAuditLogInput) error {
	if s.Dir == "" {
		return errors.New("未配置 AUDIT_OUTBOX_DIR")
	}
	if !safeEventID.MatchString(entry.EventID) {
		return fmt.Errorf("event_id 非法: %s", entry.EventID)
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, entry.EventID+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(s.Dir, entry.EventID+".json"))
}

func (s *Sink) remove(eventID string) error {
	if s.Dir == "" {
		return nil
	}
	err := os.Remove(filepath.Join(s.Dir, eventID+".json"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
