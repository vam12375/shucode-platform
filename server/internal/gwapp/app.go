// Package gwapp:模型网关(specs/go-rewrite.md §2.6)。
// 管道与 apps/gateway/src/app.ts 逐行为对齐:
// 鉴权 → 模型校验 → 上游解析 → 零占用准入(原子) → 透传转发 → usage 结算。
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
	"net/url"
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
	"github.com/vam12375/shucode-platform/server/internal/upstreamurl"
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
	// SettlementFallback:结算重试耗尽后的持久化补偿（nil 时只记日志）。
	SettlementFallback SettlementFallback
	// AttemptRecorder:记录每个上游 attempt（含供应商请求 ID），便于故障转移对账。
	AttemptRecorder AttemptRecorder
}

type SettlementFallback interface {
	Persist(ctx context.Context, reservationID string, record store.UsageRecord) error
}

type AttemptRecorder interface {
	InsertGatewayAttempt(ctx context.Context, record store.GatewayAttemptRecord) error
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
	routePath     string
	upstreamPath  string
	family        string // "openai" | "anthropic"
	errorResponse errorResponder
	readModelID   func(body map[string]any) string
	readMaxTokens func(body map[string]any) int64
	// applyOutputLimit:把平台实际采用的输出上限写回上游请求。
	// 缺省值/夹紧值必须写回上游，确保实际执行遵守平台输出上限。
	applyOutputLimit func(body map[string]any, limit int64)
	isStream         func(body map[string]any) bool
	buildUpstream    func(r *http.Request, body map[string]any, upstreamModel string, stream bool, apiKey, anthropicVersion string) (map[string]string, []byte)
	parseUsageJSON   func(text string) *UsageCounts
	newScanner       func() streamScanner
}

