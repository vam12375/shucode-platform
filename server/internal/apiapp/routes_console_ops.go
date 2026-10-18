package apiapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/contracts"
	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/pricing"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// console.ts 运维面:上游供应商 / 定价库 / 平台配置 / 用量 / 设备 / 审计。

const (
	providerTestTimeoutMs    = 30_000
	upstreamModelListTimeout = 15_000
	anthropicVersionFallback = "2023-06-01"
)

func normalizeUpstreamBaseURL(raw string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if len(trimmed) >= 3 && strings.EqualFold(trimmed[len(trimmed)-3:], "/v1") {
		trimmed = trimmed[:len(trimmed)-3]
	}
	return strings.TrimRight(trimmed, "/")
}

func registerConsoleOpsRoutes(router *gin.Engine, deps *Deps) {
	group := router.Group("/console/api")

	// 按协议构造最小推理探测请求(路径与网关同口径:base_url 是协议根,/v1 由平台拼接)。
	probeRequest := func(protocol store.UpstreamProtocol, model string) (string, []byte) {
		switch protocol {
		case store.ProtocolOpenAIResponses:
			return "/v1/responses", mustJSONBytes(map[string]any{
				"model": model, "input": "ping", "max_output_tokens": 16,
			})
		case store.ProtocolAnthropicMessages:
			return "/v1/messages", mustJSONBytes(map[string]any{
				"model": model, "max_tokens": 1,
				"messages": []any{map[string]any{"role": "user", "content": "ping"}},
			})
		default:
			return "/v1/chat/completions", mustJSONBytes(map[string]any{
				"model": model, "messages": []any{map[string]any{"role": "user", "content": "ping"}},
				"max_tokens": 1, "stream": false,
			})
		}
	}

	decryptProviderKey := func(c *gin.Context, provider *store.UpstreamProviderRecord) (string, bool) {
		if provider.APIKeyCiphertext == "" {
			return "", true
		}
		if deps.SecretBox == nil {
			c.JSON(500, fail(500, "平台密钥未配置，无法解密该供应商的 API Key"))
			return "", false
		}
		apiKey, err := deps.SecretBox.Decrypt(provider.APIKeyCiphertext)
		if err != nil {
			c.JSON(500, fail(500, "供应商 API Key 解密失败（PLATFORM_SECRET_KEY 是否更换过？）"))
			return "", false
		}
		return apiKey, true
	}

	// ---- providers ----

	group.GET("/providers", func(c *gin.Context) {
		if _, ok := requireAdmin(c.Request.Context(), deps, c); !ok {
			return
		}
		providers, err := deps.Store.ListUpstreamProviders(c.Request.Context(), true)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		rows := make([]providerPublic, 0, len(providers))
		for i := range providers {
			rows = append(rows, toPublicProvider(&providers[i]))
		}
		c.JSON(200, okData(gin.H{"providers": rows}))
	})

	group.POST("/providers", func(c *gin.Context) {
		admin, ok := requireAdmin(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		name, okName := trimmedRequired(body["name"], 1, 80)
		baseURL, okBase := trimmedRequired(body["baseUrl"], 1, 300)
		if !okName || !okBase {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		protocol := store.ProtocolOpenAIChatCompletions
		if raw, exists := body["protocol"]; exists {
			parsed, okProtocol := parseProtocol(raw)
			if !okProtocol {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			protocol = parsed
		}
		apiKey, hasKey, hasKeyField, okKey := parseNullableTrimmedString(body["apiKey"], 400)
		if !okKey || (hasKeyField && body["apiKey"] != nil && !hasKey) {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		ciphertext, hint := "", ""
		if hasKeyField && body["apiKey"] != nil {
			if apiKey == "" {
				// 显式空串 = 清除(与 null 同义)。
			} else if deps.SecretBox == nil {
				c.JSON(400, fail(400, "未配置 PLATFORM_SECRET_KEY，无法安全存储上游密钥"))
				return
			} else {
				encrypted, err := deps.SecretBox.Encrypt(apiKey)
				if err != nil {
					internalError(c, deps, err)
					return
				}
				ciphertext, hint = encrypted, hintOf(apiKey)
			}
		}
		anthropicVersion, _, hasVersionField, okVersion := parseNullableTrimmedString(body["anthropicVersion"], 40)
		if !okVersion {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		timeoutMs, hasTimeout, okTimeout := parseNullableInt(body["timeoutMs"], 1000, 600_000)
		if !okTimeout {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		enabled := true
		if raw, exists := body["enabled"]; exists {
			parsed, okBool := boolValue(raw)
			if !okBool {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			enabled = parsed
		}
		input := store.CreateUpstreamProviderInput{
			Name: name, BaseURL: normalizeUpstreamBaseURL(baseURL), Protocol: protocol,
			APIKeyCiphertext: ciphertext, APIKeyHint: hint, Enabled: &enabled,
		}
		if hasVersionField && anthropicVersion != "" {
			input.AnthropicVersion = &anthropicVersion
		}
		_ = hasKey
		if hasTimeout && timeoutMs > 0 {
			input.TimeoutMs = &timeoutMs
		}
		provider, err := deps.Store.CreateUpstreamProvider(ctx, input)
		if err != nil {
			c.JSON(409, fail(409, err.Error()))
			return
		}
		writeAudit(ctx, deps, admin, "provider.create", "provider", provider.ID, map[string]any{
			"name": provider.Name, "protocol": string(provider.Protocol),
		})
		deps.ConfigEvents.Emit(events.DomainProviders)
		c.JSON(200, okData(gin.H{"provider": toPublicProvider(provider)}))
	})

	group.PATCH("/providers/:id", func(c *gin.Context) {
		admin, ok := requireAdmin(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		var patch store.UpdateUpstreamProviderPatch
		if raw, exists := body["name"]; exists {
			name, okName := trimmedRequired(raw, 1, 80)
			if !okName {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			patch.Name = &name
		}
		if raw, exists := body["baseUrl"]; exists {
			baseURL, okBase := trimmedRequired(raw, 1, 300)
			if !okBase {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			normalized := normalizeUpstreamBaseURL(baseURL)
			patch.BaseURL = &normalized
		}
		if raw, exists := body["protocol"]; exists {
			parsed, okProtocol := parseProtocol(raw)
			if !okProtocol {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			patch.Protocol = &parsed
		}
		if _, hasKeyField := body["apiKey"]; hasKeyField {
			apiKey, hasKey, _, okKey := parseNullableTrimmedString(body["apiKey"], 400)
			if !okKey {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			patch.ProvideAPIKey = true
			if hasKey && apiKey != "" {
				if deps.SecretBox == nil {
					c.JSON(400, fail(400, "未配置 PLATFORM_SECRET_KEY，无法安全存储上游密钥"))
					return
				}
				encrypted, err := deps.SecretBox.Encrypt(apiKey)
				if err != nil {
					internalError(c, deps, err)
					return
				}
				patch.APIKeyCiphertext, patch.APIKeyHint = encrypted, hintOf(apiKey)
			}
		}
		if _, hasVersionField := body["anthropicVersion"]; hasVersionField {
			version, hasVersion, _, okVersion := parseNullableTrimmedString(body["anthropicVersion"], 40)
			if !okVersion {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			patch.ProvideAnthropicVersion = true
			if hasVersion && version != "" {
				patch.AnthropicVersion = &version
			}
		}
		if _, hasTimeoutField := body["timeoutMs"]; hasTimeoutField {
			timeout, hasTimeout, okTimeout := parseNullableInt(body["timeoutMs"], 1000, 600_000)
			if !okTimeout {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			patch.ProvideTimeoutMs = true
			if hasTimeout && timeout > 0 {
				patch.TimeoutMs = &timeout
			}
		}
		if raw, exists := body["enabled"]; exists {
			parsed, okBool := boolValue(raw)
			if !okBool {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			patch.Enabled = &parsed
		}
		provider, err := deps.Store.UpdateUpstreamProvider(ctx, c.Param("id"), patch)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if provider == nil {
			c.JSON(404, fail(404, "供应商不存在"))
			return
		}
		auditFields := make([]string, 0, len(body))
		for key := range body {
			auditFields = append(auditFields, key)
		}
		sort.Strings(auditFields)
		writeAudit(ctx, deps, admin, "provider.update", "provider", provider.ID, map[string]any{"fields": auditFields})
		deps.ConfigEvents.Emit(events.DomainProviders)
		c.JSON(200, okData(gin.H{"provider": toPublicProvider(provider)}))
	})

	group.DELETE("/providers/:id", func(c *gin.Context) {
		admin, ok := requireAdmin(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		existing, err := deps.Store.GetUpstreamProvider(ctx, c.Param("id"))
		if err != nil {
			internalError(c, deps, err)
			return
		}
		removed, err := deps.Store.DeleteUpstreamProvider(ctx, c.Param("id"))
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if !removed {
			c.JSON(404, fail(404, "供应商不存在"))
			return
		}
		name := any(nil)
		if existing != nil {
			name = existing.Name
		}
		writeAudit(ctx, deps, admin, "provider.delete", "provider", c.Param("id"), map[string]any{"name": name})
		deps.ConfigEvents.Emit(events.DomainProviders)
		c.JSON(200, gin.H{"code": 0})
	})

	group.GET("/providers/:id/models", func(c *gin.Context) {
		if _, ok := requireAdmin(c.Request.Context(), deps, c); !ok {
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
		apiKey, okKey := decryptProviderKey(c, provider)
		if !okKey {
			return
		}
		headers := map[string]string{}
		if provider.Protocol == store.ProtocolAnthropicMessages {
			if apiKey != "" {
				headers["x-api-key"] = apiKey
			}
			version := provider.AnthropicVersion
			if version == "" {
				version = anthropicVersionFallback
			}
			headers["anthropic-version"] = version
		} else if apiKey != "" {
			headers["authorization"] = "Bearer " + apiKey
		}
		requestCtx, cancel := context.WithTimeout(ctx, upstreamModelListTimeout*time.Millisecond)
		defer cancel()
		req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, provider.BaseURL+"/v1/models", nil)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		resp, err := deps.HTTPClient.Do(req)
		if err != nil {
			deps.Logger.Warn("获取上游模型列表失败", "providerId", provider.ID, "error", err)
			c.JSON(504, fail(504, "上游不可达或响应超时"))
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			detailBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			detail := string(detailBytes)
			message := "上游返回 HTTP " + strconv.Itoa(resp.StatusCode)
			if detail != "" {
				message += ": " + detail
			}
			c.JSON(502, fail(502, message))
			return
		}
		bodyBytes, _ := io.ReadAll(resp.Body)
		var payload any
		_ = json.Unmarshal(bodyBytes, &payload)
		rows := []any{}
		switch typed := payload.(type) {
		case []any:
			rows = typed
		case map[string]any:
			if data, ok := typed["data"].([]any); ok {
				rows = data
			}
		}
		models := make([]string, 0, len(rows))
		for _, row := range rows {
			record, ok := row.(map[string]any)
			if !ok {
				continue
			}
			id, exists := record["id"]
			if !exists || id == nil {
				continue
			}
			if idStr, ok := id.(string); ok && idStr != "" {
				models = append(models, idStr)
			}
		}
		sort.Strings(models)
		c.JSON(200, okData(gin.H{"models": models}))
	})

	group.POST("/providers/:id/test", func(c *gin.Context) {
		admin, ok := requireAdmin(c.Request.Context(), deps, c)
		if !ok {
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
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		probeModel := ""
		if raw, exists := body["model"]; exists {
			parsed, okModel := trimmedRequired(raw, 1, 200)
			if !okModel {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			probeModel = parsed
		}
		apiKey, okKey := decryptProviderKey(c, provider)
		if !okKey {
			return
		}
		headers := map[string]string{"content-type": "application/json"}
		if provider.Protocol == store.ProtocolAnthropicMessages {
			if apiKey != "" {
				headers["x-api-key"] = apiKey
			}
			version := provider.AnthropicVersion
			if version == "" {
				version = anthropicVersionFallback
			}
			headers["anthropic-version"] = version
		} else if apiKey != "" {
			headers["authorization"] = "Bearer " + apiKey
		}
		url := provider.BaseURL + "/v1/models"
		var bodyBytes []byte
		method := http.MethodGet
		if probeModel != "" {
			path, payload := probeRequest(provider.Protocol, probeModel)
			url = provider.BaseURL + path
			bodyBytes = payload
			method = http.MethodPost
		}
		testTimeout := int64(providerTestTimeoutMs)
		if provider.TimeoutMs > 0 && provider.TimeoutMs < testTimeout {
			testTimeout = provider.TimeoutMs
		}
		startedAt := deps.Now()
		requestCtx, cancel := context.WithTimeout(ctx, time.Duration(testTimeout)*time.Millisecond)
		defer cancel()
		req, err := http.NewRequestWithContext(requestCtx, method, url, strings.NewReader(string(bodyBytes)))
		if err != nil {
			internalError(c, deps, err)
			return
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		persist := func(outcome store.ProviderTestOutcome) {
			if err := deps.Store.RecordProviderTestResult(ctx, provider.ID, outcome); err != nil {
				deps.Logger.Warn("供应商测试结果落库失败", "providerId", provider.ID, "error", err)
			}
			writeAudit(ctx, deps, admin, "provider.test", "provider", provider.ID, map[string]any{
				"ok": outcome.OK, "kind": outcome.Kind,
			})
		}
		resp, err := deps.HTTPClient.Do(req)
		latencyMs := deps.Now().Sub(startedAt).Milliseconds()
		if err != nil {
			kind := "network"
			if requestCtx.Err() == context.DeadlineExceeded {
				kind = "timeout"
			}
			outcome := store.ProviderTestOutcome{OK: false, Kind: kind, LatencyMs: latencyMs, Model: probeModel}
			deps.Logger.Warn("供应商测试失败", "providerId", provider.ID, "withModel", probeModel != "", "kind", kind)
			persist(outcome)
			c.JSON(200, okData(gin.H{
				"ok": false, "kind": kind, "status": nil, "latencyMs": latencyMs, "detail": nil,
			}))
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			detailBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			detail := string(detailBytes)
			kind := "upstream_error"
			switch {
			case resp.StatusCode == 401 || resp.StatusCode == 403:
				kind = "auth"
			case resp.StatusCode == 404:
				kind = "not_found"
			case resp.StatusCode == 429:
				kind = "rate_limited"
			}
			status := int64(resp.StatusCode)
			outcome := store.ProviderTestOutcome{
				OK: false, Kind: kind, Status: &status, LatencyMs: latencyMs,
				Model: probeModel, Detail: detail,
			}
			persist(outcome)
			c.JSON(200, okData(gin.H{
				"ok": false, "kind": kind, "status": resp.StatusCode, "latencyMs": latencyMs, "detail": detail,
			}))
			return
		}
		status := int64(resp.StatusCode)
		outcome := store.ProviderTestOutcome{
			OK: true, Kind: "ok", Status: &status, LatencyMs: latencyMs, Model: probeModel,
		}
		persist(outcome)
		c.JSON(200, okData(gin.H{
			"ok": true, "kind": "ok", "status": resp.StatusCode, "latencyMs": latencyMs, "detail": nil,
		}))
	})

	// ---- model pricing ----

	group.GET("/model-pricing", func(c *gin.Context) {
		if _, ok := requireAdmin(c.Request.Context(), deps, c); !ok {
			return
		}
		doc, err := loadPricingDoc(c.Request.Context(), deps.Store)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.JSON(200, okData(doc))
	})

	group.PUT("/model-pricing", func(c *gin.Context) {
		admin, ok := requireAdmin(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法（条目需含模型 ID、显示名与非负价格，并携带 expectedVersion）"))
			return
		}
		expectedVersion, okVersion := trimmedRequired(body["expectedVersion"], 1, 64)
		rawEntries, okEntries := body["entries"].([]any)
		if !okVersion || !okEntries || len(rawEntries) < 1 || len(rawEntries) > 2000 {
			c.JSON(400, fail(400, "请求参数不合法（条目需含模型 ID、显示名与非负价格，并携带 expectedVersion）"))
			return
		}
		byID := map[string]pricing.PricingEntry{}
		order := []string{}
		for _, raw := range rawEntries {
			record, okRecord := raw.(map[string]any)
			if !okRecord {
				c.JSON(400, fail(400, "请求参数不合法（条目需含模型 ID、显示名与非负价格，并携带 expectedVersion）"))
				return
			}
			modelID, okModel := trimmedRequired(record["modelId"], 1, 120)
			displayName, okName := trimmedRequired(record["displayName"], 1, 120)
			input, okInput := parsePriceString(record["inputCostPerMillion"])
			output, okOutput := parsePriceString(record["outputCostPerMillion"])
			if !okModel || !okName || !okInput || !okOutput {
				c.JSON(400, fail(400, "请求参数不合法（条目需含模型 ID、显示名与非负价格，并携带 expectedVersion）"))
				return
			}
			cacheRead := "0.000000"
			if rawPrice, exists := record["cacheReadCostPerMillion"]; exists {
				parsed, okPrice := parsePriceString(rawPrice)
				if !okPrice {
					c.JSON(400, fail(400, "请求参数不合法（条目需含模型 ID、显示名与非负价格，并携带 expectedVersion）"))
					return
				}
				cacheRead = parsed
			}
			cacheCreation := "0.000000"
			if rawPrice, exists := record["cacheCreationCostPerMillion"]; exists {
				parsed, okPrice := parsePriceString(rawPrice)
				if !okPrice {
					c.JSON(400, fail(400, "请求参数不合法（条目需含模型 ID、显示名与非负价格，并携带 expectedVersion）"))
					return
				}
				cacheCreation = parsed
			}
			if _, exists := byID[modelID]; !exists {
				order = append(order, modelID)
			}
			byID[modelID] = pricing.PricingEntry{
				ModelID: modelID, DisplayName: displayName,
				InputCostPerMillion: input, OutputCostPerMillion: output,
				CacheReadCostPerMillion: cacheRead, CacheCreationCostPerMillion: cacheCreation,
			}
		}
		entries := make([]pricing.PricingEntry, 0, len(order))
		for _, modelID := range order {
			entries = append(entries, byID[modelID])
		}
		saved, err := deps.Store.SaveModelPricing(ctx, entries, expectedVersion)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if saved == nil {
			c.JSON(409, fail(409, "定价库已被他人修改，请刷新页面后重试"))
			return
		}
		writeAudit(ctx, deps, admin, "pricing.update", "", "", map[string]any{"entries": len(entries)})
		c.JSON(200, okData(saved))
	})

	// ---- config ----

	group.GET("/config", func(c *gin.Context) {
		if _, ok := requireAdmin(c.Request.Context(), deps, c); !ok {
			return
		}
		ctx := c.Request.Context()
		helpRaw, err := deps.Store.GetConfigValue(ctx, contracts.PlatformConfigKeyHelp)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		forceRaw, err := deps.Store.GetConfigValue(ctx, contracts.PlatformConfigKeyForceUpdate)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		help, _ := jsonDecode(helpRaw)
		force, _ := jsonDecode(forceRaw)
		c.JSON(200, okData(gin.H{"help": help, "forceUpdate": force}))
	})

	group.PUT("/config", func(c *gin.Context) {
		if _, ok := requireAdmin(c.Request.Context(), deps, c); !ok {
			return
		}
		ctx := c.Request.Context()
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		for _, field := range []string{"help", "forceUpdate"} {
			if raw, exists := body[field]; exists {
				encoded, err := jsonMarshalNoHTML(raw)
				if err != nil {
					internalError(c, deps, err)
					return
				}
				key := contracts.PlatformConfigKeyHelp
				if field == "forceUpdate" {
					key = contracts.PlatformConfigKeyForceUpdate
				}
				if err := deps.Store.SetConfigValue(ctx, key, encoded); err != nil {
					internalError(c, deps, err)
					return
				}
			}
		}
		deps.ConfigEvents.Emit(events.DomainPlatformConfig)
		c.JSON(200, gin.H{"code": 0})
	})

	// ---- usage ----

	parseUsageQueryFilter := func(c *gin.Context) (store.UsageFilter, bool, bool) {
		filter := store.UsageFilter{}
		if value := c.Query("teamId"); value != "" {
			filter.TeamID = value
		}
		if value := c.Query("userId"); value != "" {
			filter.UserID = value
		}
		if value := c.Query("model"); value != "" {
			filter.Model = value
		}
		if value := c.Query("from"); value != "" {
			if parsed, ok := parseQueryTime(value); ok {
				filter.From = &parsed
			}
		}
		if value := c.Query("to"); value != "" {
			if parsed, ok := parseQueryTime(value); ok {
				filter.To = &parsed
			}
		}
		statusRaw := c.Query("status")
		if statusRaw != "" {
			if statusRaw != "ok" && statusRaw != "error" && statusRaw != "aborted" {
				c.JSON(400, fail(400, "status 只支持 ok / error / aborted"))
				return filter, false, false
			}
			filter.Status = store.UsageStatus(statusRaw)
		}
		providerRaw := c.Query("providerId")
		if providerRaw != "" {
			if !uuidShape(providerRaw) {
				c.JSON(400, fail(400, "providerId 不合法"))
				return filter, false, false
			}
			filter.ProviderID = providerRaw
		}
		return filter, true, true
	}

	group.GET("/usage", func(c *gin.Context) {
		if _, ok := requireAdmin(c.Request.Context(), deps, c); !ok {
			return
		}
		ctx := c.Request.Context()
		limit := int64(100)
		if rawLimit, okParse := parseLeadingInt(c.Query("limit")); okParse && rawLimit > 0 {
			if rawLimit > 500 {
				rawLimit = 500
			}
			limit = rawLimit
		}
		cursorRaw := c.Query("cursor")
		cursorCreatedAt, cursorID, cursorOK := decodeUsageCursor(cursorRaw)
		if cursorRaw != "" && !cursorOK {
			c.JSON(400, fail(400, "分页游标不合法"))
			return
		}
		baseFilter, okFilter, _ := parseUsageQueryFilter(c)
		if !okFilter {
			return
		}
		pageFilter := baseFilter
		pageLimit := limit + 1
		pageFilter.Limit = &pageLimit
		if cursorOK {
			parsed, okTime := parseQueryTime(cursorCreatedAt)
			if !okTime {
				c.JSON(400, fail(400, "分页游标不合法"))
				return
			}
			pageFilter.CursorCreatedAt = &parsed
			pageFilter.CursorID = cursorID
		}
		aggregates, err := deps.Store.AggregateUsage(ctx, baseFilter)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		allRecords, err := deps.Store.ListUsage(ctx, pageFilter)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		hasMore := int64(len(allRecords)) > limit
		records := allRecords
		if hasMore {
			records = allRecords[:limit]
		}
		var nextCursor *string
		if hasMore && len(records) > 0 {
			last := records[len(records)-1]
			cursor := encodeUsageCursor(isoString(last.CreatedAt), last.ID)
			nextCursor = &cursor
		}
		aggregateRows := make([]aggregatePublic, 0, len(aggregates))
		for _, row := range aggregates {
			aggregateRows = append(aggregateRows, toPublicAggregate(row))
		}
		recordRows := make([]usagePublic, 0, len(records))
		for i := range records {
			recordRows = append(recordRows, toPublicUsage(&records[i]))
		}
		c.JSON(200, okData(gin.H{
			"aggregates": aggregateRows, "records": recordRows, "nextCursor": nextCursor,
		}))
	})

	group.GET("/usage/series", func(c *gin.Context) {
		if _, ok := requireAdmin(c.Request.Context(), deps, c); !ok {
			return
		}
		ctx := c.Request.Context()
		granularity := store.SeriesGranularityDay
		if c.Query("granularity") == "hour" {
			granularity = store.SeriesGranularityHour
		}
		fromQuery, toQuery := c.Query("from"), c.Query("to")
		if granularity == store.SeriesGranularityHour && fromQuery != "" && toQuery != "" {
			// 仅用于范围长度估算:naive 串统一按 UTC 解析(与 TS 的 +Z 口径一致)。
			fromTime, fromOK := parseQueryTime(strings.ReplaceAll(fromQuery, " ", "T") + "Z")
			toTime, toOK := parseQueryTime(strings.ReplaceAll(toQuery, " ", "T") + "Z")
			if fromOK && toOK {
				span := toTime.Sub(fromTime)
				if span > 31*24*time.Hour {
					c.JSON(400, fail(400, "小时粒度最多查询 31 天范围"))
					return
				}
			}
		}
		filter, okFilter, _ := parseUsageQueryFilter(c)
		if !okFilter {
			return
		}
		series, err := deps.Store.AggregateUsageSeries(ctx, filter, granularity)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		rows := make([]seriesPublic, 0, len(series))
		for _, row := range series {
			rows = append(rows, toPublicSeries(row))
		}
		c.JSON(200, okData(gin.H{"series": rows}))
	})

	// ---- devices ----

	group.GET("/devices", func(c *gin.Context) {
		if _, ok := requireAdmin(c.Request.Context(), deps, c); !ok {
			return
		}
		ctx := c.Request.Context()
		tokens, err := deps.Store.ListDeviceTokens(ctx, "")
		if err != nil {
			internalError(c, deps, err)
			return
		}
		users, err := deps.Store.ListUsers(ctx)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		emailByID := make(map[string]string, len(users))
		for _, user := range users {
			emailByID[user.ID] = user.Email
		}
		rows := make([]devicePublic, 0, len(tokens))
		for i := range tokens {
			token := tokens[i]
			email := emailByID[token.UserID]
			if email == "" {
				email = "(已删除)"
			}
			rows = append(rows, devicePublic{
				ID: token.ID, Name: token.Name, Platform: stringPtrOrNil(token.Platform),
				Hint: token.TokenHint, UserEmail: email, CreatedAt: isoString(token.CreatedAt),
				LastUsedAt: isoStringPtr(token.LastUsedAt), RevokedAt: isoStringPtr(token.RevokedAt),
			})
		}
		c.JSON(200, okData(gin.H{"devices": rows}))
	})

	group.POST("/devices/:id/revoke", func(c *gin.Context) {
		admin, ok := requireAdmin(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		device, err := deps.Store.FindDeviceTokenByID(ctx, c.Param("id"))
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if device == nil {
			c.JSON(404, fail(404, "设备不存在"))
			return
		}
		if err := deps.Store.RevokeDeviceToken(ctx, device.ID, deps.Now()); err != nil {
			internalError(c, deps, err)
			return
		}
		writeAudit(ctx, deps, admin, "device.revoke", "device", device.ID, nil)
		deps.ConfigEvents.Emit(events.DomainDevices)
		c.JSON(200, gin.H{"code": 0})
	})

	// ---- audit ----

	group.GET("/audit", func(c *gin.Context) {
		if _, ok := requireAdmin(c.Request.Context(), deps, c); !ok {
			return
		}
		ctx := c.Request.Context()
		limit := int64(50)
		if rawLimit, okParse := parseLeadingInt(c.Query("limit")); okParse && rawLimit > 0 {
			if rawLimit > 500 {
				rawLimit = 500
			}
			limit = rawLimit
		}
		cursorRaw := c.Query("cursor")
		cursorCreatedAt, cursorID, cursorOK := decodeUsageCursor(cursorRaw)
		if cursorRaw != "" && !cursorOK {
			c.JSON(400, fail(400, "分页游标不合法"))
			return
		}
		pageLimit := limit + 1
		filter := store.AuditLogFilter{Limit: &pageLimit}
		if actor := c.Query("actorUserId"); actor != "" {
			filter.ActorUserID = actor
		}
		if cursorOK {
			parsed, okTime := parseQueryTime(cursorCreatedAt)
			if !okTime {
				c.JSON(400, fail(400, "分页游标不合法"))
				return
			}
			filter.CursorCreatedAt = &parsed
			filter.CursorID = cursorID
		}
		logs, err := deps.Store.ListAuditLogs(ctx, filter)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		hasMore := int64(len(logs)) > limit
		page := logs
		if hasMore {
			page = logs[:limit]
		}
		var nextCursor *string
		if hasMore && len(page) > 0 {
			last := page[len(page)-1]
			cursor := encodeUsageCursor(isoString(last.CreatedAt), last.ID)
			nextCursor = &cursor
		}
		rows := make([]auditPublic, 0, len(page))
		for _, row := range page {
			rows = append(rows, toPublicAudit(row))
		}
		c.JSON(200, okData(gin.H{"logs": rows, "nextCursor": nextCursor}))
	})
}

func mustJSONBytes(v any) []byte {
	raw, err := jsonMarshalNoHTML(v)
	if err != nil {
		return []byte(`{}`)
	}
	return raw
}

func hintOf(apiKey string) string {
	if len(apiKey) < 4 {
		return apiKey
	}
	return apiKey[len(apiKey)-4:]
}

// parseProtocol:protocol 白名单(三值)。
func parseProtocol(raw any) (store.UpstreamProtocol, bool) {
	value, ok := raw.(string)
	if !ok {
		return "", false
	}
	switch store.UpstreamProtocol(value) {
	case store.ProtocolOpenAIChatCompletions, store.ProtocolOpenAIResponses, store.ProtocolAnthropicMessages:
		return store.UpstreamProtocol(value), true
	default:
		return "", false
	}
}

// parseNullableTrimmedString:nullable optional 字符串(trim/max)。
// 返回 (值, 是否有非空值, 字段是否存在, 是否合法)。
func parseNullableTrimmedString(raw any, max int) (string, bool, bool, bool) {
	if raw == nil {
		return "", false, true, true
	}
	value, ok := raw.(string)
	if !ok {
		return "", false, true, false
	}
	trimmed := strings.TrimSpace(value)
	if len(trimmed) > max {
		return "", false, true, false
	}
	return trimmed, trimmed != "", true, true
}

// parseNullableInt:nullable optional 整数(min..max 仅在非空时校验)。
func parseNullableInt(raw any, min, max int64) (int64, bool, bool) {
	if raw == nil {
		return 0, false, true
	}
	numeric, ok := raw.(float64)
	if !ok || numeric != float64(int64(numeric)) {
		return 0, false, false
	}
	parsed := int64(numeric)
	if parsed < min || parsed > max {
		return 0, false, false
	}
	return parsed, true, true
}
