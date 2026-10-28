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

	"github.com/vam12375/shucode-platform/server/internal/credits"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"github.com/vam12375/shucode-platform/server/internal/upstreamurl"
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
	Title   string `json:"title"`
	Content string `json:"content"`
}

// registerDeepSeekToolRoutes exposes the raw DeepSeek Search/Fetch APIs behind
// platform authentication. The tool responses remain DeepSeek-compatible so
// the client can use the same parser for direct and platform-routed calls.
func registerDeepSeekToolRoutes(app *gin.Engine, deps Deps) {
	app.POST(deepSeekSearchPath, func(c *gin.Context) {
		handleDeepSeekTool(c, deps, "search")
	})
	app.POST(deepSeekFetchPath, func(c *gin.Context) {
		handleDeepSeekTool(c, deps, "fetch")
	})
}

func handleDeepSeekTool(c *gin.Context, deps Deps, kind string) {
	device, user, ok := authenticate(c.Request, deps, openAIError, c)
	if !ok {
		return
	}
	membership, _, ok := resolveGatewayTeam(c, deps, user.ID, openAIError)
	if !ok {
		return
	}
	if err := deps.Store.TouchDeviceToken(c.Request.Context(), device.ID, deps.Now()); err != nil {
		deps.Logger.Warn("设备活跃时间更新失败", "error", err)
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxDeepSeekToolBody)
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		openAIError(c, 413, "invalid_request_error", "请求体超过 1 MiB 上限")
		return
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		openAIError(c, 400, "invalid_request_error", "请求体不是合法 JSON 对象")
		return
	}
	var upstreamBody []byte
	if kind == "search" {
		var input deepSeekSearchRequest
		if err := json.Unmarshal(raw, &input); err != nil || !validSearchRequest(input) {
			openAIError(c, 400, "invalid_request_error", "Search 请求参数无效")
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
			openAIError(c, 400, "invalid_request_error", "Fetch 请求参数无效")
			return
		}
		upstreamBody, _ = json.Marshal(input)
	}

	fee := deps.Env.DeepSeekSearchFeeMicro
	if kind == "fetch" {
		fee = deps.Env.DeepSeekFetchFeeMicro
	}
	if fee <= 0 {
		openAIError(c, 503, "api_error", "DeepSeek 工具计费配置无效")
		return
	}
	if strings.TrimSpace(deps.Env.DeepSeekToolsBaseURL) == "" || strings.TrimSpace(deps.Env.DeepSeekToolsAPIKey) == "" {
		openAIError(c, 503, "api_error", "DeepSeek 网页搜索与抓取尚未配置")
		return
	}
	requestID := strings.TrimSpace(c.GetHeader("x-request-id"))
	if requestID == "" {
		requestID = uuid.NewString()
	}
	if len(requestID) > maxRequestIDLength {
		openAIError(c, 400, "invalid_request_error", "x-request-id 过长")
		return
	}
	reservationID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("deepseek-tool:"+membership.Team.ID+":"+user.ID+":"+requestID+":"+kind)).String()
	createdAt := deps.Now()
	decision, err := deps.Store.AdmitUsage(c.Request.Context(), store.ReserveQuotaInput{
		ID: reservationID, TeamID: membership.Team.ID, UserID: user.ID,
		RequestID: requestID, Micro: fee, CreatedAt: createdAt,
	})
	if err != nil {
		deps.Logger.Error("DeepSeek 工具额度准入失败", "error", err)
		openAIError(c, 503, "api_error", "额度服务不可用")
		return
	}
	switch decision {
	case store.ReserveDenied:
		openAIError(c, 402, "insufficient_quota", "额度不足")
		return
	case store.ReserveDeniedBudget:
		openAIError(c, 402, "insufficient_quota", "预算已用尽")
		return
	case store.ReserveDeniedInactive:
		openAIError(c, 403, "permission_error", "团队不可用")
		return
	case store.ReserveDuplicate:
		openAIError(c, 409, "conflict_error", "x-request-id 已使用")
		return
	case store.ReserveReserved:
		// continue
	default:
		openAIError(c, 503, "api_error", "额度服务返回未知状态")
		return
	}

	response, status, callErr := callDeepSeekTool(c.Request.Context(), deps, kind, upstreamBody)
	if callErr != nil {
		if err := settleDeepSeekToolUsage(c, deps, reservationID, requestID, membership.Team.ID, user.ID, device.ID, kind, createdAt, 0, store.UsageStatusError, "upstream_error"); err != nil {
			openAIError(c, 503, "api_error", "用量结算暂不可用")
			return
		}
		if status == 0 {
			status = 502
		}
		openAIError(c, status, "upstream_error", "DeepSeek 工具请求失败")
		return
	}
	var validResult, validResponse bool
	if kind == "search" {
		var parsed deepSeekSearchResponse
		validResponse = json.Unmarshal(response, &parsed) == nil && parsed.Results != nil
		validResult = validResponse && len(parsed.Results) > 0
	} else {
		var parsed deepSeekFetchResponse
		validResponse = json.Unmarshal(response, &parsed) == nil
		validResult = validResponse && strings.TrimSpace(parsed.Content) != ""
	}
	if !validResult {
		if err := settleDeepSeekToolUsage(c, deps, reservationID, requestID, membership.Team.ID, user.ID, device.ID, kind, createdAt, 0, store.UsageStatusError, "empty_result"); err != nil {
			openAIError(c, 503, "api_error", "用量结算暂不可用")
			return
		}
		if !validResponse {
			openAIError(c, 502, "upstream_error", "DeepSeek 工具返回无效结果")
			return
		}
		c.Data(200, "application/json; charset=utf-8", response)
		return
	}
	if err := settleDeepSeekToolUsage(c, deps, reservationID, requestID, membership.Team.ID, user.ID, device.ID, kind, createdAt, fee, store.UsageStatusOk, ""); err != nil {
		openAIError(c, 503, "api_error", "用量结算暂不可用")
		return
	}
	c.Data(200, "application/json; charset=utf-8", response)
}

