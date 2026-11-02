package gwapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/contracts"
	"github.com/vam12375/shucode-platform/server/internal/credits"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"github.com/vam12375/shucode-platform/server/internal/upstreamurl"
	"github.com/vam12375/shucode-platform/server/internal/webtools"
)

const (
	deepSeekSearchPath      = "/v1/tools/search"
	deepSeekFetchPath       = "/v1/tools/fetch"
	deepSeekSearchModel     = "deepseek:web-search"
	deepSeekFetchModel      = "deepseek:web-fetch"
	deepSeekToolCostMicro   = 5_000
	maxDeepSeekToolBody     = 1 << 20
	maxDeepSeekToolResponse = 8 << 20
)

type deepSeekSearchRequest struct {
	Query        string  `json:"query"`
	Count        *int64  `json:"count,omitempty"`
	OriQuestion  *string `json:"ori_question,omitempty"`
	Localization *string `json:"localization,omitempty"`
	NeedContent  bool    `json:"need_content,omitempty"`
}

type deepSeekSearchResponse struct {
	Results []json.RawMessage `json:"results"`
}

type deepSeekFetchRequest struct {
	URL string `json:"url"`
}

type deepSeekFetchResponse struct {
	Content *string `json:"content"`
}

// registerDeepSeekToolRoutes exposes the raw DeepSeek Search/Fetch APIs behind
// platform authentication. The tool responses remain DeepSeek-compatible so
// the client can use the same parser for direct and platform-routed calls.
func registerDeepSeekToolRoutes(app *gin.Engine, deps Deps) {
	app.GET(contracts.WebToolsCapabilitiesPath, func(c *gin.Context) {
		handleDeepSeekToolCapabilities(c, deps)
	})
	app.POST(deepSeekSearchPath, func(c *gin.Context) {
		handleDeepSeekTool(c, deps, "search")
	})
	app.POST(deepSeekFetchPath, func(c *gin.Context) {
		handleDeepSeekTool(c, deps, "fetch")
	})
}

// 工具错误码属于跨端契约，不能继承普通模型路由的 code:null。
func deepSeekToolError(c *gin.Context, status int, typ, message string) {
	code := typ
	if typ == "api_error" {
		code = "web_tools_unavailable"
	}
	c.JSON(status, gin.H{"error": gin.H{"message": message, "type": typ, "code": code}})
}

func resolveDeepSeekToolSnapshot(ctx context.Context, deps Deps) (webtools.Snapshot, error) {
	config, exists, err := webtools.Load(ctx, deps.Store)
	if err != nil {
		return webtools.Snapshot{}, err
	}
	if exists {
		// 已保存的停用或损坏配置必须显式生效，不能让旧环境变量重新启用能力。
		snapshot, err := webtools.Resolve(ctx, deps.Store, deps.SecretBox, config)
		if snapshot.TimeoutMs <= 0 {
			snapshot.TimeoutMs = deps.Env.UpstreamTimeoutMs
		}
		return snapshot, err
	}
	base, key := strings.TrimSpace(deps.Env.DeepSeekToolsBaseURL), strings.TrimSpace(deps.Env.DeepSeekToolsAPIKey)
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || key == "" {
		return webtools.Snapshot{}, webtools.ErrUnavailable
	}
	for _, char := range key {
		if char <= 32 || char > 126 {
			return webtools.Snapshot{}, webtools.ErrUnavailable
		}
	}
	for _, price := range []int64{deps.Env.DeepSeekSearchFeeMicro, deps.Env.DeepSeekFetchFeeMicro} {
		if price < 0 || price > webtools.MaxPriceMicro {
			return webtools.Snapshot{}, webtools.ErrInvalid
		}
	}
	return webtools.Snapshot{Config: webtools.Config{
		Version: "deepseek-tools-legacy-env-v1", Enabled: true, SearchEnabled: true, FetchEnabled: true,
		SearchSaleMicro: deps.Env.DeepSeekSearchFeeMicro, FetchSaleMicro: deps.Env.DeepSeekFetchFeeMicro,
		SearchCostMicro: deepSeekToolCostMicro, FetchCostMicro: deepSeekToolCostMicro,
	}, BaseURL: base, APIKey: key, TimeoutMs: deps.Env.UpstreamTimeoutMs}, nil
}

