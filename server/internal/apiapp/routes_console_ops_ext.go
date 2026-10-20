package apiapp

import (
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// 控制台运维扩展：渠道池、ops 大盘、告警、预留对账。

func registerConsoleOpsExtRoutes(router *gin.Engine, deps *Deps) {
	group := router.Group("/console/api")

	// ---- 渠道池 ----

	routePublic := func(route store.UpstreamRouteRecord) gin.H {
		return gin.H{
			"id": route.ID, "modelId": route.ModelID, "providerId": route.ProviderID,
			"providerName": route.ProviderName, "upstreamModel": route.UpstreamModel,
			"priority": route.Priority, "weight": route.Weight, "enabled": route.Enabled,
			"createdAt": isoString(route.CreatedAt), "updatedAt": isoString(route.UpdatedAt),
		}
	}

	group.GET("/models/:modelId/routes", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		routes, err := deps.Store.ListUpstreamRoutes(c.Request.Context(), c.Param("modelId"), true)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		rows := make([]gin.H, 0, len(routes))
		for _, route := range routes {
			rows = append(rows, routePublic(route))
		}
		c.JSON(200, okData(gin.H{"routes": rows}))
	})

	group.POST("/models/:modelId/routes", func(c *gin.Context) {
		admin, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		modelID := c.Param("modelId")
		model, err := deps.Store.GetModel(ctx, modelID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if model == nil {
			c.JSON(404, fail(404, "模型不存在"))
			return
		}
		providerID, okProvider := trimmedRequired(body["providerId"], 36, 36)
		if !okProvider || !uuidShape(providerID) {
			c.JSON(400, fail(400, "providerId 不合法"))
			return
		}
		provider, err := deps.Store.GetUpstreamProvider(ctx, providerID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if provider == nil {
			c.JSON(404, fail(404, "供应商不存在"))
			return
		}
		upstreamModel, _ := trimmedOptional(body["upstreamModel"], 200)
		priority := int64(100)
		if raw, exists := body["priority"]; exists {
			parsed, okInt := intInRange(raw, 0, 1_000_000)
			if !okInt {
				c.JSON(400, fail(400, "priority 必须是非负整数"))
				return
			}
			priority = parsed
		}
		weight := int64(1)
		if raw, exists := body["weight"]; exists {
			parsed, okInt := intInRange(raw, 1, 10_000)
			if !okInt {
				c.JSON(400, fail(400, "weight 必须是 1..10000 的整数"))
				return
			}
			weight = parsed
		}
		enabled := true
		if raw, exists := body["enabled"]; exists {
			parsed, okBool := boolValue(raw)
			if !okBool {
				c.JSON(400, fail(400, "enabled 必须是布尔值"))
				return
			}
			enabled = parsed
		}
		route, err := deps.Store.CreateUpstreamRoute(ctx, store.CreateUpstreamRouteInput{
			ModelID: modelID, ProviderID: providerID, UpstreamModel: upstreamModel,
			Priority: priority, Weight: weight, Enabled: enabled,
		})
		if err != nil {
			if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "重复") {
				c.JSON(409, fail(409, "同一模型不能重复绑定同一供应商"))
				return
			}
			internalError(c, deps, err)
			return
		}
		if route == nil {
			c.JSON(404, fail(404, "供应商不存在"))
			return
		}
		writeAudit(ctx, deps, admin, "model.route_create", "model", modelID, map[string]any{
			"providerId": providerID, "priority": priority, "weight": weight,
		})
		deps.ConfigEvents.Emit(events.DomainModels)
		c.JSON(200, okData(gin.H{"route": routePublic(*route)}))
	})

	group.PATCH("/routes/:id", func(c *gin.Context) {
		admin, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		if !uuidShape(c.Param("id")) {
			c.JSON(400, fail(400, "路由 ID 不合法"))
			return
		}
		ctx := c.Request.Context()
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		var patch store.UpdateUpstreamRoutePatch
		if raw, exists := body["upstreamModel"]; exists {
			value, okString := trimmedOptional(raw, 200)
			if !okString {
				c.JSON(400, fail(400, "upstreamModel 不合法"))
				return
			}
			patch.UpstreamModel = &value
		}
		if raw, exists := body["priority"]; exists {
			value, okInt := intInRange(raw, 0, 1_000_000)
			if !okInt {
				c.JSON(400, fail(400, "priority 必须是非负整数"))
				return
			}
			patch.Priority = &value
		}
		if raw, exists := body["weight"]; exists {
			value, okInt := intInRange(raw, 1, 10_000)
			if !okInt {
				c.JSON(400, fail(400, "weight 必须是 1..10000 的整数"))
				return
			}
			patch.Weight = &value
		}
		if raw, exists := body["enabled"]; exists {
			value, okBool := boolValue(raw)
			if !okBool {
				c.JSON(400, fail(400, "enabled 必须是布尔值"))
				return
			}
			patch.Enabled = &value
		}
		route, err := deps.Store.UpdateUpstreamRoute(ctx, c.Param("id"), patch)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if route == nil {
			c.JSON(404, fail(404, "路由不存在"))
			return
		}
		writeAudit(ctx, deps, admin, "model.route_update", "route", route.ID, nil)
		deps.ConfigEvents.Emit(events.DomainModels)
		c.JSON(200, okData(gin.H{"route": routePublic(*route)}))
	})

	group.DELETE("/routes/:id", func(c *gin.Context) {
		admin, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		if !uuidShape(c.Param("id")) {
			c.JSON(400, fail(400, "路由 ID 不合法"))
			return
		}
		ctx := c.Request.Context()
		deleted, err := deps.Store.DeleteUpstreamRoute(ctx, c.Param("id"))
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if !deleted {
			c.JSON(404, fail(404, "路由不存在"))
			return
		}
		writeAudit(ctx, deps, admin, "model.route_delete", "route", c.Param("id"), nil)
		deps.ConfigEvents.Emit(events.DomainModels)
		c.JSON(200, gin.H{"code": 0})
	})

	group.POST("/providers/:id/recover", func(c *gin.Context) {
		admin, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		if !uuidShape(c.Param("id")) {
			c.JSON(400, fail(400, "供应商 ID 不合法"))
			return
		}
		ctx := c.Request.Context()
		provider, err := deps.Store.GetUpstreamProvider(ctx, c.Param("id"))
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if provider == nil {
			c.JSON(404, fail(404, "供应商不存在"))
			return
		}
		recovered, err := deps.Store.RecoverUpstreamProvider(ctx, provider.ID, deps.Now())
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if !recovered {
			c.JSON(400, fail(400, "当前不是自动熔断状态，无需恢复"))
			return
		}
		writeAudit(ctx, deps, admin, "provider.recover", "provider", provider.ID, nil)
		deps.ConfigEvents.Emit(events.DomainProviders)
		c.JSON(200, gin.H{"code": 0})
	})

	// ---- ops ----

	group.GET("/ops/overview", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		hours := int64(24)
		if raw, okParse := parseLeadingInt(c.Query("hours")); okParse && raw > 0 && raw <= 720 {
			hours = raw
		}
		since := deps.Now().Add(-time.Duration(hours) * time.Hour)
		overview, err := deps.Store.OpsOverview(c.Request.Context(), since)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		costSummary, err := deps.Store.AggregateUsageCosts(c.Request.Context(), store.UsageFilter{From: &since})
		if err != nil {
			internalError(c, deps, err)
			return
		}
		auditPending := int64(0)
		if deps.AuditSink != nil {
			count, countErr := deps.AuditSink.PendingCount()
			if countErr != nil {
				deps.Logger.Warn("读取审计 outbox 数量失败", "error", countErr)
			} else {
				auditPending = count
			}
		}
		providerStats := make([]gin.H, 0, len(overview.ProviderStats))
		for _, stat := range overview.ProviderStats {
			providerStats = append(providerStats, gin.H{
				"providerId": stat.ProviderID, "providerName": stat.ProviderName,
				"requests": stat.Requests, "errors": stat.Errors,
				"errorRate": stat.ErrorRate, "p95LatencyMs": stat.P95LatencyMs,
			})
		}
		c.JSON(200, okData(gin.H{
			"requests": overview.Requests, "successRequests": overview.SuccessRequests,
			"errorRequests": overview.ErrorRequests, "abortedRequests": overview.AbortedRequests,
			"inputTokens": overview.InputTokens, "outputTokens": overview.OutputTokens,
			"cacheReadTokens": overview.CacheReadTokens, "cacheCreationTokens": overview.CacheCreationTokens,
			"creditsMicro": overview.CreditsMicro,
			"p50LatencyMs": overview.P50LatencyMs, "p95LatencyMs": overview.P95LatencyMs,
			"p99LatencyMs":  overview.P99LatencyMs,
			"activeDevices": overview.ActiveDevices, "openReservations": overview.OpenReservations,
			"pendingSettlements": overview.PendingSettlements,
			"auditOutboxPending": auditPending,
			"providerStats":      providerStats,
			"costSummary":        toPublicUsageCostSummary(costSummary),
		}))
	})

	group.GET("/ops/series", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		hours := int64(24)
		if raw, okParse := parseLeadingInt(c.Query("hours")); okParse && raw > 0 && raw <= 720 {
			hours = raw
		}
		bucketMinutes := int64(5)
		if raw, okParse := parseLeadingInt(c.Query("bucketMinutes")); okParse && raw > 0 && raw <= 1440 {
			bucketMinutes = raw
		}
		points, err := deps.Store.OpsSeries(c.Request.Context(), deps.Now().Add(-time.Duration(hours)*time.Hour), bucketMinutes)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		rows := make([]gin.H, 0, len(points))
		for _, point := range points {
			rows = append(rows, gin.H{
				"bucketStart": point.BucketStart, "requests": point.Requests,
				"errors": point.Errors, "creditsMicro": point.CreditsMicro,
				"p95LatencyMs": point.P95LatencyMs,
			})
		}
		c.JSON(200, okData(gin.H{"series": rows}))
	})

	group.GET("/ops/reconciliation", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		limit := int64(50)
		if raw, okParse := parseLeadingInt(c.Query("limit")); okParse && raw > 0 && raw <= 500 {
			limit = raw
		}
		reservations, err := deps.Store.ListOpenReservations(c.Request.Context(), limit)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		now := deps.Now()
		rows := make([]gin.H, 0, len(reservations))
		stale := int64(0)
		for _, reservation := range reservations {
			if reservation.ExpiresAt.Before(now) {
				stale++
			}
			rows = append(rows, gin.H{
				"id": reservation.ID, "teamId": reservation.TeamID, "requestId": reservation.RequestID,
				"reservedCreditsMicro": reservation.ReservedMicro,
				"retainUntilSettled":   reservation.RetainUntilSettled,
				"createdAt":            isoString(reservation.CreatedAt), "expiresAt": isoString(reservation.ExpiresAt),
			})
		}
		pending, err := deps.Store.ListPendingSettlements(c.Request.Context(), store.PendingSettlementFilter{
			Status: store.PendingSettlementPending, Limit: limit,
		})
		if err != nil {
			internalError(c, deps, err)
			return
		}
		failed, err := deps.Store.ListPendingSettlements(c.Request.Context(), store.PendingSettlementFilter{
			Status: store.PendingSettlementFailed, Limit: limit,
		})
		if err != nil {
			internalError(c, deps, err)
			return
		}
		pendingCount := len(pending)
		failedCount := len(failed)
		pendingRows := make([]gin.H, 0, pendingCount+failedCount)
		for _, item := range append(pending, failed...) {
			pendingRows = append(pendingRows, gin.H{
				"reservationId": item.ReservationID, "requestId": item.Record.RequestID,
				"teamId": item.Record.TeamID, "creditsMicro": item.Record.CreditsMicro,
				"settlementStatus": item.Status, "usageStatus": item.Record.Status,
				"attempts":  item.Attempts,
				"lastError": item.LastError, "nextAttemptAt": isoString(item.NextAttemptAt),
				"createdAt": isoString(item.CreatedAt),
			})
		}
		c.JSON(200, okData(gin.H{
			"open": rows, "openCount": len(rows), "staleCount": stale,
			"pendingSettlements": pendingRows, "pendingSettlementCount": pendingCount,
			"failedSettlementCount": failedCount,
		}))
	})

	// ---- alerts ----

	alertRulePublic := func(rule store.AlertRuleRecord) gin.H {
		return gin.H{
			"id": rule.ID, "name": rule.Name, "metric": rule.Metric,
			"threshold": rule.Threshold, "windowMinutes": rule.WindowMinutes,
			"enabled": rule.Enabled, "createdAt": isoString(rule.CreatedAt), "updatedAt": isoString(rule.UpdatedAt),
		}
	}
	alertEventPublic := func(event store.AlertEventRecord) gin.H {
		return gin.H{
			"id": event.ID, "ruleId": event.RuleID, "ruleName": event.RuleName,
			"metric": event.Metric, "value": event.Value, "threshold": event.Threshold,
			"message": event.Message, "status": event.Status,
			"createdAt": isoString(event.CreatedAt), "resolvedAt": isoStringPtr(event.ResolvedAt),
		}
	}

	alertMetrics := map[string]bool{
		"error_rate": true, "p95_latency_ms": true, "open_reservations": true,
		"pending_settlements": true, "audit_outbox": true,
		"daily_credits": true, "provider_failures": true,
	}

	parseAlertRuleBody := func(c *gin.Context) (store.UpsertAlertRuleInput, bool) {
		body, ok := readObjectBody(c)
		if !ok {
			return store.UpsertAlertRuleInput{}, false
		}
		name, okName := trimmedRequired(body["name"], 1, 80)
		metric, okMetric := trimmedRequired(body["metric"], 1, 40)
		if !okName || !okMetric || !alertMetrics[metric] {
			c.JSON(400, fail(400, "告警名称或指标不合法"))
			return store.UpsertAlertRuleInput{}, false
		}
		threshold, okThreshold := parsePriceString(body["threshold"])
		if !okThreshold {
			c.JSON(400, fail(400, "threshold 必须是非负数"))
			return store.UpsertAlertRuleInput{}, false
		}
		window := int64(15)
		if raw, exists := body["windowMinutes"]; exists {
			parsed, okInt := intInRange(raw, 1, 1440)
			if !okInt {
				c.JSON(400, fail(400, "windowMinutes 必须是 1..1440 的整数"))
				return store.UpsertAlertRuleInput{}, false
			}
			window = parsed
		}
		enabled := true
		if raw, exists := body["enabled"]; exists {
			parsed, okBool := boolValue(raw)
			if !okBool {
				c.JSON(400, fail(400, "enabled 必须是布尔值"))
				return store.UpsertAlertRuleInput{}, false
			}
			enabled = parsed
		}
		return store.UpsertAlertRuleInput{
			Name: name, Metric: metric, Threshold: threshold,
			WindowMinutes: window, Enabled: enabled,
		}, true
	}

	group.GET("/ops/alert-rules", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		rules, err := deps.Store.ListAlertRules(c.Request.Context())
		if err != nil {
			internalError(c, deps, err)
			return
		}
		rows := make([]gin.H, 0, len(rules))
		for _, rule := range rules {
			rows = append(rows, alertRulePublic(rule))
		}
		c.JSON(200, okData(gin.H{"rules": rows}))
	})

	group.POST("/ops/alert-rules", func(c *gin.Context) {
		admin, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		input, ok := parseAlertRuleBody(c)
		if !ok {
			return
		}
		rule, err := deps.Store.UpsertAlertRule(c.Request.Context(), input)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		writeAudit(c.Request.Context(), deps, admin, "alert.rule_create", "alert_rule", rule.ID, nil)
		c.JSON(200, okData(gin.H{"rule": alertRulePublic(*rule)}))
	})

	group.PUT("/ops/alert-rules/:id", func(c *gin.Context) {
		admin, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		id := c.Param("id")
		if !uuidShape(id) {
			c.JSON(400, fail(400, "告警规则 ID 不合法"))
			return
		}
		input, ok := parseAlertRuleBody(c)
		if !ok {
			return
		}
		input.ID = id
		rule, err := deps.Store.UpsertAlertRule(c.Request.Context(), input)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if rule == nil {
			c.JSON(404, fail(404, "告警规则不存在"))
			return
		}
		writeAudit(c.Request.Context(), deps, admin, "alert.rule_update", "alert_rule", rule.ID, nil)
		c.JSON(200, okData(gin.H{"rule": alertRulePublic(*rule)}))
	})

	group.DELETE("/ops/alert-rules/:id", func(c *gin.Context) {
		admin, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		if !uuidShape(c.Param("id")) {
			c.JSON(400, fail(400, "告警规则 ID 不合法"))
			return
		}
		deleted, err := deps.Store.DeleteAlertRule(c.Request.Context(), c.Param("id"))
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if !deleted {
			c.JSON(404, fail(404, "告警规则不存在"))
			return
		}
		writeAudit(c.Request.Context(), deps, admin, "alert.rule_delete", "alert_rule", c.Param("id"), nil)
		c.JSON(200, gin.H{"code": 0})
	})

	group.GET("/ops/alerts", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		limit := int64(50)
		if raw, okParse := parseLeadingInt(c.Query("limit")); okParse && raw > 0 && raw <= 500 {
			limit = raw
		}
		status := strings.TrimSpace(c.Query("status"))
		if status != "" && status != "open" && status != "resolved" {
			c.JSON(400, fail(400, "status 只支持 open / resolved"))
			return
		}
		events, err := deps.Store.ListAlertEvents(c.Request.Context(), status, limit)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		rows := make([]gin.H, 0, len(events))
		for _, event := range events {
			rows = append(rows, alertEventPublic(event))
		}
		c.JSON(200, okData(gin.H{"alerts": rows}))
	})

	group.POST("/ops/alerts/:id/resolve", func(c *gin.Context) {
		admin, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		if !uuidShape(c.Param("id")) {
			c.JSON(400, fail(400, "告警事件 ID 不合法"))
			return
		}
		resolved, err := deps.Store.ResolveAlertEvent(c.Request.Context(), c.Param("id"), deps.Now())
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if !resolved {
			c.JSON(404, fail(404, "告警事件不存在或已处理"))
			return
		}
		writeAudit(c.Request.Context(), deps, admin, "alert.resolve", "alert_event", c.Param("id"), nil)
		c.JSON(200, gin.H{"code": 0})
	})
}

