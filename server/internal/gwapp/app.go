// Package gwapp:模型网关(specs/go-rewrite.md §2.6)。
// 管道与 apps/gateway/src/app.ts 逐行为对齐:
// 鉴权 → 模型校验 → 上游解析 → 额度预留(原子) → 透传转发 → usage 结算。
// 三协议差异只体现在请求体字段读取、上游鉴权头、usage 解析与流式采集器。
package gwapp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/contracts"
	"github.com/vam12375/shucode-platform/server/internal/credits"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

const (
	maxMaxTokens          = 256_000
	defaultMaxTokens      = 4_096
	maxUpstreamErrorBytes = 1_000_000
)

type Deps struct {
	Store      store.PlatformStore
	Env        *Env
	Now        func() time.Time
	HTTPClient *http.Client
	Logger     *slog.Logger
	SecretBox  *crypto.SecretBox // PLATFORM_SECRET_KEY 缺省时为 nil(绑定供应商密文不可解析)
	Trusted    trustedProxies
}

type errorResponder func(c *gin.Context, status int, typ, message string)

func anthropicError(c *gin.Context, status int, typ, message string) {
	c.Data(status, "application/json; charset=utf-8", mustJSON(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": typ, "message": message},
	}))
}

func openAIError(c *gin.Context, status int, typ, message string) {
	c.Data(status, "application/json; charset=utf-8", mustJSON(map[string]any{
		"error": map[string]any{"message": message, "type": typ, "code": nil},
	}))
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte(`{}`)
	}
	return raw
}

type protocolSpec struct {
	routePath      string
	upstreamPath   string
	family         string // "openai" | "anthropic"
	errorResponse  errorResponder
	readModelID    func(body map[string]any) string
	readMaxTokens  func(body map[string]any) int64
	isStream       func(body map[string]any) bool
	buildUpstream  func(r *http.Request, body map[string]any, upstreamModel string, stream bool, apiKey, anthropicVersion string) (map[string]string, []byte)
	parseUsageJSON func(text string) *UsageCounts
	newScanner     func() streamScanner
}

type streamScanner interface {
	Push(chunk string)
	Usage() UsageCounts
	Dropped() int64
}

func buildAnthropicSpec() protocolSpec {
	return protocolSpec{
		routePath:     "/v1/messages",
		upstreamPath:  "/v1/messages",
		family:        "anthropic",
		errorResponse: anthropicError,
		readModelID: func(body map[string]any) string {
			if model, ok := body["model"].(string); ok {
				return strings.TrimSpace(model)
			}
			return ""
		},
		readMaxTokens: func(body map[string]any) int64 { return readMaxTokensFrom(body["max_tokens"]) },
		isStream:      func(body map[string]any) bool { return body["stream"] == true },
		buildUpstream: func(r *http.Request, body map[string]any, upstreamModel string, _ bool, apiKey, anthropicVersion string) (map[string]string, []byte) {
			headers := map[string]string{
				"content-type":      "application/json",
				"anthropic-version": anthropicVersion,
			}
			if clientVersion := strings.TrimSpace(r.Header.Get("anthropic-version")); clientVersion != "" {
				headers["anthropic-version"] = clientVersion
			}
			if beta := strings.TrimSpace(r.Header.Get("anthropic-beta")); beta != "" {
				headers["anthropic-beta"] = beta
			}
			// 上游鉴权由平台注入,客户端的平台凭据不外发。
			if apiKey != "" {
				headers["x-api-key"] = apiKey
			}
			body["model"] = upstreamModel
			return headers, mustJSON(body)
		},
		parseUsageJSON: func(text string) *UsageCounts {
			var body any
			if err := json.Unmarshal([]byte(text), &body); err != nil {
				return nil
			}
			return ParseUsageFromAnthropicJSON(body)
		},
		newScanner: func() streamScanner { return NewAnthropicScanner() },
	}
}

