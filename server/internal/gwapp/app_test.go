package gwapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/ratelimit"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

type gatewayFixture struct {
	t        *testing.T
	store    store.PlatformStore
	engine   *gin.Engine
	env      *Env
	upstream *httptest.Server

	mu       sync.Mutex
	handler  http.HandlerFunc
	lastReq  *http.Request
	lastBody []byte

	token  string
	userID string
	teamID string

	settlementFallback SettlementFallback
	attemptRecorder    AttemptRecorder
}

func newGatewayFixture(t *testing.T, patch func(env *Env, f *gatewayFixture)) *gatewayFixture {
	t.Helper()
	ctx := context.Background()
	mem, err := store.NewMemoryStore(ctx, store.MemoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f := &gatewayFixture{t: t, store: mem}
	f.handler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}

	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.lastReq = r.Clone(context.Background())
		f.lastBody = body
		handler := f.handler
		f.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(f.upstream.Close)

	user, err := mem.CreateUser(ctx, store.UserInput{
		Email: "gw@test.local", PasswordHash: "x", DisplayName: "网关用户", Role: store.RoleUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	team, err := mem.CreateTeam(ctx, store.CreateTeamInput{Name: "网关团队", QuotaCredits: "100.000000"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.AddMember(ctx, team.ID, user.ID, store.MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	f.token = crypto.GenerateDeviceToken()
	if err := mem.CreateDeviceToken(ctx, store.DeviceTokenRecord{
		ID: uuid.NewString(), UserID: user.ID, Name: "测试设备",
		TokenHash: crypto.HashToken(f.token), TokenCiphertext: "v1.a.b.c", TokenHint: "abcd",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.UpsertModel(ctx, store.UpsertModelInput{
		ModelID: "claude-sonnet-4-5", DisplayName: "Claude Sonnet 4.5",
		InputPricePerMtok: "3", OutputPricePerMtok: "15",
		CacheReadPricePerMtok: ptr("0.3"), CacheWritePricePerMtok: ptr("3.75"),
	}); err != nil {
		t.Fatal(err)
	}
	f.userID, f.teamID = user.ID, team.ID

	f.env = &Env{
		Port: 0, Host: "127.0.0.1",
		TrustedProxyIPs: []string{"10.0.0.2"},
		UpstreamBaseURL: f.upstream.URL, UpstreamAPIKey: "upstream-key",
		AnthropicVersion:  "2023-06-01",
		UpstreamTimeoutMs: 5_000, UpstreamIdleTimeoutMs: 1_000,
		ProviderAutoDisableThreshold: 3,
	}
	if patch != nil {
		patch(f.env, f)
	}
	f.engine = New(Deps{
		Store: f.store, Env: f.env, Now: time.Now,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		HTTPClient: &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return fmt.Errorf("redirect blocked")
		}},
		Trusted:            NewTrustedProxies(f.env.TrustedProxyIPs),
		RateLimiter:        ratelimit.NewGatewayLimiter(nil),
		SettlementFallback: f.settlementFallback,
		AttemptRecorder:    f.attemptRecorder,
	})
	return f
}

func TestTrailingSlashNotRedirected(t *testing.T) {
	f := newGatewayFixture(t, nil)
	for _, path := range []string{"/healthz/", "/v1/models/"} {
		rec := httptest.NewRecorder()
		f.engine.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 404 {
			t.Fatalf("%s 应 404(不跟随尾斜杠重定向): %d", path, rec.Code)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func (f *gatewayFixture) setHandler(handler http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handler = handler
}

func (f *gatewayFixture) upstreamRequest() (*http.Request, []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastReq, f.lastBody
}

func (f *gatewayFixture) do(method, path, token string, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("content-type", "application/json")
	}
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	return rec
}

func (f *gatewayFixture) usageRecords() []store.UsageRecord {
	f.t.Helper()
	records, err := f.store.ListUsage(context.Background(), store.UsageFilter{TeamID: f.teamID})
	if err != nil {
		f.t.Fatal(err)
	}
	return records
}

func (f *gatewayFixture) team() *store.TeamRecord {
	f.t.Helper()
	team, err := f.store.GetTeam(context.Background(), f.teamID)
	if err != nil {
		f.t.Fatal(err)
	}
	return team
}

func assertAnthropicError(t *testing.T, rec *httptest.ResponseRecorder, status int, typ string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("状态码: got %d want %d, body=%s", rec.Code, status, rec.Body.String())
	}
	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("错误体不是 JSON: %s", rec.Body.String())
	}
	if body.Type != "error" || body.Error.Type != typ || body.Error.Message == "" {
		t.Fatalf("错误体形状不符: %s", rec.Body.String())
	}
}

func assertOpenAIError(t *testing.T, rec *httptest.ResponseRecorder, status int, typ string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("状态码: got %d want %d, body=%s", rec.Code, status, rec.Body.String())
	}
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("错误体不是 JSON: %s", rec.Body.String())
	}
	if body.Error.Type != typ || body.Error.Message == "" {
		t.Fatalf("错误体形状不符: %s", rec.Body.String())
	}
}

func TestHealthz(t *testing.T) {
	f := newGatewayFixture(t, nil)
	rec := f.do("GET", "/healthz", "", "")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("healthz: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAuthFailures(t *testing.T) {
	f := newGatewayFixture(t, nil)
	// 缺凭据(anthropic 错误形状)。
	assertAnthropicError(t, f.do("POST", "/v1/messages", "", `{}`), 401, "authentication_error")
	// 无效凭据。
	assertAnthropicError(t, f.do("POST", "/v1/messages", "mdp_invalid", `{}`), 401, "authentication_error")
	// openai 路由错误形状。
	rec := f.do("POST", "/v1/chat/completions", "", `{}`)
	if rec.Code != 401 {
		t.Fatalf("openai 路由缺凭据应 401: %d", rec.Code)
	}
	var openaiBody struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &openaiBody); err != nil || openaiBody.Error.Type != "authentication_error" {
		t.Fatalf("openai 错误形状不符: %s", rec.Body.String())
	}
	// 已吊销凭据。
	ctx := context.Background()
	device, _ := f.store.FindDeviceTokenByHash(ctx, crypto.HashToken(f.token))
	if err := f.store.RevokeDeviceToken(ctx, device.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	assertAnthropicError(t, f.do("POST", "/v1/messages", f.token, `{}`), 401, "authentication_error")
	// 账号禁用。
	f2 := newGatewayFixture(t, nil)
	// 全局停用也受最后管理员保护；先保留另一位团队管理员，再验证网关拒绝禁用账号。
	otherAdmin, err := f2.store.CreateUser(ctx, store.UserInput{Email: "other-admin@test.local", PasswordHash: "x", DisplayName: "A", Role: store.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if err := f2.store.AddMember(ctx, f2.teamID, otherAdmin.ID, store.MemberRoleAdmin); err != nil {
		t.Fatal(err)
	}
	disabledAt := time.Now()
	if _, err := f2.store.UpdateUser(context.Background(), f2.userID, store.UpdateUserPatch{HasDisabled: true, DisabledAt: &disabledAt}); err != nil {
		t.Fatal(err)
	}
	assertAnthropicError(t, f2.do("POST", "/v1/messages", f2.token, `{}`), 403, "permission_error")
	// 无团队。
	f3 := newGatewayFixture(t, nil)
	lonely, _ := f3.store.CreateUser(context.Background(), store.UserInput{
		Email: "lonely@test.local", PasswordHash: "x", DisplayName: "L", Role: store.RoleUser,
	})
	lonelyToken := crypto.GenerateDeviceToken()
	_ = f3.store.CreateDeviceToken(context.Background(), store.DeviceTokenRecord{
		ID: uuid.NewString(), UserID: lonely.ID, Name: "d",
		TokenHash: crypto.HashToken(lonelyToken), TokenCiphertext: "x", TokenHint: "0000", CreatedAt: time.Now(),
	})
	assertAnthropicError(t, f3.do("POST", "/v1/messages", lonelyToken, `{}`), 403, "permission_error")
}

func TestModelValidation(t *testing.T) {
	f := newGatewayFixture(t, nil)
	// 缺 model 字段。
	assertAnthropicError(t, f.do("POST", "/v1/messages", f.token, `{}`), 400, "invalid_request_error")
	// 非法 JSON / 非对象。
	assertAnthropicError(t, f.do("POST", "/v1/messages", f.token, `{oops`), 400, "invalid_request_error")
	assertAnthropicError(t, f.do("POST", "/v1/messages", f.token, `[1,2]`), 400, "invalid_request_error")
	// 未知模型。
	assertAnthropicError(t, f.do("POST", "/v1/messages", f.token, `{"model":"nope"}`), 404, "not_found_error")
	// 停用模型。
	disabled := false
	if _, err := f.store.UpsertModel(context.Background(), store.UpsertModelInput{
		ModelID: "claude-sonnet-4-5", DisplayName: "x", InputPricePerMtok: "3",
		OutputPricePerMtok: "15", Enabled: &disabled,
	}); err != nil {
		t.Fatal(err)
	}
	assertAnthropicError(t, f.do("POST", "/v1/messages", f.token, `{"model":"claude-sonnet-4-5"}`), 404, "not_found_error")
}

func TestQuotaExhausted(t *testing.T) {
	f := newGatewayFixture(t, nil)
	zero := "0.000000"
	if _, err := f.store.UpdateTeam(context.Background(), f.teamID, store.UpdateTeamPatch{QuotaCredits: &zero}); err != nil {
		t.Fatal(err)
	}
	rec := f.do("POST", "/v1/messages", f.token, `{"model":"claude-sonnet-4-5"}`)
	assertAnthropicError(t, rec, 429, "rate_limit_error")
	if req, _ := f.upstreamRequest(); req != nil {
		t.Fatal("额度不足不应请求上游")
	}
	if len(f.usageRecords()) != 0 {
		t.Fatal("额度不足不应写 usage")
	}
}

func TestNonStreamSuccess(t *testing.T) {
	f := newGatewayFixture(t, nil)
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":20,"cache_creation_input_tokens":5}}`))
	})
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":8192,"stream":false}`))
	req.Header.Set("authorization", "Bearer "+f.token)
	req.Header.Set("x-request-id", "req-abc")
	req.Header.Set("x-forwarded-for", "203.0.113.9")
	req.Header.Set("anthropic-beta", "beta-1")
	rec := httptest.NewRecorder()
	// 让 TCP 对端成为可信代理,验证 XFF 采集。
	req.RemoteAddr = "10.0.0.2:12345"
	engine := f.engine
	engine.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"msg_1"`) {
		t.Fatalf("透传失败: %d %s", rec.Code, rec.Body.String())
	}
	upReq, upBody := f.upstreamRequest()
	if upReq == nil {
		t.Fatal("上游未被调用")
	}
	if upReq.Header.Get("x-api-key") != "upstream-key" {
		t.Fatalf("上游鉴权头应为平台 key: %q", upReq.Header.Get("x-api-key"))
	}
	if upReq.Header.Get("authorization") != "" {
		t.Fatal("客户端的平台凭据不得外发")
	}
	if upReq.Header.Get("anthropic-beta") != "beta-1" {
		t.Fatal("anthropic-beta 应透传")
	}
	var sentBody map[string]any
	_ = json.Unmarshal(upBody, &sentBody)
	if sentBody["model"] != "claude-sonnet-4-5" {
		t.Fatalf("上游 model 不符: %v", sentBody["model"])
	}
	// 结算:100*3 + 50*15 + 20*0.3 + 5*3.75 = 300+750+6+18.75 = 1074.75 micro → 1075
	records := f.usageRecords()
	if len(records) != 1 {
		t.Fatalf("应有一条 usage: %d", len(records))
	}
	record := records[0]
	if record.RequestID != "req-abc" || record.Status != store.UsageStatusOk ||
		record.InputTokens != 100 || record.OutputTokens != 50 ||
		record.CacheReadTokens != 20 || record.CacheCreationTokens != 5 ||
		record.CreditsMicro != 1075 || record.ClientIP != "203.0.113.9" ||
		record.LatencyMs == nil {
		t.Fatalf("usage 记录不符: %+v", record)
	}
	if team := f.team(); team.UsedCredits != "0.001075" || team.ReservedCredits != "0.000000" {
		t.Fatalf("额度结算不符: used=%s reserved=%s", team.UsedCredits, team.ReservedCredits)
	}
}

func TestOpenAIStreamForcesUsageOption(t *testing.T) {
	f := newGatewayFixture(t, nil)
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	rec := f.do("POST", "/v1/chat/completions", f.token,
		`{"model":"claude-sonnet-4-5","stream":true,"stream_options":{"include_usage":false}}`)
	if rec.Code != 200 {
		t.Fatalf("流式请求失败: %d %s", rec.Code, rec.Body.String())
	}
	_, upBody := f.upstreamRequest()
	var sent map[string]any
	if err := json.Unmarshal(upBody, &sent); err != nil {
		t.Fatal(err)
	}
	options, ok := sent["stream_options"].(map[string]any)
	if !ok || options["include_usage"] != true {
		t.Fatalf("网关必须强制 include_usage=true: %v", sent["stream_options"])
	}
}

func TestMissingUsageDoesNotChargeEstimate(t *testing.T) {
	f := newGatewayFixture(t, nil)
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	})
	rec := f.do("POST", "/v1/chat/completions", f.token,
		`{"model":"claude-sonnet-4-5","max_tokens":100}`)
	if rec.Code != 200 {
		t.Fatalf("非流式请求失败: %d %s", rec.Code, rec.Body.String())
	}
	records := f.usageRecords()
	if len(records) != 1 {
		t.Fatalf("应写入一条 usage: %d", len(records))
	}
	record := records[0]
	if record.Status != store.UsageStatusError || record.ErrorType != "usage_missing" ||
		record.CreditsMicro != 0 {
		t.Fatalf("缺少 usage 不得按输出上限估算扣款: %+v", record)
	}
}

func TestStreamingMissingUsageDoesNotChargeEstimate(t *testing.T) {
	f := newGatewayFixture(t, nil)
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	rec := f.do("POST", "/v1/chat/completions", f.token,
		`{"model":"claude-sonnet-4-5","stream":true,"max_tokens":100}`)
	if rec.Code != 200 {
		t.Fatalf("流式请求失败: %d %s", rec.Code, rec.Body.String())
	}
	records := f.usageRecords()
	if len(records) != 1 {
		t.Fatalf("应写入一条 usage: %d", len(records))
	}
	record := records[0]
	if record.ErrorType != "usage_missing" || record.CreditsMicro != 0 {
		t.Fatalf("流式缺少 usage 不得估算扣款: %+v", record)
	}
}

func TestStreamingUsageBeyondEstimateCompletesAndChargesActual(t *testing.T) {
	f := newGatewayFixture(t, nil)
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"too much\"}}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":100}}\n\ndata: [DONE]\n\n"))
	})
	rec := f.do("POST", "/v1/chat/completions", f.token,
		`{"model":"claude-sonnet-4-5","stream":true,"max_tokens":1}`)
	if rec.Code != 200 {
		t.Fatalf("流式请求状态: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "rate_limit_error") || !strings.Contains(rec.Body.String(), "too much") {
		t.Fatalf("已准入请求不能按估算费用截断: %s", rec.Body.String())
	}
	records := f.usageRecords()
	if len(records) != 1 || records[0].Status != store.UsageStatusOk || records[0].CreditsMicro != 1560 {
		t.Fatalf("完整真实费用必须进入用量账本: %+v", records)
	}
}

func TestUpstreamErrorRedactsAPIKey(t *testing.T) {
	f := newGatewayFixture(t, nil)
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":"upstream-key leaked"}`))
	})
	rec := f.do("POST", "/v1/messages", f.token, `{"model":"claude-sonnet-4-5"}`)
	if rec.Code != 500 {
		t.Fatalf("上游错误状态: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "upstream-key") ||
		!strings.Contains(rec.Body.String(), "[redacted]") {
		t.Fatalf("错误正文必须脱敏密钥: %s", rec.Body.String())
	}
}

func TestUpstreamErrorPassthroughAndRelease(t *testing.T) {
	f := newGatewayFixture(t, nil)
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	})
	rec := f.do("POST", "/v1/messages", f.token, `{"model":"claude-sonnet-4-5"}`)
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("上游错误应原样透传: %d %s", rec.Code, rec.Body.String())
	}
	records := f.usageRecords()
	if len(records) != 1 || records[0].Status != store.UsageStatusError || records[0].ErrorType != "upstream_500" {
		t.Fatalf("失败 usage 不符: %+v", records)
	}
	if team := f.team(); team.ReservedCredits != "0.000000" || team.UsedCredits != "0.000000" {
		t.Fatalf("失败应释放预留: reserved=%s used=%s", team.ReservedCredits, team.UsedCredits)
	}
}