// opsAlertValue 供告警评估器复用。
func opsAlertMetricValue(metric string, overview *store.OpsOverview) (float64, string) {
	switch metric {
	case "error_rate":
		if overview.Requests == 0 {
			return 0, "窗口内没有请求"
		}
		return float64(overview.ErrorRequests) / float64(overview.Requests), "错误率"
	case "p95_latency_ms":
		if overview.P95LatencyMs == nil {
			return 0, "窗口内没有延迟样本"
		}
		return *overview.P95LatencyMs, "P95 延迟(ms)"
	case "open_reservations":
		return float64(overview.OpenReservations), "未释放预留数"
	case "pending_settlements":
		return float64(overview.PendingSettlements), "待结算补偿数"
	case "audit_outbox":
		return float64(overview.PendingAuditLogs), "待入库审计数"
	case "daily_credits":
		return float64(overview.CreditsMicro) / 1_000_000, "窗口内消费(CR)"
	case "provider_failures":
		total := int64(0)
		for _, stat := range overview.ProviderStats {
			total += stat.Errors
		}
		return float64(total), "上游错误数"
	default:
		return 0, metric
	}
}

// formatAlertValue:告警消息里的数值展示。
func formatAlertValue(metric string, value float64) string {
	switch metric {
	case "error_rate":
		return strconv.FormatFloat(value*100, 'f', 2, 64) + "%"
	default:
		return strconv.FormatFloat(value, 'f', 2, 64)
	}
}
