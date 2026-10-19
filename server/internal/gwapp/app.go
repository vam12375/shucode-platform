// Package gwapp:模型网关(specs/go-rewrite.md §2.6)。
// 管道与 apps/gateway/src/app.ts 逐行为对齐:
// 鉴权 → 模型校验 → 上游解析 → 额度预留(原子) → 透传转发 → usage 结算。
// 三协议差异只体现在请求体字段读取、上游鉴权头、usage 解析与流式采集器。
package gwapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/vam12375/shucode-platform/server/internal/pricing"
	"github.com/vam12375/shucode-platform/server/internal/ratelimit"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

const (
	maxMaxTokens             = 256_000
	defaultMaxTokens         = 4_096
	maxUpstreamErrorBytes    = 1_000_000
	maxGatewayRequestBytes   = 32 << 20 // 32 MiB，覆盖带图片的多模态请求
	maxUpstreamResponseBytes = 32 << 20
	maxOutputCandidates      = 16
	maxRequestIDLength       = 128
)

type Deps struct {
	Store      store.PlatformStore
	Env        *Env
	Now        func() time.Time
	HTTPClient *http.Client
	Logger     *slog.Logger
	SecretBox  *crypto.SecretBox // PLATFORM_SECRET_KEY 缺省时为 nil(绑定供应商密文不可解析)
	Trusted    trustedProxies
	// RateLimiter 为 nil 时跳过团队级限流（测试/极简部署）。
	RateLimiter *ratelimit.GatewayLimiter
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
	// UsageObserved:上游是否真的返回过 usage；零用量与缺少计量必须区分。
	UsageObserved() bool
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
			// 计量选项必须由网关强制控制：客户端显式关闭 include_usage 时也要覆盖，
			// 否则上游不返回最终 usage，会形成“有内容、零扣费”的缺口。
			if stream {
				options, _ := body["stream_options"].(map[string]any)
				if options == nil {
					options = map[string]any{}
				}
				options["include_usage"] = true
				body["stream_options"] = options
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
	// 与 TS 路由逐位兼容:Gin 默认会把未知路径的尾斜杠 301 到去斜杠版本,TS 直接 404。
	app.RedirectTrailingSlash = false
	app.RedirectFixedPath = false
	app.Use(func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				deps.Logger.Error("网关请求处理失败", "path", c.Request.URL.Path, "error", recovered)
				if !c.Writer.Written() {
					anthropicError(c, 500, "api_error", "网关内部错误")
				}
				c.Abort()
			}
		}()
		c.Next()
	})
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
	if device == nil || device.RevokedAt != nil ||
		device.Expired(deps.Now(), deps.Env.DeviceTokenTTL) {
		errorResponse(c, 401, "authentication_error", "平台凭据无效、已吊销或已过期")
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

// resolveGatewayTeam 同时检查团队与成员状态；历史多团队数据不能按列表顺序静默选取。
func resolveGatewayTeam(c *gin.Context, deps Deps, userID string, errorResponse errorResponder) (*store.TeamMembership, *store.TeamPolicyRecord, bool) {
	membership, err := store.ResolveUserTeam(c.Request.Context(), deps.Store, userID)
	if errors.Is(err, store.ErrAmbiguousTeam) {
		errorResponse(c, 409, "permission_error", "账号存在多个有效团队，请联系管理员明确团队归属")
		return nil, nil, false
	}
	if err != nil {
		deps.Logger.Error("团队查询失败", "error", err)
		errorResponse(c, 500, "api_error", "网关内部错误")
		return nil, nil, false
	}
	if membership == nil {
		errorResponse(c, 403, "permission_error", "账号没有有效团队或团队访问已停用，请联系团队管理员")
		return nil, nil, false
	}
	policy, err := deps.Store.GetTeamPolicy(c.Request.Context(), membership.Team.ID)
	if err != nil {
		deps.Logger.Error("团队策略查询失败", "error", err)
		errorResponse(c, 500, "api_error", "网关内部错误")
		return nil, nil, false
	}
	return membership, policy, true
}

// handleModelsRequest:OpenAI list 兼容清单，只返回当前有效团队允许使用的模型。
func handleModelsRequest(c *gin.Context, deps Deps) {
	_, user, ok := authenticate(c.Request, deps, openAIError, c)
	if !ok {
		return
	}
	_, policy, ok := resolveGatewayTeam(c, deps, user.ID, openAIError)
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
		if !store.TeamModelAllowed(policy, model.ModelID) {
			continue
		}
		data = append(data, gin.H{
			"id":       model.ModelID,
			"object":   "model",
			"created":  model.CreatedAt.Unix(),
			"owned_by": "mendao-platform",
		})
	}
	c.JSON(200, gin.H{"object": "list", "data": data})
}