func TestUpstreamTimeout(t *testing.T) {
	f := newGatewayFixture(t, func(env *Env, _ *gatewayFixture) {
		env.UpstreamTimeoutMs = 50
	})
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})
	rec := f.do("POST", "/v1/messages", f.token, `{"model":"claude-sonnet-4-5"}`)
	assertAnthropicError(t, rec, 504, "api_error")
	records := f.usageRecords()
	if len(records) != 1 || records[0].ErrorType != "upstream_timeout" {
		t.Fatalf("超时 usage 不符: %+v", records)
	}
	if team := f.team(); team.ReservedCredits != "0.000000" {
		t.Fatal("超时应释放预留")
	}
}

func TestUpstreamRedirectRejected(t *testing.T) {
	f := newGatewayFixture(t, nil)
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("location", "/elsewhere")
		w.WriteHeader(302)
	})
	rec := f.do("POST", "/v1/messages", f.token, `{"model":"claude-sonnet-4-5"}`)
	assertAnthropicError(t, rec, 502, "api_error")
	records := f.usageRecords()
	if len(records) != 1 || records[0].ErrorType != "upstream_unavailable" {
		t.Fatalf("重定向应按连接失败记录: %+v", records)
	}
}

func TestFuseCounting(t *testing.T) {
	ctx := context.Background()
	f := newGatewayFixture(t, nil)
	provider, err := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
		Name: "fuse-provider", BaseURL: f.upstream.URL, Protocol: store.ProtocolAnthropicMessages,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpsertModel(ctx, store.UpsertModelInput{
		ModelID: "claude-sonnet-4-5", DisplayName: "x", InputPricePerMtok: "3", OutputPricePerMtok: "15",
		ProvideUpstreamProviderID: true, UpstreamProviderID: provider.ID,
	}); err != nil {
		t.Fatal(err)
	}
	// 429(限流)不计入熔断。
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":"rate"}`))
	})
	for i := 0; i < 2; i++ {
		f.do("POST", "/v1/messages", f.token, `{"model":"claude-sonnet-4-5"}`)
	}
	got, _ := f.store.GetUpstreamProvider(ctx, provider.ID)
	if got.ConsecutiveFailures != 0 || !got.Enabled {
		t.Fatalf("429 不应计入熔断: %+v", got)
	}
	// 500 计入:连续 3 次自动停用。
	f.mu.Lock()
	f.handler = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}
	f.mu.Unlock()
	for i := 0; i < 3; i++ {
		f.do("POST", "/v1/messages", f.token, `{"model":"claude-sonnet-4-5"}`)
	}
	got, _ = f.store.GetUpstreamProvider(ctx, provider.ID)
	if got.Enabled || got.DisabledMode != "auto" || got.AutoDisabledReason != "upstream_500" {
		t.Fatalf("熔断现场不符: %+v", got)
	}
	// 熔断后请求被拒(供应商不可用,404)。
	assertAnthropicError(t, f.do("POST", "/v1/messages", f.token, `{"model":"claude-sonnet-4-5"}`), 404, "not_found_error")
}