func buildOpenAISpec() protocolSpec {
	return protocolSpec{
		routePath:     "/v1/chat/completions",
		upstreamPath:  "/v1/chat/completions",
		family:        "openai",
		errorResponse: openAIError,
		readModelID: func(body map[string]any) string {
			if model, ok := body["model"].(string); ok {
				return strings.TrimSpace(model)
			}
			return ""
		},
		readMaxTokens: func(body map[string]any) int64 {
			if _, ok := body["max_completion_tokens"]; ok {
				return readMaxTokensFrom(body["max_completion_tokens"])
			}
			return readMaxTokensFrom(body["max_tokens"])
		},
		isStream: func(body map[string]any) bool { return body["stream"] == true },
		buildUpstream: func(_ *http.Request, body map[string]any, upstreamModel string, stream bool, apiKey, _ string) (map[string]string, []byte) {
			headers := map[string]string{"content-type": "application/json"}
			if apiKey != "" {
				headers["authorization"] = "Bearer " + apiKey
			}
			body["model"] = upstreamModel
			// 流式计量依赖最终 usage chunk:客户端未声明时注入 include_usage。
			if stream {
				if _, exists := body["stream_options"]; !exists {
					body["stream_options"] = map[string]any{"include_usage": true}
				}
			}
			return headers, mustJSON(body)
		},
		parseUsageJSON: func(text string) *UsageCounts {
			var body any
			if err := json.Unmarshal([]byte(text), &body); err != nil {
				return nil
			}
			return ParseUsageFromOpenAIJSON(body)
		},
		newScanner: func() streamScanner { return NewOpenAIScanner() },
	}
}

func buildOpenAIResponsesSpec() protocolSpec {
	return protocolSpec{
		routePath:     "/v1/responses",
		upstreamPath:  "/v1/responses",
		family:        "openai",
		errorResponse: openAIError,
		readModelID: func(body map[string]any) string {
			if model, ok := body["model"].(string); ok {
				return strings.TrimSpace(model)
			}
			return ""
		},
		readMaxTokens: func(body map[string]any) int64 {
			if _, ok := body["max_output_tokens"]; ok {
				return readMaxTokensFrom(body["max_output_tokens"])
			}
			return readMaxTokensFrom(body["max_tokens"])
		},
		isStream: func(body map[string]any) bool { return body["stream"] == true },
		buildUpstream: func(_ *http.Request, body map[string]any, upstreamModel string, _ bool, apiKey, _ string) (map[string]string, []byte) {
			headers := map[string]string{"content-type": "application/json"}
			if apiKey != "" {
				headers["authorization"] = "Bearer " + apiKey
			}
			body["model"] = upstreamModel
			return headers, mustJSON(body)
		},
		parseUsageJSON: func(text string) *UsageCounts {
			var body any
			if err := json.Unmarshal([]byte(text), &body); err != nil {
				return nil
			}
			return ParseUsageFromResponsesJSON(body)
		},
		newScanner: func() streamScanner { return NewResponsesScanner() },
	}
}

// New 组装 gin 路由:错误形状跟随协议(onError 等价物用 recovery 返回 anthropic 500)。
func New(deps Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	app := gin.New()
	app.Use(gin.CustomRecovery(func(c *gin.Context, err any) {
		deps.Logger.Error("网关请求处理失败", "path", c.Request.URL.Path, "error", err)
		anthropicError(c, 500, "api_error", "网关内部错误")
	}))
	app.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	app.GET("/v1/models", func(c *gin.Context) { handleModelsRequest(c, deps) })
	for _, spec := range []protocolSpec{buildAnthropicSpec(), buildOpenAISpec(), buildOpenAIResponsesSpec()} {
		spec := spec
		app.POST(spec.routePath, func(c *gin.Context) {
			handleProtocolRequest(c, deps, spec)
		})
	}
	return app
}

func readGatewayToken(r *http.Request) string {
	authorization := strings.TrimSpace(r.Header.Get("authorization"))
	if len(authorization) >= 7 && strings.EqualFold(authorization[:7], "bearer ") {
		if token := strings.TrimSpace(authorization[7:]); token != "" {
			return token
		}
	}
	return strings.TrimSpace(r.Header.Get("x-api-key"))
}

func authenticate(r *http.Request, deps Deps, errorResponse errorResponder, c *gin.Context) (*store.DeviceTokenRecord, *store.UserRecord, bool) {
	token := readGatewayToken(r)
	if token == "" {
		errorResponse(c, 401, "authentication_error", "缺少平台凭据（x-api-key 或 Bearer）")
		return nil, nil, false
	}
	device, err := deps.Store.FindDeviceTokenByHash(r.Context(), crypto.HashToken(token))
	if err != nil {
		deps.Logger.Error("设备凭据查询失败", "error", err)
		errorResponse(c, 500, "api_error", "网关内部错误")
		return nil, nil, false
	}
	if device == nil || device.RevokedAt != nil {
		errorResponse(c, 401, "authentication_error", "平台凭据无效或已吊销")
		return nil, nil, false
	}
	user, err := deps.Store.FindUserByID(r.Context(), device.UserID)
	if err != nil {
		deps.Logger.Error("用户查询失败", "error", err)
		errorResponse(c, 500, "api_error", "网关内部错误")
		return nil, nil, false
	}
	if user == nil || user.DisabledAt != nil {
		errorResponse(c, 403, "permission_error", "账号不可用")
		return nil, nil, false
	}
	return device, user, true
}