func priceRates(model *store.PlatformModelRecord, at time.Time, holidays map[string]bool) credits.ModelPriceRates {
	parse := func(value string) float64 {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || parsed < 0 {
			return 0
		}
		return parsed
	}
	multiplier := 1.0
	if parsed := parse(model.OffPeakMultiplier); parsed > 0 {
		multiplier = parsed
	}
	factor := pricing.OffPeakMultiplierAt(at, multiplier, holidays)
	return credits.ModelPriceRates{
		InputPerMtok:      parse(model.InputPricePerMtok) * factor,
		OutputPerMtok:     parse(model.OutputPricePerMtok) * factor,
		CacheReadPerMtok:  parse(model.CacheReadPricePerMtok) * factor,
		CacheWritePerMtok: parse(model.CacheWritePricePerMtok) * factor,
	}
}

func requestedOutputCount(body map[string]any, max int64) (int64, bool) {
	raw, exists := body["n"]
	if !exists {
		return 1, true
	}
	numeric, ok := raw.(float64)
	if !ok || math.IsNaN(numeric) || math.IsInf(numeric, 0) ||
		numeric != math.Trunc(numeric) || numeric < 1 || numeric > float64(max) {
		return 0, false
	}
	return int64(numeric), true
}

// clampRequestedMaxTokens:内部夹紧值必须同步写回上游，否则准入预留与实际输出上限不一致。
func clampRequestedMaxTokens(body map[string]any) {
	for _, key := range []string{"max_completion_tokens", "max_tokens", "max_output_tokens"} {
		value, ok := body[key].(float64)
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value <= maxMaxTokens {
			continue
		}
		body[key] = float64(maxMaxTokens)
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

func reservationRequestWindows(ttl time.Duration) (requestLifetime, settlementTimeout time.Duration) {
	if ttl <= 0 {
		ttl = 30 * time.Minute // 与存储端缺省预留 TTL 保持一致。
	}
	settlementTimeout = min(10*time.Second, ttl/10)
	if settlementTimeout <= 0 {
		settlementTimeout = time.Nanosecond
	}
	return ttl - settlementTimeout, settlementTimeout
}

func handleProtocolRequest(c *gin.Context, deps Deps, spec protocolSpec) {
	ctx := c.Request.Context()
	// ---- 鉴权 ----
	device, user, ok := authenticate(c.Request, deps, spec.errorResponse, c)
	if !ok {
		return
	}
	membership, policy, ok := resolveGatewayTeam(c, deps, user.ID, spec.errorResponse)
	if !ok {
		return
	}
	effectiveTeam := effectiveTeamLimits(membership.Team, policy)
	primary := &effectiveTeam
	if err := deps.Store.TouchDeviceToken(ctx, device.ID, deps.Now()); err != nil {
		deps.Logger.Warn("设备活跃时间更新失败", "error", err)
	}
	clientIP := clientIPOf(c, deps)

	// ---- 请求体校验 ----
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxGatewayRequestBytes)
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			spec.errorResponse(c, 413, "invalid_request_error", "请求体超过 32 MiB 上限")
			return
		}
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
	// 客户端可改写配置并手工构造 model，模型白名单必须在网关准入执行。
	if !store.TeamModelAllowed(policy, modelID) {
		spec.errorResponse(c, 403, "permission_error", "团队未授权使用该模型："+modelID)
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
	// n>1 时输出可能成倍放大：预留按 n 倍输出上限计算，超过 16 个候选直接拒绝。
	outputCount := int64(1)
	if spec.family == "openai" {
		parsedCount, okCount := requestedOutputCount(body, maxOutputCandidates)
		if !okCount {
			spec.errorResponse(c, 400, "invalid_request_error", "n 必须是 1..16 的整数")
			return
		}
		outputCount = parsedCount
	}
	maxTokens *= outputCount
	clampRequestedMaxTokens(body)
	priceCapturedAt := deps.Now()
	rates := priceRates(model, priceCapturedAt, deps.Env.PeakHoursHolidays)
	priceCapture := captureUsagePricing(ctx, deps, model, priceCapturedAt)

	// ---- 解析上游目标：渠道池按 priority/weight 产出候选；无路由时兼容回退 ----
	targets, routeErr := resolveTargets(ctx, deps, model, spec)
	if routeErr != nil {
		spec.errorResponse(c, routeErr.Status, routeErr.Type, routeErr.Message)
		return
	}

	requestID := strings.TrimSpace(c.Request.Header.Get("x-request-id"))
	if len(requestID) > maxRequestIDLength {
		spec.errorResponse(c, 400, "invalid_request_error", "x-request-id 过长")
		return
	}
	if requestID == "" {
		requestID, _ = contracts.RandomUUIDv4()
	}

	// ---- 团队级流控：RPM / TPM / 并发（0 = 不限） ----
	if deps.RateLimiter != nil {
		if primary.RPMLimit > 0 {
			allowed, err := deps.RateLimiter.AllowRPM(ctx, primary.ID, primary.RPMLimit)
			if err != nil {
				deps.Logger.Warn("RPM 限流不可用，按 fail-open 放行", "error", err)
			} else if !allowed {
				c.Header("retry-after", "60")
				spec.errorResponse(c, 429, "rate_limit_error", "团队请求频率超限（RPM），请稍后重试")
				return
			}
		}
		if primary.TPMLimit > 0 {
			cost := estimateRequestTokens(body, maxTokens)
			allowed, err := deps.RateLimiter.AllowTPM(ctx, primary.ID, primary.TPMLimit, cost)
			if err != nil {
				deps.Logger.Warn("TPM 限流不可用，按 fail-open 放行", "error", err)
			} else if !allowed {
				c.Header("retry-after", "60")
				spec.errorResponse(c, 429, "rate_limit_error", "团队 Token 速率超限（TPM），请稍后重试")
				return
			}
		}
		if primary.ConcurrencyLimit > 0 {
			release, acquired, err := deps.RateLimiter.AcquireConcurrency(ctx, primary.ID, primary.ConcurrencyLimit)
			if err != nil {
				deps.Logger.Warn("并发限流不可用，按 fail-open 放行", "error", err)
			} else if !acquired {
				c.Header("retry-after", "1")
				spec.errorResponse(c, 429, "rate_limit_error", "团队并发请求超限，请稍后重试")
				return
			} else {
				defer release()
			}
		}
	}

	// ---- 额度准入：预留状态机 + request_id 幂等 ----
	promptTokens := estimatePromptTokens(body)
	estimateMicro := credits.EstimateReservationMicroCredits(promptTokens, maxTokens, rates)
	reservationID, _ := contracts.RandomUUIDv4()
	// 持续流量会不断重置 idle timer，旧实现可活过预留 TTL，被 sweeper 提前释放后绕过预算。
	// 从准入开始让所有重试共用总寿命，并在 TTL 前保留结算窗口；短 TTL 按比例留余量。
	requestLifetime, settlementTimeout := reservationRequestWindows(deps.Env.ReservationTTL)
	requestCtx, cancelRequest := context.WithTimeout(ctx, requestLifetime)
	defer cancelRequest()
	reserveResult, err := deps.Store.ReserveQuota(requestCtx, store.ReserveQuotaInput{
		ID: reservationID, TeamID: primary.ID, UserID: user.ID, RequestID: requestID,
		Micro: estimateMicro, TTL: deps.Env.ReservationTTL, CreatedAt: deps.Now(),
	})
	if err != nil {
		deps.Logger.Error("额度预留失败", "error", err)
		spec.errorResponse(c, 500, "api_error", "网关内部错误")
		return
	}
	if reserveResult == store.ReserveDuplicate {
		spec.errorResponse(c, 409, "invalid_request_error", "request_id 已存在，拒绝重复请求："+requestID)
		return
	}
	if reserveResult != store.ReserveReserved {
		spec.errorResponse(c, 429, "rate_limit_error", "团队额度或预算已用尽，请联系团队管理员 / Team quota or budget exhausted")
		return
	}
	// 慢客户端也不能把处理器卡在响应写入，拖到预留过期才开始结算。
	responseController := http.NewResponseController(c.Writer)
	requestDeadline, _ := requestCtx.Deadline()
	if responseController.SetWriteDeadline(requestDeadline) == nil {
		defer responseController.SetWriteDeadline(time.Time{})
	}

	startedAt := deps.Now()
	var settled atomic.Bool
	lastTarget := targets[0]

	buildUsageRecord := func(target upstreamTarget, usage UsageCounts, status store.UsageStatus, errorType string, micro int64) store.UsageRecord {
		record := store.UsageRecord{
			RequestID:       requestID,
			UserID:          user.ID,
			TeamID:          primary.ID,
			DeviceTokenID:   device.ID,
			Model:           model.ModelID,
			UpstreamModel:   target.UpstreamModel,
			InputTokens:     usage.InputTokens,
			OutputTokens:    usage.OutputTokens,
			CacheReadTokens: usage.CacheReadTokens, CacheCreationTokens: usage.CacheCreationTokens,
			CreditsMicro:  micro,
			PriceSnapshot: priceCapture.snapshot(target, usage, status, errorType),
			Status:        status,
			ProviderID:    target.ProviderID,
			CreatedAt:     deps.Now(),
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

	usageMicro := func(usage UsageCounts) int64 {
		micro, err := credits.ComputeUsageMicroCredits(credits.UsageTokenCounts{
			InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
			CacheReadTokens: usage.CacheReadTokens, CacheCreationTokens: usage.CacheCreationTokens,
		}, rates)
		if err != nil {
			deps.Logger.Error("用量计价失败", "requestId", requestID, "error", err)
			return 0
		}
		return micro
	}

	settle := func(target upstreamTarget, usage UsageCounts, status store.UsageStatus, errorType string, microOverride *int64) {
		if settled.Swap(true) {
			return
		}
		micro := usageMicro(usage)
		if microOverride != nil {
			micro = *microOverride
		}
		record := buildUsageRecord(target, usage, status, errorType, micro)
		if microOverride != nil && record.PriceSnapshot != nil {
			record.PriceSnapshot.Cost.UsageEstimated = true
		}
		// 客户端取消/断连会取消请求 ctx；结算必须使用独立且有界的 ctx，
		// 否则 Postgres 事务在 Begin 阶段即失败，已发生用量永远落不了库。
		// 多次重试共享结算窗口，避免每次重新计时后累计超过预留 TTL。
		settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settlementTimeout)
		defer cancel()
		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			lastErr = deps.Store.SettleReservation(settleCtx, reservationID, record)
			if lastErr == nil {
				return
			}
			if attempt == 2 {
				break
			}
			select {
			case <-settleCtx.Done():
				lastErr = settleCtx.Err()
			case <-time.After(time.Duration(100*(attempt+1)) * time.Millisecond):
			}
			if settleCtx.Err() != nil {
				break
			}
		}
		deps.Logger.Error("结算写入失败", "requestId", requestID, "error", lastErr)
	}

	failWithUsage := func(target upstreamTarget, status store.UsageStatus, errorType string) {
		settle(target, UsageCounts{}, status, errorType, nil)
	}

	// 上游成功返回但缺少 usage：零计费会让所有实际消耗免费，按准入预留额保守结算，
	// 并以 usage_missing_estimated 标记，供对账/告警人工核销。
	settleMissingUsage := func(target upstreamTarget, status store.UsageStatus, errorType string) {
		usage := UsageCounts{InputTokens: promptTokens, OutputTokens: maxTokens}
		settle(target, usage, status, errorType, &estimateMicro)
	}
	respondLifetimeTimeout := func() {
		// 此时结算已经完成，允许在剩余窗口内把 504 写回尚未开始响应的客户端。
		_ = responseController.SetWriteDeadline(requestDeadline.Add(settlementTimeout))
		spec.errorResponse(c, 504, "api_error", "请求总时长超限，请稍后重试")
	}

	reportFailure := func(providerID, reason string) {
		if providerID == "" || deps.Env.ProviderAutoDisableThreshold <= 0 {
			return
		}
		disabled, err := deps.Store.ReportUpstreamFailure(requestCtx, providerID, reason, deps.Env.ProviderAutoDisableThreshold)
		if err != nil {
			deps.Logger.Error("熔断计数失败", "error", err)
			return
		}
		if disabled {
			deps.Logger.Warn("上游供应商连续失败，已自动停用", "providerId", providerID, "reason", reason)
		}
	}

	maxAttempts := deps.Env.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	if int64(len(targets)) < maxAttempts {
		maxAttempts = int64(len(targets))
	}

	var (
		lastStatus      = 502
		lastErrorType   = "upstream_unavailable"
		lastMessage     = "上游供应商暂时不可用，请稍后重试"
		lastBody        []byte
		lastContentType = "application/json"
	)

	for attempt := int64(0); attempt < maxAttempts; attempt++ {
		target := targets[attempt]
		lastTarget = target
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			failWithUsage(target, store.UsageStatusError, "request_lifetime_timeout")
			respondLifetimeTimeout()
			return
		}
		upstreamConnectTimeoutMs := target.TimeoutMs
		if upstreamConnectTimeoutMs <= 0 {
			upstreamConnectTimeoutMs = deps.Env.UpstreamTimeoutMs
		}
		upstreamIdleTimeoutMs := target.TimeoutMs
		if upstreamIdleTimeoutMs <= 0 {
			upstreamIdleTimeoutMs = deps.Env.UpstreamIdleTimeoutMs
		}
		upstreamHeaders, upstreamBody := spec.buildUpstream(c.Request, body, target.UpstreamModel, stream, target.APIKey, target.AnthropicVersion)
		upstreamURL := strings.TrimRight(target.BaseURL, "/") + spec.upstreamPath
		// 上游请求必须绑定带取消的 ctx:header 超时、空闲看门狗、客户端断连都靠它中断。
		upstreamCtx, cancelUpstream := context.WithCancel(requestCtx)
		req, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, upstreamURL, bytes.NewReader(upstreamBody))
		if err != nil {
			cancelUpstream()
			deps.Logger.Error("构造上游请求失败", "error", err)
			failWithUsage(target, store.UsageStatusError, "upstream_unavailable")
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
			cancelUpstream()
			if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
				failWithUsage(target, store.UsageStatusError, "request_lifetime_timeout")
				respondLifetimeTimeout()
				return
			}
			errorType := "upstream_unavailable"
			if headerTimedOut.Load() {
				errorType = "upstream_timeout"
			}
			deps.Logger.Warn("上游请求失败", "requestId", requestID, "attempt", attempt+1,
				"providerId", target.ProviderID, "timedOut", headerTimedOut.Load(), "error", err)
			reportFailure(target.ProviderID, errorType)
			if ctx.Err() != nil {
				failWithUsage(target, store.UsageStatusAborted, "client_aborted")
				return
			}
			lastStatus, lastErrorType = 502, errorType
			lastMessage = "上游供应商暂时不可用，请稍后重试"
			lastBody = nil
			if headerTimedOut.Load() {
				lastStatus, lastMessage = 504, "上游响应超时，请稍后重试"
			}
			if attempt+1 < maxAttempts {
				continue
			}
			failWithUsage(target, store.UsageStatusError, errorType)
			spec.errorResponse(c, lastStatus, "api_error", lastMessage)
			return
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			headerTimer.Stop()
			text, _ := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamErrorBytes))
			resp.Body.Close()
			cancelUpstream()
			if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
				failWithUsage(target, store.UsageStatusError, "request_lifetime_timeout")
				respondLifetimeTimeout()
				return
			}
			// 上游/中间代理可能在错误正文回显密钥；返回普通调用者前先脱敏。
			text = redactSecret(text, target.APIKey)
			errorType := "upstream_" + strconv.Itoa(resp.StatusCode)
			deps.Logger.Warn("上游返回错误", "requestId", requestID, "attempt", attempt+1,
				"providerId", target.ProviderID, "status", resp.StatusCode)
			// 只有供应商自身故障(5xx 与鉴权失效 401/403)计入熔断;404/429 等不入。
			if resp.StatusCode >= 500 || resp.StatusCode == 401 || resp.StatusCode == 403 {
				reportFailure(target.ProviderID, errorType)
			}
			contentType := resp.Header.Get("content-type")
			if contentType == "" {
				contentType = "application/json"
			}
			lastStatus, lastErrorType = resp.StatusCode, errorType
			lastBody, lastContentType = text, contentType
			lastMessage = "upstream error"
			if retryableUpstreamStatus(resp.StatusCode) && attempt+1 < maxAttempts {
				continue
			}
			failWithUsage(target, store.UsageStatusError, errorType)
			if len(text) == 0 {
				spec.errorResponse(c, resp.StatusCode, "api_error", "upstream error")
				return
			}
			c.Data(resp.StatusCode, contentType, text)
			return
		}

		// 成功转发到响应头即重置连续失败计数（无渠道池的 env 上游不计数）。
		if target.ProviderID != "" {
			if err := deps.Store.ReportUpstreamSuccess(requestCtx, target.ProviderID); err != nil {
				deps.Logger.Error("熔断计数重置失败", "error", err)
			}
		}

		// ---- 非流式:一次结算(header 限时覆盖整个响应体读取) ----
		if !stream {
			text, readErr := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamResponseBytes+1))
			resp.Body.Close()
			if readErr == nil && len(text) > maxUpstreamResponseBytes {
				readErr = errors.New("上游响应体超过大小上限")
			}
			if readErr != nil {
				headerTimer.Stop()
				cancelUpstream()
				if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
					settleMissingUsage(target, store.UsageStatusError, "request_lifetime_timeout")
					respondLifetimeTimeout()
					return
				}
				errorType := "upstream_body_failure"
				if headerTimedOut.Load() {
					errorType = "upstream_timeout"
				}
				deps.Logger.Warn("上游响应体读取失败", "requestId", requestID, "errorType", errorType)
				failWithUsage(target, store.UsageStatusError, errorType)
				if headerTimedOut.Load() {
					spec.errorResponse(c, 504, "api_error", "上游响应异常，请稍后重试")
					return
				}
				spec.errorResponse(c, 502, "api_error", "上游响应异常，请稍后重试")
				return
			}
			headerTimer.Stop()
			cancelUpstream()
			parsedUsage := spec.parseUsageJSON(string(text))
			if parsedUsage == nil {
				deps.Logger.Warn("上游成功响应缺少 usage，按准入预留额保守结算", "requestId", requestID)
				settleMissingUsage(target, store.UsageStatusError, "usage_missing_estimated")
			} else {
				settle(target, *parsedUsage, store.UsageStatusOk, "", nil)
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

		finalize := func(status store.UsageStatus) {
			idleTimer.Stop()
			cancelUpstream()
			if dropped := scanner.Dropped(); dropped > 0 {
				deps.Logger.Warn("流式 usage 存在无法解析/被截断的事件帧", "requestId", requestID, "dropped", dropped)
			}
			errorType := ""
			if status == store.UsageStatusAborted {
				errorType = "client_aborted"
			}
			if !scanner.UsageObserved() {
				deps.Logger.Warn("流式响应结束但未采集到 usage，按准入预留额保守结算",
					"requestId", requestID, "status", status)
				if errorType == "" {
					settleMissingUsage(target, status, "usage_missing_estimated")
				} else {
					// 保留 client_aborted 等既有语义，但同样按预留额保守结算。
					settle(target, scanner.Usage(), status, errorType, &estimateMicro)
				}
				return
			}
			settle(target, scanner.Usage(), status, errorType, nil)
		}
		finalizeLifetimeTimeout := func() {
			idleTimer.Stop()
			cancelUpstream()
			if !scanner.UsageObserved() {
				settleMissingUsage(target, store.UsageStatusError, "request_lifetime_timeout")
				return
			}
			settle(target, scanner.Usage(), store.UsageStatusError, "request_lifetime_timeout", nil)
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
					resp.Body.Close()
					if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
						finalizeLifetimeTimeout()
						return
					}
					finalize(store.UsageStatusAborted)
					return
				}
				c.Writer.Flush()
			}
			if readErr == nil {
				continue
			}
			resp.Body.Close()
			if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
				finalizeLifetimeTimeout()
				return
			}
			// 空闲看门狗会取消上游 ctx；取消后上游通常以 EOF 结束，
			// 必须先判定 idleFired，否则会把超时误记成成功。
			if idleFired.Load() {
				deps.Logger.Warn("上游流式传输空闲超时", "requestId", requestID)
				failWithUsage(target, store.UsageStatusError, "upstream_idle_timeout")
				reportFailure(target.ProviderID, "upstream_idle_timeout")
				return
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
			deps.Logger.Warn("上游流式中断", "requestId", requestID, "error", readErr)
			failWithUsage(target, store.UsageStatusError, "upstream_stream_failure")
			return
		}
	}

	// 所有候选都失败（通常不会走到这里，循环末次已提前返回）。
	failWithUsage(lastTarget, store.UsageStatusError, lastErrorType)
	if len(lastBody) > 0 {
		c.Data(lastStatus, lastContentType, lastBody)
		return
	}
	spec.errorResponse(c, lastStatus, "api_error", lastMessage)
}

func redactSecret(text []byte, secret string) []byte {
	if secret == "" {
		return text
	}
	return []byte(strings.ReplaceAll(string(text), secret, "[redacted]"))
}

func retryableUpstreamStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests ||
		status == http.StatusUnauthorized || status == http.StatusForbidden ||
		status >= 500
}