func TestProviderBindingAndKey(t *testing.T) {
	ctx := context.Background()
	key := "qv3YmS8XKg0SdoLJc1TzW7tQe4pXbUcHnMhR9IyAL2k="
	box, err := crypto.NewSecretBox(key)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := box.Encrypt("provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	f := newGatewayFixture(t, func(env *Env, f *gatewayFixture) {
		env.PlatformSecretKey = key
	})
	// fixture 的 engine 在 patch 之后构建;补 SecretBox 需要重建 engine。
	secretBox, _ := crypto.NewSecretBox(key)
	f.engine = New(Deps{
		Store: f.store, Env: f.env, Now: time.Now,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		HTTPClient: &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return fmt.Errorf("redirect blocked")
		}},
		Trusted: NewTrustedProxies(f.env.TrustedProxyIPs), SecretBox: secretBox,
	})
	provider, err := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
		Name: "bound-provider", BaseURL: f.upstream.URL, Protocol: store.ProtocolAnthropicMessages,
		APIKeyCiphertext: ciphertext, APIKeyHint: "cret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpsertModel(ctx, store.UpsertModelInput{
		ModelID: "claude-sonnet-4-5", DisplayName: "x", InputPricePerMtok: "3", OutputPricePerMtok: "15",
		ProvideUpstreamProviderID: true, UpstreamProviderID: provider.ID,
		ProvideUpstreamModel: true, UpstreamModel: "real-upstream-model",
	}); err != nil {
		t.Fatal(err)
	}
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"input_tokens":1,"output_tokens":1}}`))
	})
	rec := f.do("POST", "/v1/messages", f.token, `{"model":"claude-sonnet-4-5"}`)
	if rec.Code != 200 {
		t.Fatalf("绑定供应商转发失败: %d %s", rec.Code, rec.Body.String())
	}
	upReq, upBody := f.upstreamRequest()
	if upReq.Header.Get("x-api-key") != "provider-secret" {
		t.Fatalf("应使用供应商解密后的 key: %q", upReq.Header.Get("x-api-key"))
	}
	var sent map[string]any
	_ = json.Unmarshal(upBody, &sent)
	if sent["model"] != "real-upstream-model" {
		t.Fatalf("upstreamModel 映射失败: %v", sent["model"])
	}
	records := f.usageRecords()
	if len(records) != 1 || records[0].ProviderID != provider.ID {
		t.Fatalf("usage 应带供应商维度: %+v", records)
	}
	// 无 PLATFORM_SECRET_KEY 时绑定密文供应商 → 500。
	f2 := newGatewayFixture(t, nil)
	provider2, _ := f2.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
		Name: "no-key-provider", BaseURL: f2.upstream.URL, Protocol: store.ProtocolAnthropicMessages,
		APIKeyCiphertext: ciphertext,
	})
	_, _ = f2.store.UpsertModel(ctx, store.UpsertModelInput{
		ModelID: "claude-sonnet-4-5", DisplayName: "x", InputPricePerMtok: "3", OutputPricePerMtok: "15",
		ProvideUpstreamProviderID: true, UpstreamProviderID: provider2.ID,
	})
	assertAnthropicError(t, f2.do("POST", "/v1/messages", f2.token, `{"model":"claude-sonnet-4-5"}`), 500, "api_error")
}

func TestProviderProtocolMismatch(t *testing.T) {
	ctx := context.Background()
	f := newGatewayFixture(t, nil)
	provider, _ := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
		Name: "anthropic-provider", BaseURL: f.upstream.URL, Protocol: store.ProtocolAnthropicMessages,
	})
	_, _ = f.store.UpsertModel(ctx, store.UpsertModelInput{
		ModelID: "claude-sonnet-4-5", DisplayName: "x", InputPricePerMtok: "3", OutputPricePerMtok: "15",
		ProvideUpstreamProviderID: true, UpstreamProviderID: provider.ID,
	})
	assertOpenAIError(t, f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5"}`), 404, "not_found_error")
	chatProvider, _ := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
		Name: "chat-provider", BaseURL: f.upstream.URL, Protocol: store.ProtocolOpenAIChatCompletions,
	})
	_, _ = f.store.UpsertModel(ctx, store.UpsertModelInput{
		ModelID: "claude-sonnet-4-5", DisplayName: "x", InputPricePerMtok: "3", OutputPricePerMtok: "15",
		ProvideUpstreamProviderID: true, UpstreamProviderID: chatProvider.ID,
	})
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(openAISuccessBody))
	})
	if rec := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5"}`); rec.Code != 200 {
		t.Fatalf("chat 供应商转发失败: %d %s", rec.Code, rec.Body.String())
	}
	assertOpenAIError(t, f.do("POST", "/v1/responses", f.token, `{"model":"claude-sonnet-4-5"}`), 404, "not_found_error")
	// Responses 专用供应商不接受 chat，Chat 供应商也不能承接 Responses。
	responsesProvider, _ := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
		Name: "responses-provider", BaseURL: f.upstream.URL, Protocol: store.ProtocolOpenAIResponses,
	})
	_, _ = f.store.UpsertModel(ctx, store.UpsertModelInput{
		ModelID: "claude-sonnet-4-5", DisplayName: "x", InputPricePerMtok: "3", OutputPricePerMtok: "15",
		ProvideUpstreamProviderID: true, UpstreamProviderID: responsesProvider.ID,
	})
	assertOpenAIError(t, f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5"}`), 404, "not_found_error")
	// 该供应商走 /v1/responses 可以正常转发。
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"input_tokens":1,"output_tokens":1}}`))
	})
	if rec := f.do("POST", "/v1/responses", f.token, `{"model":"claude-sonnet-4-5"}`); rec.Code != 200 {
		t.Fatalf("responses 供应商转发失败: %d %s", rec.Code, rec.Body.String())
	}
}

func TestModelsList(t *testing.T) {
	f := newGatewayFixture(t, nil)
	rec := f.do("GET", "/v1/models", f.token, "")
	if rec.Code != 200 {
		t.Fatalf("models 列表失败: %d", rec.Code)
	}
	var body struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			Owner   string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Object != "list" || len(body.Data) != 1 || body.Data[0].ID != "claude-sonnet-4-5" ||
		body.Data[0].Object != "model" || body.Data[0].Owner != "mendao-platform" || body.Data[0].Created <= 0 {
		t.Fatalf("models 形状不符: %s", rec.Body.String())
	}
}

func TestStreamingSuccessAndUsage(t *testing.T) {
	f := newGatewayFixture(t, nil)
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		flusher := w.(http.Flusher)
		frames := []string{
			"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n",
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":50,\"prompt_tokens_details\":{\"cached_tokens\":40}}}\n\n",
			"data: [DONE]\n\n",
		}
		for _, frame := range frames {
			_, _ = w.Write([]byte(frame))
			flusher.Flush()
		}
	})
	rec := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5","stream":true}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"content":"hi"`) {
		t.Fatalf("流式透传失败: %d %s", rec.Code, rec.Body.String())
	}
	// 注入 include_usage。
	_, upBody := f.upstreamRequest()
	if !strings.Contains(string(upBody), `"include_usage":true`) {
		t.Fatalf("流式应注入 include_usage: %s", upBody)
	}
	records := f.usageRecords()
	if len(records) != 1 || records[0].Status != store.UsageStatusOk {
		t.Fatalf("流式结算失败: %+v", records)
	}
	if records[0].InputTokens != 60 || records[0].OutputTokens != 50 || records[0].CacheReadTokens != 40 {
		t.Fatalf("流式 usage 不符: %+v", records[0])
	}
}