func settleDeepSeekToolUsage(c *gin.Context, deps Deps, reservationID, requestID, teamID, userID, deviceID, kind string, createdAt time.Time, fee int64, status store.UsageStatus, errorType string) error {
	modelID, saleFee := deepSeekSearchModel, deps.Env.DeepSeekSearchFeeMicro
	if kind == "fetch" {
		modelID, saleFee = deepSeekFetchModel, deps.Env.DeepSeekFetchFeeMicro
	}
	cost := int64(0)
	if fee > 0 {
		cost = deepSeekToolCostMicro
	}
	requestPrice := func(micro int64) store.UsagePriceSnapshot {
		return store.UsagePriceSnapshot{Unit: "request", PerRequest: credits.FormatMicroCredits(micro),
			InputPerMtok: "0", OutputPerMtok: "0", CacheReadPerMtok: "0", CacheWritePerMtok: "0", Multiplier: "1", Currency: "CNY"}
	}
	costRate := requestPrice(deepSeekToolCostMicro)
	usage := store.UsageRecord{
		ID:        uuid.NewSHA1(uuid.NameSpaceOID, []byte("deepseek-tool-usage:"+reservationID)).String(),
		RequestID: requestID, UserID: userID, TeamID: teamID, DeviceTokenID: deviceID,
		Model: modelID, UpstreamModel: "deepseek.com/" + kind,
		CreditsMicro: fee, Status: status, ErrorType: errorType, CreatedAt: createdAt,
		PriceSnapshot: &store.UsagePricingSnapshot{SchemaVersion: 1, CapturedAt: createdAt,
			Sale: requestPrice(saleFee), Cost: store.UsageCostSnapshot{Status: "estimated", PricingVersion: "deepseek-tools-2026-10-28", ModelID: modelID,
				Rates: &costRate, EstimatedMicro: &cost}},
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

func callDeepSeekTool(ctx context.Context, deps Deps, kind string, body []byte) ([]byte, int, error) {
	base := strings.TrimRight(deps.Env.DeepSeekToolsBaseURL, "/")
	if base == "" || deps.Env.DeepSeekToolsAPIKey == "" {
		return nil, 503, errors.New("deepseek_tools_not_configured")
	}
	path := deepSeekSearchPath
	if kind == "fetch" {
		path = deepSeekFetchPath
	}
	timeout := deps.Env.UpstreamTimeoutMs
	if timeout <= 0 {
		timeout = 120_000
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, upstreamurl.Join(base, path), strings.NewReader(string(body)))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("authorization", "Bearer "+deps.Env.DeepSeekToolsAPIKey)
	req.Header.Set("content-type", "application/json")
	client := deps.HTTPClient
	if client == nil {
		client = &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return fmt.Errorf("redirect blocked") }}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	response, err := io.ReadAll(io.LimitReader(resp.Body, maxDeepSeekToolResponse+1))
	if err != nil || len(response) > maxDeepSeekToolResponse {
		return nil, 502, errors.New("deepseek_tool_response_invalid")
	}
	// 官方仅将 HTTP 200 的有结果请求计费，其它 2xx 不能视作成功。
	if resp.StatusCode != http.StatusOK {
		status := resp.StatusCode
		if status < 400 || status > 599 {
			status = 502
		}
		return nil, status, errors.New("deepseek_tool_upstream_error")
	}
	return response, 200, nil
}