func handleDeepSeekToolCapabilities(c *gin.Context, deps Deps) {
	_, user, ok := authenticate(c.Request, deps, deepSeekToolError, c)
	if !ok {
		return
	}
	if _, _, ok = resolveGatewayTeam(c, deps, user.ID, deepSeekToolError); !ok {
		return
	}
	capabilities := contracts.WebToolsCapabilities{SchemaVersion: 1}
	snapshot, err := resolveDeepSeekToolSnapshot(c.Request.Context(), deps)
	if err == nil {
		capabilities.Enabled = snapshot.Enabled
		capabilities.SearchEnabled = snapshot.Enabled && snapshot.SearchEnabled
		capabilities.FetchEnabled = snapshot.Enabled && snapshot.FetchEnabled
		if capabilities.SearchEnabled {
			capabilities.SearchSaleMicro = &snapshot.SearchSaleMicro
		}
		if capabilities.FetchEnabled {
			capabilities.FetchSaleMicro = &snapshot.FetchSaleMicro
		}
	}
	c.JSON(http.StatusOK, capabilities)
}

func handleDeepSeekTool(c *gin.Context, deps Deps, kind string) {
	requestID := strings.TrimSpace(c.GetHeader("x-request-id"))
	if requestID == "" {
		requestID = uuid.NewString()
	}
	if len(requestID) > maxRequestIDLength {
		deepSeekToolError(c, 400, "invalid_request_error", "x-request-id 过长")
		return
	}
	c.Header("x-request-id", requestID)
	device, user, ok := authenticate(c.Request, deps, deepSeekToolError, c)
	if !ok {
		return
	}
	membership, policy, ok := resolveGatewayTeam(c, deps, user.ID, deepSeekToolError)
	if !ok {
		return
	}
	if err := deps.Store.TouchDeviceToken(c.Request.Context(), device.ID, deps.Now()); err != nil {
		deps.Logger.Warn("设备活跃时间更新失败", "error", err)
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxDeepSeekToolBody)
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		deepSeekToolError(c, 413, "invalid_request_error", "请求体超过 1 MiB 上限")
		return
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		deepSeekToolError(c, 400, "invalid_request_error", "请求体不是合法 JSON 对象")
		return
	}
	var upstreamBody []byte
	if kind == "search" {
		var input deepSeekSearchRequest
		if err := json.Unmarshal(raw, &input); err != nil || !validSearchRequest(input) {
			deepSeekToolError(c, 400, "invalid_request_error", "Search 请求参数无效")
			return
		}
		if input.Count == nil {
			value := int64(10)
			input.Count = &value
		}
		upstreamBody, _ = json.Marshal(input)
	} else {
		var input deepSeekFetchRequest
		if err := json.Unmarshal(raw, &input); err != nil || !validFetchRequest(input) {
			deepSeekToolError(c, 400, "invalid_request_error", "Fetch 请求参数无效")
			return
		}
		upstreamBody, _ = json.Marshal(input)
	}

	createdAt := deps.Now()
	snapshot, err := resolveDeepSeekToolSnapshot(c.Request.Context(), deps)
	if err != nil || (kind == "search" && !snapshot.SearchEnabled) || (kind == "fetch" && !snapshot.FetchEnabled) {
		deepSeekToolError(c, 503, "api_error", "DeepSeek 网页搜索与抓取不可用，请联系管理员检查工具配置与供应商")
		return
	}
	fee := snapshot.SearchSaleMicro
	if kind == "fetch" {
		fee = snapshot.FetchSaleMicro
	}
	release, allowed := acquireDeepSeekToolFlow(c, deps, effectiveTeamLimits(membership.Team, policy))
	if !allowed {
		return
	}
	defer release()
	reservationID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("deepseek-tool:"+membership.Team.ID+":"+user.ID+":"+requestID+":"+kind)).String()
	decision, err := deps.Store.AdmitUsage(c.Request.Context(), store.ReserveQuotaInput{
		ID: reservationID, TeamID: membership.Team.ID, UserID: user.ID,
		RequestID: requestID, Micro: fee, CreatedAt: createdAt,
		BillingBindingID: strings.TrimSpace(c.GetHeader("x-mendao-billing-binding")), DeviceTokenID: device.ID,
	})
	if err != nil {
		deps.Logger.Error("DeepSeek 工具额度准入失败", "error", err)
		deepSeekToolError(c, 503, "api_error", "额度服务不可用")
		return
	}
	switch decision {
	case store.ReserveDenied:
		deepSeekToolError(c, 402, "insufficient_quota", "额度不足")
		return
	case store.ReserveDeniedBudget:
		deepSeekToolError(c, 402, "insufficient_quota", "预算已用尽")
		return
	case store.ReserveDeniedInactive:
		deepSeekToolError(c, 403, "permission_error", "团队不可用")
		return
	case store.ReserveDuplicate:
		deepSeekToolError(c, 409, "conflict_error", "x-request-id 已使用")
		return
	case store.ReserveReserved:
		// continue
	default:
		deepSeekToolError(c, 503, "api_error", "额度服务返回未知状态")
		return
	}

	response, status, costReason, callErr := callDeepSeekTool(c.Request.Context(), deps, snapshot, kind, upstreamBody)
	if callErr != nil {
		if err := settleDeepSeekToolUsage(c, deps, snapshot, reservationID, requestID, membership.Team.ID, user.ID, device.ID, kind, createdAt, false, costReason, store.UsageStatusError, "upstream_error"); err != nil {
			deepSeekToolError(c, 503, "api_error", "用量结算暂不可用")
			return
		}
		if status == 0 {
			status = 502
		}
		deepSeekToolError(c, status, "upstream_error", "DeepSeek 工具请求失败")
		return
	}
	var validResult, validResponse bool
	if kind == "search" {
		var parsed deepSeekSearchResponse
		validResponse = json.Unmarshal(response, &parsed) == nil && parsed.Results != nil
		validResult = validResponse && len(parsed.Results) > 0
	} else {
		var parsed deepSeekFetchResponse
		// content 缺失/null 不是已确认的空正文，不能据此伪造零采购成本。
		validResponse = json.Unmarshal(response, &parsed) == nil && parsed.Content != nil
		validResult = validResponse && strings.TrimSpace(*parsed.Content) != ""
	}
	if !validResult {
		errorType := "empty_result"
		costReason = "tool_empty_result"
		if !validResponse {
			errorType = "invalid_response"
			costReason = "tool_outcome_unknown"
		}
		if err := settleDeepSeekToolUsage(c, deps, snapshot, reservationID, requestID, membership.Team.ID, user.ID, device.ID, kind, createdAt, false, costReason, store.UsageStatusError, errorType); err != nil {
			deepSeekToolError(c, 503, "api_error", "用量结算暂不可用")
			return
		}
		if !validResponse {
			deepSeekToolError(c, 502, "upstream_error", "DeepSeek 工具返回无效结果")
			return
		}
		c.Data(200, "application/json; charset=utf-8", response)
		return
	}
	if err := settleDeepSeekToolUsage(c, deps, snapshot, reservationID, requestID, membership.Team.ID, user.ID, device.ID, kind, createdAt, true, "", store.UsageStatusOk, ""); err != nil {
		deepSeekToolError(c, 503, "api_error", "用量结算暂不可用")
		return
	}
	c.Data(200, "application/json; charset=utf-8", response)
}

