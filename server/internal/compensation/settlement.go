// Package compensation:网关结算失败后的持久化补偿。
//
// 结算仍以 Postgres 事务为权威；这里只负责把“已经发生但没写进 usage_records”
// 的请求完整保存下来并幂等重放。顺序是：先尝试写 pending_settlements，
// 数据库不可用时退到本地 spool 文件（容器挂载持久卷），数据库恢复后由
// Reconcile 先回灌 spool，再重试 pending 行。
package compensation

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

	"github.com/vam12375/shucode-platform/server/internal/store"
)

const (
	defaultReconcileInterval = 30 * time.Second
	defaultMaxAttempts       = 20
	defaultRetryBase         = 10 * time.Second
	defaultRetryMax          = time.Hour
	settleTimeout            = 10 * time.Second
)

var safeReservationID = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

type SettlementCompensator struct {
	Store       store.PlatformStore
	SpoolDir    string
	Logger      *slog.Logger
	Now         func() time.Time
	MaxAttempts int64
	RetryBase   time.Duration
	RetryMax    time.Duration
}

type spoolEntry struct {
	ReservationID string            `json:"reservationId"`
	Record        store.UsageRecord `json:"record"`
}

func NewSettlementCompensator(st store.PlatformStore, spoolDir string, logger *slog.Logger, now func() time.Time) *SettlementCompensator {
	if logger == nil {
		logger = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	return &SettlementCompensator{
		Store:       st,
		SpoolDir:    strings.TrimSpace(spoolDir),
		Logger:      logger,
		Now:         now,
		MaxAttempts: defaultMaxAttempts,
	}
}

// Persist 先写数据库待结算表；失败时落本地 spool。返回 nil 表示至少有一处
// 已持久化，调用方可安全停止重试。
func (c *SettlementCompensator) Persist(ctx context.Context, reservationID string, record store.UsageRecord) error {
	if c == nil || c.Store == nil {
		return errors.New("结算补偿器未配置")
	}
	if err := c.Store.EnqueuePendingSettlement(ctx, reservationID, record); err == nil {
		// 上一次 spool 回灌成功后可能残留同名文件；成功入库即可删除。
		_ = c.removeSpool(reservationID)
		return nil
	} else if spoolErr := c.writeSpool(reservationID, record); spoolErr != nil {
		return fmt.Errorf("待结算表写入失败（%v），本地 spool 也失败: %w", err, spoolErr)
	} else {
		return fmt.Errorf("待结算表写入失败，已落本地 spool: %w", err)
	}
}

// Reconcile 执行一轮补偿：spool 回灌 → pending 到期行重放。
func (c *SettlementCompensator) Reconcile(ctx context.Context) error {
	if c == nil || c.Store == nil {
		return nil
	}
	var errs []error
	if c.SpoolDir != "" {
		if err := c.drainSpool(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	now := c.Now()
	items, err := c.Store.ListPendingSettlements(ctx, store.PendingSettlementFilter{
		Status:    store.PendingSettlementPending,
		DueBefore: &now,
		Limit:     100,
	})
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("读取待结算表: %w", err))...)
	}
	for _, item := range items {
		// 旧版本可能把最大输出估算写入补偿；升级后不能把这些 token 当成真实用量自动实扣。
		if reason := settlementReviewReason(item.Record); reason != "" {
			if err := c.Store.MarkPendingSettlementFailed(ctx, item.ReservationID, item.Attempts, reason); err != nil {
				errs = append(errs, fmt.Errorf("标记补偿待核对失败 %s: %w", item.ReservationID, err))
			}
			c.Logger.Warn("结算记录转人工核对，未自动扣费", "reservationId", item.ReservationID, "requestId", item.Record.RequestID)
			continue
		}
		settleCtx, cancel := context.WithTimeout(ctx, settleTimeout)
		settleErr := c.Store.SettleReservation(settleCtx, item.ReservationID, item.Record)
		cancel()
		if settleErr == nil || errors.Is(settleErr, store.ErrCreditChargeCapped) {
			if err := c.Store.MarkPendingSettlementApplied(ctx, item.ReservationID, c.Now()); err != nil {
				errs = append(errs, fmt.Errorf("标记补偿完成失败 %s: %w", item.ReservationID, err))
			}
			if errors.Is(settleErr, store.ErrCreditChargeCapped) {
				c.Logger.Warn("待结算补偿已按团队额度下限封顶", "reservationId", item.ReservationID, "requestId", item.Record.RequestID)
			} else {
				c.Logger.Info("待结算补偿完成", "reservationId", item.ReservationID, "requestId", item.Record.RequestID)
			}
			continue
		}
		attempts := item.Attempts + 1
		if errors.Is(settleErr, store.ErrReservationNotFound) {
			// 预留行已不存在（数据被清理/删除），重试不会成功，直接转人工对账。
			if err := c.Store.MarkPendingSettlementFailed(ctx, item.ReservationID, attempts, truncateError(settleErr)); err != nil {
				errs = append(errs, fmt.Errorf("标记补偿失败失败 %s: %w", item.ReservationID, err))
			}
			c.Logger.Error("待结算补偿的预留不存在，转人工对账",
				"reservationId", item.ReservationID, "requestId", item.Record.RequestID)
			continue
		}
		if max := c.maxAttempts(); attempts >= max {
			if err := c.Store.MarkPendingSettlementFailed(ctx, item.ReservationID, attempts, truncateError(settleErr)); err != nil {
				errs = append(errs, fmt.Errorf("标记补偿失败失败 %s: %w", item.ReservationID, err))
			}
			c.Logger.Error("待结算补偿超过重试上限，转人工对账",
				"reservationId", item.ReservationID, "requestId", item.Record.RequestID,
				"attempts", attempts, "error", settleErr)
			continue
		}
		next := c.Now().Add(c.retryDelay(attempts))
		if err := c.Store.MarkPendingSettlementRetry(ctx, item.ReservationID, attempts, truncateError(settleErr), next); err != nil {
			errs = append(errs, fmt.Errorf("记录补偿重试失败 %s: %w", item.ReservationID, err))
		}
		c.Logger.Warn("待结算补偿重试失败",
			"reservationId", item.ReservationID, "requestId", item.Record.RequestID,
			"attempts", attempts, "nextAttemptAt", next, "error", settleErr)
	}
	return errors.Join(errs...)
}