// handleModelsRequest:OpenAI list 兼容的已启用模型清单;仅校验设备凭据有效。
func handleModelsRequest(c *gin.Context, deps Deps) {
	_, _, ok := authenticate(c.Request, deps, openAIError, c)
	if !ok {
		return
	}
	models, err := deps.Store.ListModels(c.Request.Context(), false)
	if err != nil {
		deps.Logger.Error("模型目录查询失败", "error", err)
		openAIError(c, 500, "api_error", "网关内部错误")
		return
	}
	data := make([]gin.H, 0, len(models))
	for _, model := range models {
		data = append(data, gin.H{
			"id":       model.ModelID,
			"object":   "model",
			"created":  model.CreatedAt.Unix(),
			"owned_by": "mendao-platform",
		})
	}
	c.JSON(200, gin.H{"object": "list", "data": data})
}

func priceRates(model *store.PlatformModelRecord) credits.ModelPriceRates {
	parse := func(value string) float64 {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || parsed < 0 {
			return 0
		}
		return parsed
	}
	return credits.ModelPriceRates{
		InputPerMtok:      parse(model.InputPricePerMtok),
		OutputPerMtok:     parse(model.OutputPricePerMtok),
		CacheReadPerMtok:  parse(model.CacheReadPricePerMtok),
		CacheWritePerMtok: parse(model.CacheWritePricePerMtok),
	}
}

func readMaxTokensFrom(value any) int64 {
	if raw, ok := value.(float64); ok {
		if math.IsNaN(raw) || math.IsInf(raw, 0) || raw <= 0 {
			return defaultMaxTokens
		}
		clamped := int64(raw)
		if clamped > maxMaxTokens {
			return maxMaxTokens
		}
		if clamped < 1 {
			return 1
		}
		return clamped
	}
	return defaultMaxTokens
}

func clientIPOf(c *gin.Context, deps Deps) *string {
	remote := c.Request.RemoteAddr
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	values, present := c.Request.Header["X-Forwarded-For"]
	return ResolveClientIP(remote, values, present, c.Request.Header.Get("x-real-ip"), deps.Trusted)
}