func settleDeepSeekToolUsage(c *gin.Context, deps Deps, snapshot webtools.Snapshot, reservationID, requestID, teamID, userID, deviceID, kind string, createdAt time.Time, billable bool, costReason string, status store.UsageStatus, errorType string) error {
	modelID, saleFee, costFee := deepSeekSearchModel, snapshot.SearchSaleMicro, snapshot.SearchCostMicro
	if kind == "fetch" {
		modelID, saleFee, costFee = deepSeekFetchModel, snapshot.FetchSaleMicro, snapshot.FetchCostMicro
	}
	fee, cost := int64(0), int64(0)
	// 免费售价仍会产生采购成本；成功事实不能由实扣金额推断。
	if billable {
		fee, cost = saleFee, costFee
	}
	requestPrice := func(micro int64) store.UsagePriceSnapshot {
		return store.UsagePriceSnapshot{Unit: "request", PerRequest: credits.FormatMicroCredits(micro),
			InputPerMtok: "0", OutputPerMtok: "0", CacheReadPerMtok: "0", CacheWritePerMtok: "0", Multiplier: "1", Currency: "CNY"}
	}
	costRate := requestPrice(costFee)
	costSnapshot := store.UsageCostSnapshot{Status: "estimated", PricingVersion: snapshot.Version, ModelID: modelID,
		Rates: &costRate, EstimatedMicro: &cost, Reason: costReason}
	// 超时、传输中断及无效响应不能确定供应商是否已计费，保留未知而不是伪造零成本。
	if costReason == "tool_outcome_unknown" {
		costSnapshot.Status, costSnapshot.EstimatedMicro = "unknown", nil
	}
	usage := store.UsageRecord{
		ID:        uuid.NewSHA1(uuid.NameSpaceOID, []byte("deepseek-tool-usage:"+reservationID)).String(),
		RequestID: requestID, UserID: userID, TeamID: teamID, BillingBindingID: strings.TrimSpace(c.GetHeader("x-mendao-billing-binding")), DeviceTokenID: deviceID,
		Model: modelID, UpstreamModel: "deepseek.com/" + kind, ProviderID: snapshot.ProviderID,
		CreditsMicro: fee, Status: status, ErrorType: errorType, CreatedAt: createdAt,
		PriceSnapshot: &store.UsagePricingSnapshot{SchemaVersion: 1, CapturedAt: createdAt,
			Sale: requestPrice(saleFee), Cost: costSnapshot},
	}
	if clientIP := clientIPOf(c, deps); clientIP != nil {
		usage.ClientIP = *clientIP
	}
	// 客户端取消不能撤销已收到的付费用量；结算使用独立有界上下文并保留补偿。
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Second)
	defer cancel()
	if err := deps.Store.SettleReservation(settleCtx, reservationID, usage); err != nil {
		fallbackCtx, cancelFallback := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Second)
		defer cancelFallback()
		if deps.SettlementFallback != nil {
			return deps.SettlementFallback.Persist(fallbackCtx, reservationID, usage)
		}
		return deps.Store.EnqueuePendingSettlement(fallbackCtx, reservationID, usage)
	}
	return nil
}