// Run 启动后台补偿循环；interval<=0 时使用默认 30s。
func (c *SettlementCompensator) Run(ctx context.Context, interval time.Duration) {
	if c == nil || c.Store == nil {
		return
	}
	if interval <= 0 {
		interval = defaultReconcileInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := c.Reconcile(ctx); err != nil {
				c.Logger.Error("结算补偿轮询失败", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (c *SettlementCompensator) maxAttempts() int64 {
	if c.MaxAttempts > 0 {
		return c.MaxAttempts
	}
	return defaultMaxAttempts
}

func (c *SettlementCompensator) retryDelay(attempts int64) time.Duration {
	base := c.RetryBase
	if base <= 0 {
		base = defaultRetryBase
	}
	maxDelay := c.RetryMax
	if maxDelay <= 0 {
		maxDelay = defaultRetryMax
	}
	delay := base
	for i := int64(1); i < attempts && delay < maxDelay; i++ {
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
	return delay
}

func (c *SettlementCompensator) drainSpool(ctx context.Context) error {
	entries, err := os.ReadDir(c.SpoolDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取结算 spool: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(c.SpoolDir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			c.Logger.Warn("读取结算 spool 文件失败", "file", path, "error", err)
			continue
		}
		var item spoolEntry
		if err := json.Unmarshal(raw, &item); err != nil {
			c.Logger.Error("结算 spool 文件损坏，保留待人工处理", "file", path, "error", err)
			continue
		}
		if !safeReservationID.MatchString(item.ReservationID) {
			c.Logger.Error("结算 spool 预留 ID 非法，保留待人工处理", "file", path)
			continue
		}
		if err := c.Store.EnqueuePendingSettlement(ctx, item.ReservationID, item.Record); err != nil {
			// 数据库仍不可用：保留文件，下一轮继续。
			return fmt.Errorf("回灌结算 spool 失败: %w", err)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			c.Logger.Warn("删除已回灌的结算 spool 失败", "file", path, "error", err)
		}
	}
	return nil
}

func (c *SettlementCompensator) writeSpool(reservationID string, record store.UsageRecord) error {
	if c.SpoolDir == "" {
		return errors.New("未配置 PENDING_SETTLEMENT_DIR")
	}
	if !safeReservationID.MatchString(reservationID) {
		return fmt.Errorf("预留 ID 非法: %s", reservationID)
	}
	if err := os.MkdirAll(c.SpoolDir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(spoolEntry{ReservationID: reservationID, Record: record})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.SpoolDir, reservationID+".*.tmp")
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
	return os.Rename(tmpName, filepath.Join(c.SpoolDir, reservationID+".json"))
}

func (c *SettlementCompensator) removeSpool(reservationID string) error {
	if c.SpoolDir == "" {
		return nil
	}
	err := os.Remove(filepath.Join(c.SpoolDir, reservationID+".json"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func truncateError(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if len(text) > 1000 {
		return text[:1000]
	}
	return text
}

func legacyEstimatedUsage(record store.UsageRecord) bool {
	return record.PriceSnapshot != nil && record.PriceSnapshot.Cost.UsageEstimated ||
		strings.Contains(record.ErrorType, "usage_missing_estimated") ||
		strings.Contains(record.ErrorType, "background_usage_invalid_estimated")
}

func settlementReviewReason(record store.UsageRecord) string {
	if legacyEstimatedUsage(record) {
		return "legacy_estimated_usage_requires_review"
	}
	if record.ErrorType == "background_submission_unknown" {
		return "background_submission_requires_review"
	}
	return ""
}