type settleContextStore struct {
	store.PlatformStore
	mu        sync.Mutex
	settleErr error
}

func (s *settleContextStore) SettleReservation(ctx context.Context, reservationID string, record store.UsageRecord) error {
	if err := ctx.Err(); err != nil {
		s.mu.Lock()
		s.settleErr = err
		s.mu.Unlock()
	}
	return s.PlatformStore.SettleReservation(ctx, reservationID, record)
}

func (s *settleContextStore) contextError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settleErr
}

func TestStreamingClientAbortSettlement(t *testing.T) {
	tests := []struct {
		name             string
		usageEvent       string
		wantInput        int64
		wantOutput       int64
		wantCreditsMicro int64
	}{
		{name: "settles observed usage without a terminal frame", usageEvent: "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":40}}\n\n", wantInput: 100, wantOutput: 40, wantCreditsMicro: 900},
		{name: "closes without usage", wantCreditsMicro: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checker := &settleContextStore{}
			f := newGatewayFixture(t, func(_ *Env, f *gatewayFixture) {
				checker.PlatformStore = f.store
				f.store = checker
			})
			otherReservationID := uuid.NewString()
			result, err := f.store.AdmitUsage(context.Background(), store.ReserveQuotaInput{
				ID: otherReservationID, TeamID: f.teamID, UserID: f.userID, RequestID: "other-" + otherReservationID,
				Micro: 0, TTL: time.Minute, CreatedAt: time.Now(),
			})
			if err != nil || result != store.ReserveReserved {
				t.Fatalf("create concurrent zero-balance identity: result=%s error=%v", result, err)
			}
			f.setHandler(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("content-type", "text/event-stream")
				flusher := w.(http.Flusher)
				if tc.usageEvent != "" {
					_, _ = w.Write([]byte(tc.usageEvent))
					flusher.Flush()
				}
				for i := 0; i < 100; i++ {
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"))
					flusher.Flush()
					select {
					case <-time.After(20 * time.Millisecond):
					case <-r.Context().Done():
						return
					}
				}
			})
			server := httptest.NewServer(f.engine)
			defer server.Close()

			ctx, cancel := context.WithCancel(context.Background())
			req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/chat/completions",
				bytes.NewReader([]byte(`{"model":"claude-sonnet-4-5","stream":true}`)))
			req.Header.Set("authorization", "Bearer "+f.token)
			req.Header.Set("content-type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 128)
			_, _ = resp.Body.Read(buf)
			cancel()
			_ = resp.Body.Close()

			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				records := f.usageRecords()
				if len(records) == 1 {
					record := records[0]
					if record.Status != store.UsageStatusAborted || record.ErrorType != "client_aborted" {
						t.Fatalf("中断应记录 aborted: %+v", record)
					}
					if record.InputTokens != tc.wantInput || record.OutputTokens != tc.wantOutput {
						t.Fatalf("中断应只记录合法已观察用量: %+v", record)
					}
					if record.CreditsMicro != tc.wantCreditsMicro {
						t.Fatalf("中断实扣与已观察 usage 不符: %+v", record)
					}
					if team := f.team(); team.ReservedCredits != "0.000000" {
						t.Fatalf("中断后没有冻结，不能终结其他并发请求: %s", team.ReservedCredits)
					}
					otherReservation, err := f.store.GetQuotaReservation(context.Background(), otherReservationID)
					if err != nil || otherReservation == nil || otherReservation.Status != store.ReservationReserved {
						t.Fatalf("其他并发 reservation 不应被结算: reservation=%+v error=%v", otherReservation, err)
					}
					if err := checker.contextError(); err != nil {
						t.Fatalf("结算必须使用独立 context，不能沿用已取消的请求 context: %v", err)
					}
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatal("客户端中断后未写入 usage 记录")
		})
	}
}