func acquireDeepSeekToolFlow(c *gin.Context, deps Deps, team store.TeamRecord) (func(), bool) {
	release := func() {}
	if deps.RateLimiter == nil {
		return release, true
	}
	ctx := c.Request.Context()
	if team.RPMLimit > 0 {
		allowed, err := deps.RateLimiter.AllowRPM(ctx, team.ID, team.RPMLimit)
		if err != nil {
			deps.Logger.Warn("RPM 限流不可用，按 fail-open 放行", "error", err)
		} else if !allowed {
			c.Header("retry-after", "60")
			deepSeekToolError(c, 429, "rate_limit_error", "团队请求频率超限（RPM），请稍后重试")
			return release, false
		}
	}
	// 按次工具不消耗模型 Token，只共享团队 RPM 和并发槽位。
	if team.ConcurrencyLimit > 0 {
		acquiredRelease, acquired, err := deps.RateLimiter.AcquireConcurrency(ctx, team.ID, team.ConcurrencyLimit)
		if err != nil {
			deps.Logger.Warn("并发限流不可用，按 fail-open 放行", "error", err)
		} else if !acquired {
			c.Header("retry-after", "1")
			deepSeekToolError(c, 429, "rate_limit_error", "团队并发请求超限，请稍后重试")
			return release, false
		} else {
			release = acquiredRelease
		}
	}
	return release, true
}

func validSearchRequest(input deepSeekSearchRequest) bool {
	if n := utf8.RuneCountInString(input.Query); n < 1 || n > 128 || strings.TrimSpace(input.Query) == "" {
		return false
	}
	if input.Count != nil && (*input.Count < 1 || *input.Count > 50) {
		return false
	}
	if input.OriQuestion != nil {
		n := utf8.RuneCountInString(strings.TrimSpace(*input.OriQuestion))
		if n < 1 || n > 128 {
			return false
		}
	}
	return true
}

func validFetchRequest(input deepSeekFetchRequest) bool {
	if len(input.URL) == 0 || len(input.URL) > 8192 {
		return false
	}
	parsed, err := url.Parse(input.URL)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil
}

func callDeepSeekTool(ctx context.Context, deps Deps, snapshot webtools.Snapshot, kind string, body []byte) ([]byte, int, string, error) {
	path := deepSeekSearchPath
	if kind == "fetch" {
		path = deepSeekFetchPath
	}
	timeout := snapshot.TimeoutMs
	if timeout <= 0 {
		timeout = 120_000
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, upstreamurl.Join(snapshot.BaseURL, path), strings.NewReader(string(body)))
	if err != nil {
		return nil, 0, "tool_upstream_rejected", err
	}
	req.Header.Set("authorization", "Bearer "+snapshot.APIKey)
	req.Header.Set("content-type", "application/json")
	client := deps.HTTPClient
	if client == nil {
		client = &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return fmt.Errorf("redirect blocked") }}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, "tool_outcome_unknown", err
	}
	defer resp.Body.Close()
	// 官方仅将 HTTP 200 的有结果请求计费，其它 2xx 不能视作成功。
	if resp.StatusCode != http.StatusOK {
		status := resp.StatusCode
		if status < 400 || status > 599 {
			status = 502
		}
		return nil, status, "tool_upstream_rejected", errors.New("deepseek_tool_upstream_error")
	}
	response, err := io.ReadAll(io.LimitReader(resp.Body, maxDeepSeekToolResponse+1))
	if err != nil || len(response) > maxDeepSeekToolResponse {
		return nil, 502, "tool_outcome_unknown", errors.New("deepseek_tool_response_invalid")
	}
	return response, 200, "", nil
}
