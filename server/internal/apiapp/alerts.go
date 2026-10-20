package apiapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

// 告警评估器：按规则窗口计算 ops 指标，阈值触发/恢复时写事件并可选推送 webhook。
// 评估在 api 进程内执行；事件落库保证多实例重启后仍可追溯。

func StartAlertEvaluator(ctx context.Context, deps *Deps, interval time.Duration) {
	if interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				evaluateAlerts(ctx, deps)
			}
		}
	}()
}

func StartReservationSweeper(ctx context.Context, deps *Deps, interval time.Duration) {
	if interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				count, err := deps.Store.ExpireStaleReservations(ctx, now.Add(-deps.Env.ReservationTTL))
				if err != nil {
					deps.Logger.Warn("过期预留回收失败", "error", err)
					continue
				}
				if count > 0 {
					deps.Logger.Info("已回收过期预留", "count", count)
				}
			}
		}
	}()
}

func evaluateAlerts(ctx context.Context, deps *Deps) {
	rules, err := deps.Store.ListAlertRules(ctx)
	if err != nil {
		deps.Logger.Warn("告警规则查询失败", "error", err)
		return
	}
	overviews := map[int64]*store.OpsOverview{}
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		window := rule.WindowMinutes
		if window <= 0 {
			window = 15
		}
		overview, ok := overviews[window]
		if !ok {
			computed, err := deps.Store.OpsOverview(ctx, deps.Now().Add(-time.Duration(window)*time.Minute))
			if err != nil {
				deps.Logger.Warn("告警指标计算失败", "ruleId", rule.ID, "error", err)
				continue
			}
			// 审计 outbox 是进程本地队列，指标由 api 层补充。
			if deps.AuditSink != nil {
				if count, countErr := deps.AuditSink.PendingCount(); countErr == nil {
					computed.PendingAuditLogs = count
				}
			}
			overview = computed
			overviews[window] = overview
		}
		value, label := opsAlertMetricValue(rule.Metric, overview)
		threshold, err := strconv.ParseFloat(rule.Threshold, 64)
		if err != nil {
			deps.Logger.Warn("告警阈值解析失败", "ruleId", rule.ID, "threshold", rule.Threshold)
			continue
		}
		if value < threshold {
			resolved, err := deps.Store.ResolveOpenAlertEventsForRule(ctx, rule.ID, deps.Now())
			if err != nil {
				deps.Logger.Warn("告警恢复写库失败", "ruleId", rule.ID, "error", err)
			} else if resolved > 0 {
				deps.Logger.Info("告警已恢复", "ruleId", rule.ID, "name", rule.Name)
			}
			continue
		}
		open, err := deps.Store.OpenAlertEventForRule(ctx, rule.ID)
		if err != nil {
			deps.Logger.Warn("告警事件查询失败", "ruleId", rule.ID, "error", err)
			continue
		}
		if open != nil {
			continue
		}
		message := fmt.Sprintf("%s：%s 当前 %s，阈值 %s（窗口 %d 分钟）",
			rule.Name, label, formatAlertValue(rule.Metric, value), rule.Threshold, window)
		event, err := deps.Store.InsertAlertEvent(ctx, rule, strconv.FormatFloat(value, 'f', 6, 64), message, deps.Now())
		if err != nil {
			deps.Logger.Warn("告警事件写入失败", "ruleId", rule.ID, "error", err)
			continue
		}
		deps.Logger.Warn("告警触发", "ruleId", rule.ID, "name", rule.Name, "value", value, "threshold", threshold)
		sendAlertWebhook(ctx, deps, rule.Name, rule.Metric, message, value, threshold)
		_ = event
	}
}

func sendAlertWebhook(ctx context.Context, deps *Deps, name, metric, message string, value, threshold float64) {
	if deps.Env.AlertWebhookURL == "" {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"name": name, "metric": metric, "message": message,
		"value": value, "threshold": threshold, "at": isoString(deps.Now()),
	})
	if err != nil {
		return
	}
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, deps.Env.AlertWebhookURL, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("content-type", "application/json")
	client := deps.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		deps.Logger.Warn("告警 webhook 推送失败", "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		deps.Logger.Warn("告警 webhook 返回非 2xx", "status", resp.StatusCode)
	}
}