func TestClientAbortWhileWaitingForUpstreamHeaders(t *testing.T) {
	checker := &settleContextStore{}
	f := newGatewayFixture(t, func(_ *Env, f *gatewayFixture) {
		checker.PlatformStore = f.store
		f.store = checker
	})
	otherReservationID := uuid.NewString()
	result, err := f.store.AdmitUsage(context.Background(), store.ReserveQuotaInput{
		ID: otherReservationID, TeamID: f.teamID, UserID: f.userID, RequestID: "other-" + otherReservationID,
		Micro: 0, TTL: time.Minute, CreatedAt: time.Now(),
	})
	if err != nil || result != store.ReserveReserved {
		t.Fatalf("create concurrent zero-balance identity: result=%s error=%v", result, err)
	}
	upstreamStarted := make(chan struct{}, 1)
	upstreamCanceled := make(chan struct{}, 1)
	f.setHandler(func(_ http.ResponseWriter, r *http.Request) {
		upstreamStarted <- struct{}{}
		<-r.Context().Done()
		upstreamCanceled <- struct{}{}
	})
	server := httptest.NewServer(f.engine)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/chat/completions",
		bytes.NewReader([]byte(`{"model":"claude-sonnet-4-5"}`)))
	req.Header.Set("authorization", "Bearer "+f.token)
	req.Header.Set("content-type", "application/json")
	responseErr := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		responseErr <- err
	}()
	select {
	case <-upstreamStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("gateway did not dispatch upstream request")
	}
	cancel()
	select {
	case <-upstreamCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("client cancellation did not cancel the in-flight upstream request")
	}
	select {
	case <-responseErr:
	case <-time.After(3 * time.Second):
		t.Fatal("gateway request did not finish after client cancellation")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		records := f.usageRecords()
		if len(records) == 1 {
			if records[0].Status != store.UsageStatusAborted || records[0].ErrorType != "client_aborted" || records[0].CreditsMicro != 0 {
				t.Fatalf("等待上游响应头时取消应记录零实扣的 aborted 终态: %+v", records[0])
			}
			if team := f.team(); team.ReservedCredits != "0.000000" || team.UsedCredits != "0.000000" {
				t.Fatalf("取消不能占用余额或虚扣: %+v", team)
			}
			otherReservation, err := f.store.GetQuotaReservation(context.Background(), otherReservationID)
			if err != nil || otherReservation == nil || otherReservation.Status != store.ReservationReserved {
				t.Fatalf("其他并发 reservation 不应被结算: reservation=%+v error=%v", otherReservation, err)
			}
			if err := checker.contextError(); err != nil {
				t.Fatalf("结算不能沿用已取消的客户端 context: %v", err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待上游响应头时取消后没有写入 aborted usage 记录")
}

func TestStreamingIdleTimeout(t *testing.T) {
	f := newGatewayFixture(t, func(env *Env, _ *gatewayFixture) {
		env.UpstreamIdleTimeoutMs = 80
	})
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})
	rec := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5","stream":true}`)
	if rec.Code != 200 {
		t.Fatalf("流式头应已返回: %d", rec.Code)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		records := f.usageRecords()
		if len(records) == 1 {
			if records[0].Status != store.UsageStatusError || records[0].ErrorType != "upstream_idle_timeout" {
				t.Fatalf("空闲超时记录不符: %+v", records[0])
			}
			// 无合法 usage 时不能把输出上限当成实际用量向用户扣费。
			if records[0].CreditsMicro != 0 {
				t.Fatalf("空闲超时无 usage 应零计费: %+v", records[0])
			}
			if team := f.team(); team.ReservedCredits != "0.000000" {
				t.Fatalf("空闲超时应释放预留: %s", team.ReservedCredits)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("空闲超时后未写入 usage 记录")
}

type failingSettleStore struct {
	store.PlatformStore
}

func (s *failingSettleStore) SettleReservation(context.Context, string, store.UsageRecord) error {
	return errors.New("database unavailable")
}

type recordingSettlementFallback struct {
	mu      sync.Mutex
	records []store.UsageRecord
	ids     []string
}

func (f *recordingSettlementFallback) Persist(_ context.Context, reservationID string, record store.UsageRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append(f.ids, reservationID)
	f.records = append(f.records, record)
	return nil
}

func TestSettlementFailurePersistsToFallback(t *testing.T) {
	fallback := &recordingSettlementFallback{}
	f := newGatewayFixture(t, func(_ *Env, f *gatewayFixture) {
		f.settlementFallback = fallback
		f.store = &failingSettleStore{PlatformStore: f.store}
	})
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"input_tokens":100,"output_tokens":50}}`))
	})
	rec := f.do("POST", "/v1/messages", f.token,
		`{"model":"claude-sonnet-4-5","max_tokens":100}`)
	if rec.Code != 200 {
		t.Fatalf("请求应成功返回: %d %s", rec.Code, rec.Body.String())
	}
	fallback.mu.Lock()
	defer fallback.mu.Unlock()
	if len(fallback.records) != 1 || fallback.ids[0] == "" {
		t.Fatalf("结算重试耗尽后应写入补偿队列: ids=%v records=%d", fallback.ids, len(fallback.records))
	}
	record := fallback.records[0]
	if record.InputTokens != 100 || record.OutputTokens != 50 || record.CreditsMicro <= 0 ||
		record.Status != store.UsageStatusOk {
		t.Fatalf("补偿记录应保留完整 usage: %+v", record)
	}
}

func TestDefaultOutputLimitWrittenUpstream(t *testing.T) {
	cases := []struct {
		name string
		path string
		key  string
		body string
	}{
		{"anthropic", "/v1/messages", "max_tokens", `{"model":"claude-sonnet-4-5"}`},
		{"openai-chat", "/v1/chat/completions", "max_tokens", `{"model":"claude-sonnet-4-5"}`},
		{"openai-responses", "/v1/responses", "max_output_tokens", `{"model":"claude-sonnet-4-5"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGatewayFixture(t, nil)
			f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("content-type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			})
			if rec := f.do("POST", tc.path, f.token, tc.body); rec.Code != 200 {
				t.Fatalf("请求失败: %d %s", rec.Code, rec.Body.String())
			}
			_, upBody := f.upstreamRequest()
			var sent map[string]any
			if err := json.Unmarshal(upBody, &sent); err != nil {
				t.Fatal(err)
			}
			if got, ok := sent[tc.key].(float64); !ok || int64(got) != 4096 {
				t.Fatalf("缺省输出上限必须写回上游 %s=4096: %v", tc.key, sent[tc.key])
			}
		})
	}
	// 超过平台上限时夹紧值也要同步写回，否则上游仍可能输出超过预留。
	f := newGatewayFixture(t, nil)
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	if rec := f.do("POST", "/v1/chat/completions", f.token,
		`{"model":"claude-sonnet-4-5","max_completion_tokens":999999}`); rec.Code != 200 {
		t.Fatalf("请求失败: %d %s", rec.Code, rec.Body.String())
	}
	_, upBody := f.upstreamRequest()
	var sent map[string]any
	_ = json.Unmarshal(upBody, &sent)
	if got, ok := sent["max_completion_tokens"].(float64); !ok || int64(got) != maxMaxTokens {
		t.Fatalf("夹紧后的 max_completion_tokens 不符: %v", sent["max_completion_tokens"])
	}
}

func TestStreamingFailureKeepsObservedUsage(t *testing.T) {
	f := newGatewayFixture(t, func(env *Env, _ *gatewayFixture) {
		env.UpstreamIdleTimeoutMs = 80
	})
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":40}}\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})
	rec := f.do("POST", "/v1/chat/completions", f.token,
		`{"model":"claude-sonnet-4-5","stream":true,"max_tokens":100}`)
	if rec.Code != 200 {
		t.Fatalf("流式头应已返回: %d", rec.Code)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		records := f.usageRecords()
		if len(records) == 1 {
			record := records[0]
			if record.Status != store.UsageStatusError || record.ErrorType != "upstream_idle_timeout" {
				t.Fatalf("状态不符: %+v", record)
			}
			// 断流仅按合法已观察 usage 入账，不估算未知尾部。
			if record.InputTokens != 100 || record.OutputTokens != 40 || record.CreditsMicro != 900 || record.PriceSnapshot == nil || record.PriceSnapshot.Cost.UsageEstimated {
				t.Fatalf("异常流式请求应保留已采集 usage: %+v", record)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("异常流式请求未写入 usage 记录")
}

func TestStreamingIdleFailureCountsTowardFuse(t *testing.T) {
	ctx := context.Background()
	f := newGatewayFixture(t, func(env *Env, _ *gatewayFixture) {
		env.UpstreamIdleTimeoutMs = 50
		env.ProviderAutoDisableThreshold = 2
	})
	provider, err := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
		Name: "stream-fuse-provider", BaseURL: f.upstream.URL, Protocol: store.ProtocolOpenAIChatCompletions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpsertModel(ctx, store.UpsertModelInput{
		ModelID: "claude-sonnet-4-5", DisplayName: "x", InputPricePerMtok: "3", OutputPricePerMtok: "15",
		ProvideUpstreamProviderID: true, UpstreamProviderID: provider.ID,
	}); err != nil {
		t.Fatal(err)
	}
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})
	for i := 0; i < 2; i++ {
		f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5","stream":true}`)
		got, err := f.store.GetUpstreamProvider(ctx, provider.ID)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && (got.ConsecutiveFailures != 1 || !got.Enabled) {
			t.Fatalf("流式空闲失败应计入熔断而不是被响应头重置: %+v", got)
		}
	}
	got, _ := f.store.GetUpstreamProvider(ctx, provider.ID)
	if got.Enabled || got.DisabledMode != "auto" || got.AutoDisabledReason != "upstream_idle_timeout" {
		t.Fatalf("连续流式空闲失败应触发熔断: %+v", got)
	}
}

// 网关只返回 JSON：必须有 nosniff / XFO / Referrer / Permissions，
// 且不得下发 CSP（重复 CSP 会与同源控制台策略求交，拦掉 Turnstile）。
func TestGatewaySecurityHeaders(t *testing.T) {
	f := newGatewayFixture(t, nil)
	engine := f.engine

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set("x-forwarded-proto", "https")
	engine.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options=%q", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options=%q", got)
	}
	if rec.Header().Get("Content-Security-Policy") != "" {
		t.Fatal("网关不应下发 CSP")
	}
	if got := rec.Header().Get("Strict-Transport-Security"); got != "max-age=31536000" {
		t.Fatalf("HTTPS 反代下应有 HSTS，实际 %q", got)
	}

	// 明文直连不应下发 HSTS。
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("明文不应下发 HSTS，实际 %q", got)
	}
}