func handleProtocolRequest(c *gin.Context, deps Deps, spec protocolSpec) {
	ctx := c.Request.Context()
	// ---- 鉴权 ----
	device, user, ok := authenticate(c.Request, deps, spec.errorResponse, c)
	if !ok {
		return
	}
	teams, err := deps.Store.ListTeamsForUser(ctx, user.ID)
	if err != nil {
		deps.Logger.Error("团队查询失败", "error", err)
		spec.errorResponse(c, 500, "api_error", "网关内部错误")
		return
	}
	var primary *store.TeamRecord
	for i := range teams {
		if teams[i].Team.DisabledAt == nil {
			primary = &teams[i].Team
			break
		}
	}
	if primary == nil {
		spec.errorResponse(c, 403, "permission_error", "账号未加入任何团队，请联系团队管理员")
		return
	}
	if err := deps.Store.TouchDeviceToken(ctx, device.ID, deps.Now()); err != nil {
		deps.Logger.Warn("设备活跃时间更新失败", "error", err)
	}
	clientIP := clientIPOf(c, deps)

	// ---- 请求体校验 ----
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		spec.errorResponse(c, 400, "invalid_request_error", "请求体不是合法 JSON")
		return
	}
	var parsed any
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		spec.errorResponse(c, 400, "invalid_request_error", "请求体不是合法 JSON")
		return
	}
	body, ok := parsed.(map[string]any)
	if !ok {
		spec.errorResponse(c, 400, "invalid_request_error", "请求体必须是 JSON 对象")
		return
	}
	modelID := spec.readModelID(body)
	if modelID == "" {
		spec.errorResponse(c, 400, "invalid_request_error", "缺少 model 字段")
		return
	}
	model, err := deps.Store.GetModel(ctx, modelID)
	if err != nil {
		deps.Logger.Error("模型查询失败", "error", err)
		spec.errorResponse(c, 500, "api_error", "网关内部错误")
		return
	}
	if model == nil || !model.Enabled {
		spec.errorResponse(c, 404, "not_found_error", "模型未在平台启用："+modelID)
		return
	}
	stream := spec.isStream(body)
	maxTokens := spec.readMaxTokens(body)
	rates := priceRates(model)
	upstreamModel := strings.TrimSpace(model.UpstreamModel)
	if upstreamModel == "" {
		upstreamModel = model.ModelID
	}

	// ---- 解析上游目标:模型绑定的供应商优先;未绑定回落 env 单上游 ----
	upstreamBaseURL := deps.Env.UpstreamBaseURL
	upstreamAPIKey := deps.Env.UpstreamAPIKey
	upstreamAnthropicVersion := deps.Env.AnthropicVersion
	upstreamConnectTimeoutMs := deps.Env.UpstreamTimeoutMs
	upstreamIdleTimeoutMs := deps.Env.UpstreamIdleTimeoutMs
	upstreamProviderID := ""
	if model.UpstreamProviderID != "" {
		provider, err := deps.Store.GetUpstreamProvider(ctx, model.UpstreamProviderID)
		if err != nil {
			deps.Logger.Error("供应商查询失败", "error", err)
			spec.errorResponse(c, 500, "api_error", "网关内部错误")
			return
		}
		if provider == nil || !provider.Enabled {
			reason := "已手动停用"
			if provider != nil && !provider.Enabled && provider.DisabledMode == "auto" {
				detail := provider.AutoDisabledReason
				if detail == "" {
					detail = "上游异常"
				}
				reason = "自动熔断（连续失败：" + detail + "）"
			}
			spec.errorResponse(c, 404, "not_found_error", "模型绑定的上游供应商不可用（"+reason+"）："+model.ModelID)
			return
		}
		providerFamily := "openai"
		if provider.Protocol == store.ProtocolAnthropicMessages {
			providerFamily = "anthropic"
		}
		if providerFamily != spec.family {
			spec.errorResponse(c, 404, "not_found_error",
				"模型 "+model.ModelID+" 的上游协议（"+string(provider.Protocol)+"）与请求协议（"+spec.family+"）不一致")
			return
		}
		if provider.Protocol == store.ProtocolOpenAIResponses && spec.upstreamPath == "/v1/chat/completions" {
			spec.errorResponse(c, 404, "not_found_error",
				"模型 "+model.ModelID+" 绑定的供应商按 Responses 协议接入，不支持 chat completions 请求")
			return
		}
		if provider.APIKeyCiphertext != "" {
			if deps.SecretBox == nil {
				spec.errorResponse(c, 500, "api_error", "平台密钥未配置，无法解析上游凭据")
				return
			}
			decrypted, err := deps.SecretBox.Decrypt(provider.APIKeyCiphertext)
			if err != nil {
				spec.errorResponse(c, 500, "api_error", "上游凭据解密失败（PLATFORM_SECRET_KEY 是否更换过？）")
				return
			}
			upstreamAPIKey = decrypted
		} else {
			// 供应商未配密钥:不带鉴权头(如本地 ollama / 内网网关)。
			upstreamAPIKey = ""
		}
		upstreamBaseURL = provider.BaseURL
		if provider.AnthropicVersion != "" {
			upstreamAnthropicVersion = provider.AnthropicVersion
		}
		if provider.TimeoutMs > 0 {
			upstreamConnectTimeoutMs = provider.TimeoutMs
			upstreamIdleTimeoutMs = provider.TimeoutMs
		}
		upstreamProviderID = provider.ID
	}

	// ---- 额度准入:原子预留,超限 429 ----
	estimateMicro := credits.EstimateMaxMicroCredits(maxTokens, rates.OutputPerMtok)
	reserved, err := deps.Store.TryReserveQuota(ctx, primary.ID, estimateMicro)
	if err != nil {
		deps.Logger.Error("额度预留失败", "error", err)
		spec.errorResponse(c, 500, "api_error", "网关内部错误")
		return
	}
	if !reserved {
		spec.errorResponse(c, 429, "rate_limit_error", "团队额度已用尽，请联系团队管理员充值 / Team quota exhausted")
		return
	}

	requestID := strings.TrimSpace(c.Request.Header.Get("x-request-id"))
	if requestID == "" {
		requestID, _ = contracts.RandomUUIDv4()
	}
	startedAt := deps.Now()
	var settled atomic.Bool

	buildUsageRecord := func(usage UsageCounts, status store.UsageStatus, errorType string) store.UsageRecord {
		micro, err := credits.ComputeUsageMicroCredits(credits.UsageTokenCounts{
			InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
			CacheReadTokens: usage.CacheReadTokens, CacheCreationTokens: usage.CacheCreationTokens,
		}, rates)
		if err != nil {
			micro = 0
		}
		record := store.UsageRecord{
			RequestID:       requestID,
			UserID:          user.ID,
			TeamID:          primary.ID,
			DeviceTokenID:   device.ID,
			Model:           model.ModelID,
			UpstreamModel:   upstreamModel,
			InputTokens:     usage.InputTokens,
			OutputTokens:    usage.OutputTokens,
			CacheReadTokens: usage.CacheReadTokens, CacheCreationTokens: usage.CacheCreationTokens,
			CreditsMicro: micro,
			Status:       status,
			ProviderID:   upstreamProviderID,
			CreatedAt:    deps.Now(),
		}
		record.ID, _ = contracts.RandomUUIDv4()
		record.ErrorType = errorType
		if clientIP != nil {
			record.ClientIP = *clientIP
		}
		latency := deps.Now().Sub(startedAt).Milliseconds()
		record.LatencyMs = &latency
		return record
	}

	failWithUsage := func(status store.UsageStatus, errorType string) {
		if settled.Swap(true) {
			return
		}
		if err := deps.Store.InsertUsage(ctx, buildUsageRecord(UsageCounts{}, status, errorType)); err != nil {
			deps.Logger.Error("失败记录写入失败", "requestId", requestID, "error", err)
		}
		if err := deps.Store.ReleaseReservation(ctx, primary.ID, estimateMicro); err != nil {
			deps.Logger.Error("额度释放失败", "requestId", requestID, "error", err)
		}
	}

	reportFailure := func(reason string) {
		if upstreamProviderID == "" || deps.Env.ProviderAutoDisableThreshold <= 0 {
			return
		}
		disabled, err := deps.Store.ReportUpstreamFailure(ctx, upstreamProviderID, reason, deps.Env.ProviderAutoDisableThreshold)
		if err != nil {
			deps.Logger.Error("熔断计数失败", "error", err)
			return
		}
		if disabled {
			deps.Logger.Warn("上游供应商连续失败，已自动停用", "providerId", upstreamProviderID, "reason", reason)
		}
	}

	// ---- 转发上游 ----
	upstreamHeaders, upstreamBody := spec.buildUpstream(c.Request, body, upstreamModel, stream, upstreamAPIKey, upstreamAnthropicVersion)
	upstreamURL := upstreamBaseURL + spec.upstreamPath
	// 上游请求必须绑定带取消的 ctx:header 超时、空闲看门狗、客户端断连都靠它中断。
	upstreamCtx, cancelUpstream := context.WithCancel(ctx)
	defer cancelUpstream()
	req, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, upstreamURL, bytes.NewReader(upstreamBody))
	if err != nil {
		deps.Logger.Error("构造上游请求失败", "error", err)
		failWithUsage(store.UsageStatusError, "upstream_unavailable")
		spec.errorResponse(c, 500, "api_error", "网关内部错误")
		return
	}
	for key, value := range upstreamHeaders {
		req.Header.Set(key, value)
	}

	var headerTimedOut atomic.Bool
	headerTimer := time.AfterFunc(time.Duration(upstreamConnectTimeoutMs)*time.Millisecond, func() {
		headerTimedOut.Store(true)
		cancelUpstream()
	})

	resp, err := deps.HTTPClient.Do(req)
	if err != nil {
		headerTimer.Stop()
		deps.Logger.Warn("上游请求失败", "requestId", requestID, "timedOut", headerTimedOut.Load(), "error", err)
		errorType := "upstream_unavailable"
		if headerTimedOut.Load() {
			errorType = "upstream_timeout"
		}
		failWithUsage(store.UsageStatusError, errorType)
		reportFailure(errorType)
		if headerTimedOut.Load() {
			spec.errorResponse(c, 504, "api_error", "上游响应超时，请稍后重试")
			return
		}
		spec.errorResponse(c, 502, "api_error", "上游供应商暂时不可用，请稍后重试")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		headerTimer.Stop()
		text, _ := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamErrorBytes))
		deps.Logger.Warn("上游返回错误", "requestId", requestID, "status", resp.StatusCode)
		failWithUsage(store.UsageStatusError, "upstream_"+strconv.Itoa(resp.StatusCode))
		// 只有供应商自身故障(5xx 与鉴权失效 401/403)计入熔断;404/429 等不入。
		if resp.StatusCode >= 500 || resp.StatusCode == 401 || resp.StatusCode == 403 {
			reportFailure("upstream_" + strconv.Itoa(resp.StatusCode))
		}
		contentType := resp.Header.Get("content-type")
		if contentType == "" {
			contentType = "application/json"
		}
		if len(text) == 0 {
			spec.errorResponse(c, resp.StatusCode, "api_error", "upstream error")
			return
		}
		c.Data(resp.StatusCode, contentType, text)
		return
	}

	// 成功转发到响应头即重置连续失败计数。
	if upstreamProviderID != "" {
		if err := deps.Store.ReportUpstreamSuccess(ctx, upstreamProviderID); err != nil {
			deps.Logger.Error("熔断计数重置失败", "error", err)
		}
	}

	// ---- 非流式:一次结算(header 限时覆盖整个响应体读取) ----
	if !stream {
		text, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			headerTimer.Stop()
			errorType := "upstream_body_failure"
			if headerTimedOut.Load() {
				errorType = "upstream_timeout"
			}
			deps.Logger.Warn("上游响应体读取失败", "requestId", requestID, "errorType", errorType)
			failWithUsage(store.UsageStatusError, errorType)
			if headerTimedOut.Load() {
				spec.errorResponse(c, 504, "api_error", "上游响应异常，请稍后重试")
				return
			}
			spec.errorResponse(c, 502, "api_error", "上游响应异常，请稍后重试")
			return
		}
		headerTimer.Stop()
		var usage UsageCounts
		if parsed := spec.parseUsageJSON(string(text)); parsed != nil {
			usage = *parsed
		}
		if err := deps.Store.SettleUsage(ctx, buildUsageRecord(usage, store.UsageStatusOk, ""), estimateMicro); err != nil {
			deps.Logger.Error("结算写入失败", "requestId", requestID, "error", err)
		}
		contentType := resp.Header.Get("content-type")
		if contentType == "" {
			contentType = "application/json"
		}
		c.Data(200, contentType, text)
		return
	}

	// ---- 流式:边透传边采集,结束结算;客户端中断按 aborted ----
	headerTimer.Stop()
	scanner := spec.newScanner()
	var idleFired atomic.Bool
	idleTimer := time.AfterFunc(time.Duration(upstreamIdleTimeoutMs)*time.Millisecond, func() {
		idleFired.Store(true)
		cancelUpstream()
	})
	defer idleTimer.Stop()

	finalize := func(status store.UsageStatus) {
		if settled.Swap(true) {
			return
		}
		idleTimer.Stop()
		if dropped := scanner.Dropped(); dropped > 0 {
			deps.Logger.Warn("流式 usage 存在无法解析/被截断的事件帧", "requestId", requestID, "dropped", dropped)
		}
		errorType := ""
		if status == store.UsageStatusAborted {
			errorType = "client_aborted"
		}
		if err := deps.Store.SettleUsage(ctx, buildUsageRecord(scanner.Usage(), status, errorType), estimateMicro); err != nil {
			deps.Logger.Error("流式结算写入失败", "requestId", requestID, "error", err)
		}
	}

	contentType := resp.Header.Get("content-type")
	if contentType == "" {
		contentType = "text/event-stream"
	}
	c.Header("content-type", contentType)
	c.Header("cache-control", "no-store")
	c.Writer.WriteHeader(200)
	c.Writer.Flush()

	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			idleTimer.Reset(time.Duration(upstreamIdleTimeoutMs) * time.Millisecond)
			scanner.Push(string(buf[:n]))
			if _, writeErr := c.Writer.Write(buf[:n]); writeErr != nil {
				// 客户端断开:按已采集 usage 结算。
				finalize(store.UsageStatusAborted)
				return
			}
			c.Writer.Flush()
		}
		if readErr == nil {
			continue
		}
		if readErr == io.EOF {
			finalize(store.UsageStatusOk)
			return
		}
		if ctx.Err() != nil {
			// 客户端断开触发的取消。
			finalize(store.UsageStatusAborted)
			return
		}
		if idleFired.Load() {
			deps.Logger.Warn("上游流式传输空闲超时", "requestId", requestID)
			failWithUsage(store.UsageStatusError, "upstream_idle_timeout")
			reportFailure("upstream_idle_timeout")
			return
		}
		deps.Logger.Warn("上游流式中断", "requestId", requestID, "error", readErr)
		failWithUsage(store.UsageStatusError, "upstream_stream_failure")
		return
	}
}