type streamScanner interface {
	Push(chunk string)
	Usage() UsageCounts
	// UsageObserved:上游是否真的返回过 usage；零用量与缺少计量必须区分。
	UsageObserved() bool
	UsageComplete() bool
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
		applyOutputLimit: func(body map[string]any, limit int64) {
			body["max_tokens"] = float64(limit)
		},
		isStream: func(body map[string]any) bool { return body["stream"] == true },
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
		applyOutputLimit: func(body map[string]any, limit int64) {
			// 保留客户端使用的字段：新版推理模型只认 max_completion_tokens。
			if _, ok := body["max_completion_tokens"]; ok {
				delete(body, "max_tokens")
				body["max_completion_tokens"] = float64(limit)
				return
			}
			body["max_tokens"] = float64(limit)
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
		applyOutputLimit: func(body map[string]any, limit int64) {
			// 只保留协议的规范字段，避免别名携带更高上限绕过预留。
			delete(body, "max_tokens")
			delete(body, "max_completion_tokens")
			body["max_output_tokens"] = float64(limit)
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
	app.GET("/v1/responses/:responseID", func(c *gin.Context) { handleBackgroundResponseProxy(c, deps, http.MethodGet, false) })
	app.DELETE("/v1/responses/:responseID", func(c *gin.Context) { handleBackgroundResponseProxy(c, deps, http.MethodDelete, false) })
	app.POST("/v1/responses/:responseID/cancel", func(c *gin.Context) { handleBackgroundResponseProxy(c, deps, http.MethodPost, true) })
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
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
			return math.NaN()
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
		if raw >= maxMaxTokens {
			return maxMaxTokens
		}
		clamped := int64(raw)
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
	backgroundGeneration := spec.routePath == "/v1/responses" && body["background"] == true
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
	// 预留按“单次输出上限 × n”计算；写回上游的是单次上限，不能把 n 倍额度当成单条上限。
	perRequestMaxTokens := spec.readMaxTokens(body)
	outputCount := int64(1)
	if spec.family == "openai" {
		parsedCount, okCount := requestedOutputCount(body, maxOutputCandidates)
		if !okCount {
			spec.errorResponse(c, 400, "invalid_request_error", "n 必须是 1..16 的整数")
			return
		}
		outputCount = parsedCount
	}
	maxTokens := perRequestMaxTokens * outputCount
	clampRequestedMaxTokens(body)
	spec.applyOutputLimit(body, perRequestMaxTokens)
	priceCapturedAt := deps.Now()
	rates := priceRates(model, priceCapturedAt, deps.Env.PeakHoursHolidays)
	if !validPriceRates(rates) {
		spec.errorResponse(c, 503, "api_error", "模型计费配置无效，已阻止上游调用")
		return
	}
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

	// ---- 额度准入：只校验已结算余额，登记 request_id 幂等身份，不占用余额 ----
	reservationID, _ := contracts.RandomUUIDv4()
	// 流式与重试共用有界总寿命并保留结算窗口；请求身份持久化但金额恒为 0。
	requestLifetime, settlementTimeout := reservationRequestWindows(deps.Env.ReservationTTL)
	requestCtx, cancelRequest := context.WithTimeout(ctx, requestLifetime)
	defer cancelRequest()
	reserveResult, err := deps.Store.AdmitUsage(requestCtx, store.ReserveQuotaInput{
		ID: reservationID, TeamID: primary.ID, UserID: user.ID, RequestID: requestID,
		Micro: 0, TTL: deps.Env.ReservationTTL, CreatedAt: deps.Now(), RetainUntilSettled: true,
	})
	if err != nil {
		deps.Logger.Error("额度准入失败", "error", err)
		spec.errorResponse(c, 500, "api_error", "网关内部错误")
		return
	}
	if reserveResult == store.ReserveDuplicate {
		spec.errorResponse(c, 409, "invalid_request_error", "request_id 已存在，拒绝重复请求："+requestID)
		return
	}
	switch reserveResult {
	case store.ReserveDenied:
		spec.errorResponse(c, 429, "rate_limit_error", "团队余额已用尽，请充值或联系团队管理员；请求未发送到上游")
		return
	case store.ReserveDeniedBudget:
		spec.errorResponse(c, 429, "rate_limit_error", "团队或成员预算已用尽，请联系团队管理员")
		return
	case store.ReserveDeniedInactive:
		spec.errorResponse(c, 403, "permission_error", "团队或成员已停用，请联系团队管理员")
		return
	case store.ReserveReserved:
	default:
		spec.errorResponse(c, 429, "rate_limit_error", "团队额度状态异常，已在上游调用前拒绝请求")
		return
	}
	// 慢客户端也不能把响应写入无限期阻塞，结算使用独立有界 context。
	responseController := http.NewResponseController(c.Writer)
	requestDeadline, _ := requestCtx.Deadline()
	if responseController.SetWriteDeadline(requestDeadline) == nil {
		defer responseController.SetWriteDeadline(time.Time{})
	}

	startedAt := deps.Now()
	var settled atomic.Bool
	lastTarget := targets[0]

	providerHost := func(target upstreamTarget) string {
		parsed, err := url.Parse(target.BaseURL)
		if err != nil || parsed.Host == "" {
			return target.BaseURL
		}
		return parsed.Host
	}
	providerRequestIDFrom := func(resp *http.Response) string {
		if resp == nil {
			return ""
		}
		for _, key := range []string{"x-request-id", "request-id", "openai-request-id", "x-amz-request-id"} {
			if value := strings.TrimSpace(resp.Header.Get(key)); value != "" {
				return value
			}
		}
		return ""
	}
	// 记录每个候选的完整轨迹；写失败只告警，不影响已经完成的转发与结算。
	recordAttempt := func(target upstreamTarget, attempt int64, attemptStarted time.Time, statusCode *int, errorType, providerRequestID string, final bool) {
		if deps.AttemptRecorder == nil {
			return
		}
		recordCtx, cancelRecord := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancelRecord()
		err := deps.AttemptRecorder.InsertGatewayAttempt(recordCtx, store.GatewayAttemptRecord{
			RequestID: requestID, ReservationID: reservationID, Attempt: attempt + 1,
			ProviderID: target.ProviderID, UpstreamModel: target.UpstreamModel,
			BaseHost: providerHost(target), StatusCode: statusCode,
			ErrorType: errorType, ProviderRequestID: providerRequestID,
			StartedAt: attemptStarted, DurationMs: deps.Now().Sub(attemptStarted).Milliseconds(),
			Final: final,
		})
		if err != nil {
			deps.Logger.Warn("上游尝试记录失败", "requestId", requestID, "attempt", attempt+1,
				"providerId", target.ProviderID, "error", err)
		}
	}

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
	var backgroundJob *store.BackgroundGenerationJob
	var backgroundResponseID string
	persistBackground := func(target upstreamTarget, responseID string, observed *UsageCounts) bool {
		if !safeResponseID.MatchString(responseID) {
			return false
		}
		backgroundResponseID = responseID
		record := buildUsageRecord(target, UsageCounts{}, store.UsageStatusOk, "", 0)
		var observedAt *time.Time
		if observed != nil {
			record = buildUsageRecord(target, *observed, store.UsageStatusOk, "", 0)
			at := deps.Now()
			observedAt = &at
		}
		job := store.BackgroundGenerationJob{
			ReservationID: reservationID, ResponseID: responseID, ProviderID: target.ProviderID,
			BaseURL: target.BaseURL, TeamID: primary.ID, UserID: user.ID,
			Record: record, UsageObservedAt: observedAt, Limited: !primary.Unlimited, CreatedAt: startedAt, NextPollAt: deps.Now(),
			Rates: store.UsageChargeRates{
				InputPerMtok: rates.InputPerMtok, OutputPerMtok: rates.OutputPerMtok,
				CacheReadPerMtok: rates.CacheReadPerMtok, CacheWritePerMtok: rates.CacheWritePerMtok,
			},
		}
		persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancelPersist()
		if err := deps.Store.CreateBackgroundGeneration(persistCtx, job); err != nil {
			deps.Logger.Error("后台生成状态保存失败，保留零占用请求身份待核对", "reservationId", reservationID, "error", err)
			return false
		}
		backgroundJob = &job
		return true
	}

	defer func() {
		if !backgroundGeneration || backgroundJob != nil || settled.Load() {
			return
		}
		// 未拿到任务 ID 或任务保存失败时仍可能已受理；持久化归属和锁价供核对，不能盲目重提/扣费。
		record := buildUsageRecord(lastTarget, UsageCounts{}, store.UsageStatusError, "background_submission_unknown", 0)
		record.BackgroundResponseID = backgroundResponseID
		reviewCtx, cancelReview := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancelReview()
		var reviewErr error
		if deps.SettlementFallback != nil {
			reviewErr = deps.SettlementFallback.Persist(reviewCtx, reservationID, record)
		} else {
			reviewErr = deps.Store.EnqueuePendingSettlement(reviewCtx, reservationID, record)
		}
		if reviewErr != nil {
			deps.Logger.Error("后台未知受理核对记录保存失败", "reservationId", reservationID, "error", reviewErr)
		}
	}()

	settle := func(target upstreamTarget, usage UsageCounts, status store.UsageStatus, errorType string) {
		if settled.Swap(true) {
			return
		}
		micro, chargeErr := credits.ComputeUsageMicroCredits(credits.UsageTokenCounts{
			InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
			CacheReadTokens: usage.CacheReadTokens, CacheCreationTokens: usage.CacheCreationTokens,
		}, rates)
		if chargeErr != nil {
			deps.Logger.Error("用量计价失败，保留原始计量待核对", "requestId", requestID, "error", chargeErr)
			micro, status = 0, store.UsageStatusError
			if errorType == "" {
				errorType = "usage_invalid"
			}
		}
		record := buildUsageRecord(target, usage, status, errorType, micro)
		// 客户端取消/断连会取消请求 ctx；结算必须使用独立且有界的 ctx，
		// 否则 Postgres 事务在 Begin 阶段即失败，已发生用量永远落不了库。
		// 多次重试共享结算窗口，避免失败时无限期阻塞。
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
		if deps.SettlementFallback != nil {
			// 补偿写入必须脱离客户端请求 ctx（客户端可能已断开），并给独立短超时。
			fallbackCtx, cancelFallback := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			persistErr := deps.SettlementFallback.Persist(fallbackCtx, reservationID, record)
			cancelFallback()
			if persistErr != nil {
				deps.Logger.Error("结算写入失败且持久化补偿入队失败",
					"requestId", requestID, "error", lastErr, "persistError", persistErr)
			} else {
				deps.Logger.Warn("结算写入失败，已转入持久化补偿队列",
					"requestId", requestID, "error", lastErr)
			}
			return
		}
		deps.Logger.Error("结算写入失败", "requestId", requestID, "error", lastErr)
	}

	failWithUsage := func(target upstreamTarget, status store.UsageStatus, errorType string) {
		settle(target, UsageCounts{}, status, errorType)
	}

	// 旧逻辑把最大输出当作实际 usage，停止或缺失计量时会虚扣；只结算合法已观察值。
	// 后台任务独立于 HTTP 生命周期，结果不明时保留核对身份，不占余额且不盲目重发。
	settleMissingUsage := func(target upstreamTarget, status store.UsageStatus, errorType string) {
		if backgroundGeneration {
			deps.Logger.Error("后台生成结果不明，保留零占用核对身份", "reservationId", reservationID, "errorType", errorType)
			return
		}
		settle(target, UsageCounts{}, status, errorType)
	}
	settleClientAbort := func(target upstreamTarget, usage UsageCounts, upstreamMayHaveRun bool) {
		if backgroundGeneration && upstreamMayHaveRun {
			deps.Logger.Error("后台生成取消时无法确认上游终态，保留零占用核对身份", "reservationId", reservationID)
			return
		}
		// 前台取消同步取消上游；结束本次身份，有合法 usage 则实扣，没有则扣 0。
		settle(target, usage, store.UsageStatusAborted, "client_aborted")
	}

	respondLifetimeTimeout := func() {
		// 此时结算已经完成，允许在剩余窗口内把 504 写回尚未开始响应的客户端。
		_ = responseController.SetWriteDeadline(requestDeadline.Add(settlementTimeout))
		spec.errorResponse(c, 504, "api_error", "请求总时长超限，请稍后重试")
	}

	// 熔断计数必须用脱离请求取消的短上下文：客户端断连/总时长超时后仍要能记账。
	reportFailure := func(providerID, reason string) {
		if providerID == "" || deps.Env.ProviderAutoDisableThreshold <= 0 {
			return
		}
		reportCtx, cancelReport := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancelReport()
		disabled, err := deps.Store.ReportUpstreamFailure(reportCtx, providerID, reason, deps.Env.ProviderAutoDisableThreshold)
		if err != nil {
			deps.Logger.Error("熔断计数失败", "error", err)
			return
		}
		if disabled {
			deps.Logger.Warn("上游供应商连续失败，已自动停用", "providerId", providerID, "reason", reason)
		}
	}
	// 只有完整成功（非流式响应体读完 / 流式正常结束）才清零连续失败计数。
	// 写出 2xx 响应头不代表上游成功：流式空闲超时/中断同样可能发生在头之后。
	reportSuccess := func(providerID string) {
		if providerID == "" {
			return
		}
		reportCtx, cancelReport := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancelReport()
		if err := deps.Store.ReportUpstreamSuccess(reportCtx, providerID); err != nil {
			deps.Logger.Error("熔断计数重置失败", "error", err)
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
		attemptStarted := deps.Now()
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			recordAttempt(target, attempt, attemptStarted, nil, "request_lifetime_timeout", "", true)
			reportFailure(target.ProviderID, "request_lifetime_timeout")
			failWithUsage(target, store.UsageStatusError, "request_lifetime_timeout")
			respondLifetimeTimeout()
			return
		}
		if ctx.Err() != nil {
			// 准入后、上游发送前发生取消时，确认没有上游工作，结算零用量并结束当前请求身份。
			recordAttempt(target, attempt, attemptStarted, nil, "client_aborted", "", true)
			settleClientAbort(target, UsageCounts{}, false)
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
		upstreamURL := upstreamurl.Join(target.BaseURL, spec.upstreamPath)
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
				recordAttempt(target, attempt, attemptStarted, nil, "request_lifetime_timeout", "", true)
				reportFailure(target.ProviderID, "request_lifetime_timeout")
				settleMissingUsage(target, store.UsageStatusError, "request_lifetime_timeout")
				respondLifetimeTimeout()
				return
			}
			if ctx.Err() != nil {
				recordAttempt(target, attempt, attemptStarted, nil, "client_aborted", "", true)
				settleClientAbort(target, UsageCounts{}, true)
				return
			}
			errorType := "upstream_unavailable"
			if headerTimedOut.Load() {
				errorType = "upstream_timeout"
			}
			deps.Logger.Warn("上游请求失败", "requestId", requestID, "attempt", attempt+1,
				"providerId", target.ProviderID, "timedOut", headerTimedOut.Load(), "error", err)
			reportFailure(target.ProviderID, errorType)
			lastStatus, lastErrorType = 502, errorType
			lastMessage = "上游供应商暂时不可用，请稍后重试"
			lastBody = nil
			if headerTimedOut.Load() {
				lastStatus, lastMessage = 504, "上游响应超时，请稍后重试"
			}

			// 请求可能已经被供应商执行，未知传输结果不能释放为零费用或切换上游重复生成。
			recordAttempt(target, attempt, attemptStarted, nil, errorType, "", true)
			settleMissingUsage(target, store.UsageStatusError, errorType)
			spec.errorResponse(c, lastStatus, "api_error", lastMessage)
			return
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			headerTimer.Stop()
			text, _ := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamErrorBytes))
			resp.Body.Close()
			cancelUpstream()
			if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
				recordAttempt(target, attempt, attemptStarted, &resp.StatusCode, "request_lifetime_timeout", providerRequestIDFrom(resp), true)
				reportFailure(target.ProviderID, "request_lifetime_timeout")
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
				recordAttempt(target, attempt, attemptStarted, &resp.StatusCode, errorType, providerRequestIDFrom(resp), false)
				continue
			}
			recordAttempt(target, attempt, attemptStarted, &resp.StatusCode, errorType, providerRequestIDFrom(resp), true)
			failWithUsage(target, store.UsageStatusError, errorType)
			if len(text) == 0 {
				spec.errorResponse(c, resp.StatusCode, "api_error", "upstream error")
				return
			}
			c.Data(resp.StatusCode, contentType, text)
			return
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
					recordAttempt(target, attempt, attemptStarted, &resp.StatusCode, "request_lifetime_timeout", providerRequestIDFrom(resp), true)
					reportFailure(target.ProviderID, "request_lifetime_timeout")
					settleMissingUsage(target, store.UsageStatusError, "request_lifetime_timeout")
					respondLifetimeTimeout()
					return
				}
				if ctx.Err() != nil {
					recordAttempt(target, attempt, attemptStarted, &resp.StatusCode, "client_aborted", providerRequestIDFrom(resp), true)
					settleClientAbort(target, UsageCounts{}, true)
					return
				}
				errorType := "upstream_body_failure"
				if headerTimedOut.Load() {
					errorType = "upstream_timeout"
				}
				recordAttempt(target, attempt, attemptStarted, &resp.StatusCode, errorType, providerRequestIDFrom(resp), true)
				deps.Logger.Warn("上游响应体读取失败", "requestId", requestID, "errorType", errorType)
				reportFailure(target.ProviderID, errorType)
				settleMissingUsage(target, store.UsageStatusError, errorType)
				if headerTimedOut.Load() {
					spec.errorResponse(c, 504, "api_error", "上游响应异常，请稍后重试")
					return
				}
				spec.errorResponse(c, 502, "api_error", "上游响应异常，请稍后重试")
				return
			}
			headerTimer.Stop()
			cancelUpstream()
			recordAttempt(target, attempt, attemptStarted, &resp.StatusCode, "", providerRequestIDFrom(resp), true)
			reportSuccess(target.ProviderID)
			if backgroundGeneration {
				responseID, responseStatus := backgroundResponseIdentity(text)
				if !persistBackground(target, responseID, spec.parseUsageJSON(string(text))) {
					spec.errorResponse(c, 502, "api_error", "后台任务已提交但状态暂不可确认，请联系管理员核对")
					return
				}
				if terminalBackgroundStatus(responseStatus) {
					job, jobErr := deps.Store.FindBackgroundGeneration(context.WithoutCancel(ctx), primary.ID, responseID)
					if jobErr == nil && job != nil {
						finishBackgroundGeneration(ctx, deps, *job, redactSecret(text, target.APIKey), responseStatus, resp.Header.Get("content-type"))
					}
				}
				c.Data(200, "application/json", text)
				return
			}

			parsedUsage := spec.parseUsageJSON(string(text))
			if parsedUsage == nil {
				deps.Logger.Warn("上游成功响应缺少 usage，不估算扣费，记录待核对", "requestId", requestID)
				settleMissingUsage(target, store.UsageStatusError, "usage_missing")
			} else {
				settle(target, *parsedUsage, store.UsageStatusOk, "")
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
		backgroundSaved := false
		persistObservedBackgroundUsage := func() {
			if !backgroundSaved || backgroundJob == nil || !scanner.UsageObserved() {
				return
			}
			observed := scanner.Usage()
			previous := backgroundJob.Record
			if backgroundJob.UsageObservedAt != nil && previous.InputTokens == observed.InputTokens && previous.OutputTokens == observed.OutputTokens && previous.CacheReadTokens == observed.CacheReadTokens && previous.CacheCreationTokens == observed.CacheCreationTokens {
				return
			}
			updated, err := persistBackgroundUsage(ctx, deps, *backgroundJob, observed)
			if err != nil {
				deps.Logger.Error("后台流合法用量观察保存失败", "reservationId", reservationID, "error", err)
				return
			}
			backgroundJob = updated
		}
		defer persistObservedBackgroundUsage()
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
			// 客户端主动中断时上游流本身是健康的，仍算一次成功转发。
			if status == store.UsageStatusOk || status == store.UsageStatusAborted {
				reportSuccess(target.ProviderID)
			}
			errorType := ""
			if status == store.UsageStatusAborted {
				errorType = "client_aborted"
			}
			recordAttempt(target, attempt, attemptStarted, &resp.StatusCode, errorType, providerRequestIDFrom(resp), true)
			if backgroundGeneration && backgroundSaved {
				// 持久化后台任务统一由 worker 取得终态并结算；客户端断流不终止其所有权。
				return
			}
			if backgroundGeneration && !scanner.UsageComplete() {
				deps.Logger.Error("后台生成流缺少任务 ID 或终态，保留零占用核对身份", "reservationId", reservationID)
				return
			}
			if status == store.UsageStatusAborted {
				usage := scanner.Usage()
				if !scanner.UsageObserved() {
					usage = UsageCounts{}
				}
				settleClientAbort(target, usage, true)
				return
			}
			if !scanner.UsageComplete() || scanner.Dropped() > 0 {
				deps.Logger.Warn("流式响应缺少完整最终 usage，仅结算已观察合法用量",
					"requestId", requestID, "status", status)
				if errorType == "" {
					settle(target, scanner.Usage(), status, "usage_missing")
				} else {
					// 保留上游异常原因，不估算未知尾部用量。
					settle(target, scanner.Usage(), status, errorType)
				}
				return
			}
			settle(target, scanner.Usage(), status, errorType)
		}
		finalizeLifetimeTimeout := func() {
			idleTimer.Stop()
			cancelUpstream()
			recordAttempt(target, attempt, attemptStarted, &resp.StatusCode, "request_lifetime_timeout", providerRequestIDFrom(resp), true)
			reportFailure(target.ProviderID, "request_lifetime_timeout")
			if !backgroundGeneration {
				settle(target, scanner.Usage(), store.UsageStatusError, "request_lifetime_timeout")
			}
		}
		// 断流仅结算合法已观察 usage，不能把未知尾部按输出上限虚扣。
		failStreamWithUsage := func(errorType string) {
			if !backgroundGeneration {
				settle(target, scanner.Usage(), store.UsageStatusError, errorType)
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
				if backgroundGeneration && !backgroundSaved {
					if responses, ok := scanner.(*ResponsesScanner); ok && responses.ResponseID() != "" {
						var observed *UsageCounts
						if scanner.UsageObserved() {
							usage := scanner.Usage()
							observed = &usage
						}
						backgroundSaved = persistBackground(target, responses.ResponseID(), observed)
						if !backgroundSaved {
							idleTimer.Stop()
							cancelUpstream()
							_ = resp.Body.Close()
							return
						}
					}
				}
				persistObservedBackgroundUsage()
				if _, writeErr := c.Writer.Write(buf[:n]); writeErr != nil {
					// 客户端断开同步终止上游并按已观察用量结算，不能估算未知尾部。
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
			if ctx.Err() != nil {
				// 上游可能因客户端取消返回 EOF 或 transport error，先检查请求 context 识别中断。
				finalize(store.UsageStatusAborted)
				return
			}
			// 空闲看门狗会取消上游 ctx；取消后上游通常以 EOF 结束，
			// 必须先判定 idleFired，否则会把超时误记成成功。
			if idleFired.Load() {
				deps.Logger.Warn("上游流式传输空闲超时", "requestId", requestID)
				recordAttempt(target, attempt, attemptStarted, &resp.StatusCode, "upstream_idle_timeout", providerRequestIDFrom(resp), true)
				failStreamWithUsage("upstream_idle_timeout")
				reportFailure(target.ProviderID, "upstream_idle_timeout")
				return
			}
			if readErr == io.EOF {
				finalize(store.UsageStatusOk)
				return
			}
			deps.Logger.Warn("上游流式中断", "requestId", requestID, "error", readErr)
			recordAttempt(target, attempt, attemptStarted, &resp.StatusCode, "upstream_stream_failure", providerRequestIDFrom(resp), true)
			failStreamWithUsage("upstream_stream_failure")
			reportFailure(target.ProviderID, "upstream_stream_failure")
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
