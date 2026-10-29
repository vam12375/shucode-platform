package apiapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/ratelimit"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

const testPollToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func testSecretKey() string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = 7
	}
	return base64.StdEncoding.EncodeToString(raw)
}

type apiFixture struct {
	t      *testing.T
	store  store.PlatformStore
	deps   *Deps
	engine *gin.Engine
	bus    *events.InProc

	upstreamMu   sync.Mutex
	upstream     *httptest.Server
	upstreamHits []upstreamHit
	upstreamFn   func(w http.ResponseWriter, r *http.Request)
}

type upstreamHit struct {
	method  string
	url     string
	headers http.Header
	body    []byte
}

func newAPIFixture(t *testing.T) *apiFixture {
	return newAPIFixtureWithSeed(t, true)
}

// newAPIEmptyFixture:不带演示账号的库，用于首次 root 初始化测试。
func newAPIEmptyFixture(t *testing.T) *apiFixture {
	return newAPIFixtureWithSeed(t, false)
}

func newAPIFixtureWithSeed(t *testing.T, demoSeed bool) *apiFixture {
	t.Helper()
	ctx := context.Background()
	mem, err := store.NewMemoryStore(ctx, store.MemoryOptions{DemoSeed: demoSeed})
	if err != nil {
		t.Fatal(err)
	}
	secretBox, err := crypto.NewSecretBox(testSecretKey())
	if err != nil {
		t.Fatal(err)
	}
	f := &apiFixture{t: t, store: mem, bus: events.NewInProc()}
	f.upstreamFn = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.upstreamMu.Lock()
		f.upstreamHits = append(f.upstreamHits, upstreamHit{
			method: r.Method, url: r.URL.String(), headers: r.Header.Clone(), body: body,
		})
		handler := f.upstreamFn
		f.upstreamMu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(f.upstream.Close)

	env := &Env{
		Port: 8787, Host: "127.0.0.1", MemoryDB: true,
		PublicAPIOrigin: "http://localhost:8787", PublicAppOrigin: "http://localhost:8787",
		PlatformSecretKey: testSecretKey(), SecureCookies: false,
		GatewayBaseURL: "https://gw.mendao.chat", GatewayAPIType: "openai-chat-completions",
		GatewayAPIBaseURL: "https://gw.mendao.chat/v1",
		ReleaseBaseURL:    "http://localhost:8787",
		// 测试上游是 httptest 的 http://127.0.0.1，必须显式放行；生产默认只允许 https。
		AllowInsecureUpstreamURLs: true,
	}
	window := ratelimit.Window{Window: 5 * time.Minute, Max: 10}
	f.deps = &Deps{
		Store: mem, Env: env, Now: time.Now, SecretBox: secretBox,
		ConfigEvents:         f.bus,
		ConsoleLoginLimiter:  ratelimit.NewInProc(window),
		PlatformLoginLimiter: ratelimit.NewInProc(window),
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		HTTPClient:           f.upstream.Client(),
	}
	f.engine = New(f.deps)
	return f
}

// asBrowser 复刻真实浏览器在写请求上携带的来源证明。
// csrfOriginGuard 现在要求带会话 Cookie 的写操作必须有 Origin 或
// Sec-Fetch-Site: same-origin；不给这两个头的客户端本来就不该能改状态。
func (f *apiFixture) asBrowser(req *http.Request) {
	origin := "http://localhost:8787"
	if f.deps != nil && f.deps.Env != nil && f.deps.Env.PublicAPIOrigin != "" {
		origin = f.deps.Env.PublicAPIOrigin
	}
	req.Header.Set("origin", origin)
	req.Header.Set("sec-fetch-site", "same-origin")
}

// doForge 刻意不带来源证明，用于验证 CSRF 守卫的拒绝路径。
func (f *apiFixture) doForge(method, path, cookie, body string, headers map[string]string) *httptest.ResponseRecorder {
	f.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("content-type", "application/json")
	}
	if cookie != "" {
		req.Header.Set("cookie", cookie)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	return rec
}

func (f *apiFixture) do(method, path, cookie string, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("content-type", "application/json")
	}
	if cookie != "" {
		req.Header.Set("cookie", cookie)
	}
	f.asBrowser(req)
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	return rec
}

func (f *apiFixture) form(path, cookie string, values map[string]string) *httptest.ResponseRecorder {
	f.t.Helper()
	data := url.Values{}
	for key, value := range values {
		data.Set(key, value)
	}
	req := httptest.NewRequest("POST", path, strings.NewReader(data.Encode()))
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	if cookie != "" {
		req.Header.Set("cookie", cookie)
	}
	f.asBrowser(req)
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(rec *httptest.ResponseRecorder) string {
	header := rec.Header().Get("set-cookie")
	if header == "" {
		return ""
	}
	return strings.Split(header, ";")[0]
}

func (f *apiFixture) loginAdmin() string {
	f.t.Helper()
	rec := f.do("POST", "/console/api/auth/login", "", `{"email":"admin@mendao.chat","password":"mendao-admin"}`)
	if rec.Code != 200 {
		f.t.Fatalf("管理员登录失败: %d %s", rec.Code, rec.Body.String())
	}
	return sessionCookie(rec)
}

func (f *apiFixture) lastUpstreamHit() upstreamHit {
	f.t.Helper()
	f.upstreamMu.Lock()
	defer f.upstreamMu.Unlock()
	if len(f.upstreamHits) == 0 {
		f.t.Fatal("上游未被调用")
	}
	return f.upstreamHits[len(f.upstreamHits)-1]
}

func jsonRecorderBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON: %s", rec.Body.String())
	}
	return body
}

func dataOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	body := jsonRecorderBody(t, rec)
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("缺 data: %s", rec.Body.String())
	}
	return data
}

func TestDeviceLoginFlow(t *testing.T) {
	f := newAPIFixture(t)

	// 1. init
	initRec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/platform/auth/init", strings.NewReader(`{"device_name":"测试 Mac","platform":"darwin-arm64"}`))
	req.Header.Set("authorization", "Bearer "+testPollToken)
	req.Header.Set("content-type", "application/json")
	f.engine.ServeHTTP(initRec, req)
	if initRec.Code != 200 {
		t.Fatalf("init: %d %s", initRec.Code, initRec.Body.String())
	}
	initData := dataOf(t, initRec)
	flowID := initData["flow_id"].(string)
	if !strings.Contains(initData["authorize_url"].(string), "/app/platform/login?flow_id="+flowID) {
		t.Fatalf("authorize_url 不符: %v", initData["authorize_url"])
	}

	// 2. 登录页包含设备名
	loginPage := f.do("GET", "/app/platform/login?flow_id="+flowID, "", "")
	if loginPage.Code != 200 || !strings.Contains(loginPage.Body.String(), "测试 Mac") {
		t.Fatalf("登录页: %d", loginPage.Code)
	}

	// 3. 表单登录
	loginRec := f.form("/app/platform/login", "", map[string]string{
		"flow_id": flowID, "email": "member@mendao.chat", "password": "mendao-member",
	})
	if loginRec.Code != 303 {
		t.Fatalf("表单登录: %d %s", loginRec.Code, loginRec.Body.String())
	}
	cookie := sessionCookie(loginRec)
	if !strings.Contains(cookie, "mendao_session=") {
		t.Fatalf("未设置会话 cookie: %q", loginRec.Header().Get("set-cookie"))
	}

	// 4. 授权
	authorizeRec := f.form("/app/platform/authorize", cookie, map[string]string{
		"flow_id": flowID, "decision": "approve",
	})
	if authorizeRec.Code != 303 {
		t.Fatalf("授权: %d", authorizeRec.Code)
	}

	// 5. 轮询领取凭据(只签发一次)
	poll := func() map[string]any {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/platform/auth/poll/"+flowID, nil)
		req.Header.Set("authorization", "Bearer "+testPollToken)
		f.engine.ServeHTTP(rec, req)
		return dataOf(t, rec)
	}
	first := poll()
	if first["status"] != "authorized" {
		t.Fatalf("首次轮询: %v", first)
	}
	credentials := first["credentials"].(map[string]any)
	deviceToken := credentials["token"].(string)
	if !strings.HasPrefix(deviceToken, "mdp_") || credentials["token_hint"] == "" {
		t.Fatalf("凭据形态不符: %v", credentials)
	}
	if second := poll(); second["status"] != "consumed" {
		t.Fatalf("二次轮询应 consumed: %v", second)
	}

	// 6. user-config 信封
	cfgRec := httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/v1/client/user-config", nil)
	req.Header.Set("authorization", "Bearer "+deviceToken)
	f.engine.ServeHTTP(cfgRec, req)
	if cfgRec.Code != 200 {
		t.Fatalf("user-config: %d %s", cfgRec.Code, cfgRec.Body.String())
	}
	envelope := dataOf(t, cfgRec)["envelope"].(map[string]any)
	personal := envelope["personalConfig"].(map[string]any)
	rules := personal["providerConfigRules"].(map[string]any)["providerRules"].([]any)
	rule := rules[0].(map[string]any)
	if rule["providerId"] != "platform:gateway" {
		t.Fatalf("providerId 不符: %v", rule["providerId"])
	}
	config := rule["config"].(map[string]any)
	api := config["api"].(map[string]any)
	if api["type"] != "openai-chat-completions" || api["baseUrl"] != "https://gw.mendao.chat/v1" {
		t.Fatalf("网关 api 配置不符: %v", api)
	}
	access := config["access"].(map[string]any)
	if access["apiKey"] != deviceToken {
		t.Fatalf("access.apiKey 必须为设备 token: %v", access["apiKey"])
	}
	if creds := envelope["credentials"].([]any); len(creds) != 0 {
		t.Fatalf("credentials 应为空: %v", creds)
	}
	if _, ok := personal["defaultModelSelection"]; !ok {
		t.Fatal("应下发 defaultModelSelection")
	}

	// ETag 304
	etag := cfgRec.Header().Get("etag")
	if etag == "" {
		t.Fatal("缺 etag")
	}
	notModified := httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/v1/client/user-config", nil)
	req.Header.Set("authorization", "Bearer "+deviceToken)
	req.Header.Set("if-none-match", etag)
	f.engine.ServeHTTP(notModified, req)
	if notModified.Code != 304 || notModified.Body.Len() != 0 {
		t.Fatalf("ETag 协商应 304 空体: %d %d", notModified.Code, notModified.Body.Len())
	}

	// 7. 管理员吊销设备 → 401
	adminCookie := f.loginAdmin()
	devices := dataOf(t, f.do("GET", "/console/api/devices", adminCookie, ""))["devices"].([]any)
	var targetID string
	for _, raw := range devices {
		device := raw.(map[string]any)
		if device["userEmail"] == "member@mendao.chat" {
			targetID = device["id"].(string)
		}
	}
	if targetID == "" {
		t.Fatal("未找到成员设备")
	}
	if rec := f.do("POST", "/console/api/devices/"+targetID+"/revoke", adminCookie, ""); rec.Code != 200 {
		t.Fatalf("吊销设备: %d", rec.Code)
	}
	afterRevoke := httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/v1/client/user-config", nil)
	req.Header.Set("authorization", "Bearer "+deviceToken)
	f.engine.ServeHTTP(afterRevoke, req)
	if afterRevoke.Code != 401 {
		t.Fatalf("吊销后 user-config 应 401: %d", afterRevoke.Code)
	}
}

func TestUnauthenticatedAuthorizeRedirects(t *testing.T) {
	f := newAPIFixture(t)
	initRec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/platform/auth/init", strings.NewReader(`{"device_name":"X"}`))
	req.Header.Set("authorization", "Bearer "+testPollToken)
	req.Header.Set("content-type", "application/json")
	f.engine.ServeHTTP(initRec, req)
	flowID := dataOf(t, initRec)["flow_id"].(string)
	rec := f.form("/app/platform/authorize", "", map[string]string{"flow_id": flowID, "decision": "approve"})
	if rec.Code != 303 || !strings.Contains(rec.Header().Get("location"), "/app/platform/login") {
		t.Fatalf("未登录授权应重定向登录页: %d %s", rec.Code, rec.Header().Get("location"))
	}
}

func TestUserManagement(t *testing.T) {
	f := newAPIFixture(t)
	adminCookie := f.loginAdmin()
	me := dataOf(t, f.do("GET", "/console/api/auth/me", adminCookie, ""))
	adminID := me["user"].(map[string]any)["id"].(string)

	patchUser := func(id, body string) *httptest.ResponseRecorder {
		return f.do("PATCH", "/console/api/users/"+id, adminCookie, body)
	}

	createRec := f.do("POST", "/console/api/users", adminCookie,
		`{"email":"alice@mendao.chat","password":"alice-password","displayName":"Alice","role":"user"}`)
	if createRec.Code != 200 {
		t.Fatalf("创建用户: %d %s", createRec.Code, createRec.Body.String())
	}
	aliceID := dataOf(t, createRec)["user"].(map[string]any)["id"].(string)

	// 禁用前平台登录可提交;禁用后 401。
	initRec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/platform/auth/init", strings.NewReader(`{"device_name":"Alice 的手机"}`))
	req.Header.Set("authorization", "Bearer "+testPollToken)
	req.Header.Set("content-type", "application/json")
	f.engine.ServeHTTP(initRec, req)
	flowID := dataOf(t, initRec)["flow_id"].(string)
	if rec := f.form("/app/platform/login", "", map[string]string{
		"flow_id": flowID, "email": "alice@mendao.chat", "password": "alice-password",
	}); rec.Code != 303 {
		t.Fatalf("禁用前登录应 303: %d", rec.Code)
	}
	disableRec := patchUser(aliceID, `{"disabled":true}`)
	if disableRec.Code != 200 {
		t.Fatalf("禁用: %d", disableRec.Code)
	}
	if dataOf(t, disableRec)["user"].(map[string]any)["disabledAt"] == nil {
		t.Fatal("disabledAt 应非空")
	}
	if rec := f.form("/app/platform/login", "", map[string]string{
		"flow_id": flowID, "email": "alice@mendao.chat", "password": "alice-password",
	}); rec.Code != 401 {
		t.Fatalf("禁用后登录应 401: %d", rec.Code)
	}
	// 启用 + 重置密码后可登录。
	if rec := patchUser(aliceID, `{"disabled":false,"password":"alice-password-2"}`); rec.Code != 200 {
		t.Fatalf("启用+重置: %d", rec.Code)
	}
	if rec := f.form("/app/platform/login", "", map[string]string{
		"flow_id": flowID, "email": "alice@mendao.chat", "password": "alice-password-2",
	}); rec.Code != 303 {
		t.Fatalf("重置后登录应 303: %d", rec.Code)
	}
	// 自我保护。
	if rec := patchUser(adminID, `{"disabled":true}`); rec.Code != 400 {
		t.Fatalf("禁用自己应 400: %d", rec.Code)
	}
	if rec := patchUser(adminID, `{"role":"user"}`); rec.Code != 400 {
		t.Fatalf("改自己角色应 400: %d", rec.Code)
	}
	// 未登录 / 目标不存在。
	if rec := f.do("PATCH", "/console/api/users/"+aliceID, "", `{"disabled":true}`); rec.Code != 401 {
		t.Fatal("未登录应 401")
	}
	if rec := patchUser("ffffffff-ffff-ffff-ffff-ffffffffffff", `{"disabled":true}`); rec.Code != 404 {
		t.Fatal("不存在用户应 404")
	}
}

// root 账号列表注入团队归属 + 设备列表按 userId 过滤(合并后的「团队与账号」页依赖)。
func TestUsersListTeamEnrichmentAndDevicesFilter(t *testing.T) {
	f := newAPIFixture(t)
	adminCookie := f.loginAdmin()
	ctx := context.Background()

	createRec := f.do("POST", "/console/api/users", adminCookie,
		`{"email":"noah@mendao.chat","password":"noah-password","displayName":"Noah","role":"user"}`)
	if createRec.Code != 200 {
		t.Fatalf("创建用户: %d %s", createRec.Code, createRec.Body.String())
	}
	noahID := dataOf(t, createRec)["user"].(map[string]any)["id"].(string)

	userTeams := func() map[string]map[string]any {
		t.Helper()
		rows := dataOf(t, f.do("GET", "/console/api/users", adminCookie, ""))["users"].([]any)
		out := map[string]map[string]any{}
		for _, raw := range rows {
			user := raw.(map[string]any)
			team, _ := user["team"].(map[string]any)
			out[user["email"].(string)] = team
		}
		return out
	}

	// 种子的演示成员属于演示团队;新账号未入团队时不带 team 字段。
	rows := userTeams()
	if rows["noah@mendao.chat"] != nil {
		t.Fatalf("未入团队用户不应带 team: %v", rows["noah@mendao.chat"])
	}
	member := rows["member@mendao.chat"]
	if member == nil || member["teamName"] != "演示团队" || member["memberRole"] != "member" || member["monthlyBudgetCredits"] != "" {
		t.Fatalf("演示成员团队归属不符: %v", member)
	}

	teamRec := f.do("POST", "/console/api/teams", adminCookie, `{"name":"交付团队","quotaCredits":"500","unlimited":false}`)
	if teamRec.Code != 200 {
		t.Fatalf("创建团队: %d %s", teamRec.Code, teamRec.Body.String())
	}
	teamID := dataOf(t, teamRec)["team"].(map[string]any)["id"].(string)
	if rec := f.do("POST", "/console/api/teams/"+teamID+"/members", adminCookie, `{"email":"noah@mendao.chat"}`); rec.Code != 200 {
		t.Fatalf("加入团队: %d %s", rec.Code, rec.Body.String())
	}
	noah := userTeams()["noah@mendao.chat"]
	if noah == nil || noah["teamId"] != teamID || noah["teamName"] != "交付团队" || noah["memberRole"] != "member" {
		t.Fatalf("加入后归属未被注入: %v", noah)
	}

	// 设备列表按 userId 精确过滤(合法/非法/含 teamId 时 userId 优先)。
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := f.store.CreateDeviceToken(ctx, store.DeviceTokenRecord{
		ID: "00000000-0000-4000-8000-000000000901", UserID: noahID, Name: "Noah 笔记本",
		Platform: "darwin", TokenHash: "th-noah", TokenCiphertext: "v1.a.b.c", TokenHint: "n901", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	filtered := dataOf(t, f.do("GET", "/console/api/devices?userId="+noahID, adminCookie, ""))["devices"].([]any)
	if len(filtered) != 1 || filtered[0].(map[string]any)["name"] != "Noah 笔记本" {
		t.Fatalf("按 userId 过滤设备不符: %v", filtered)
	}
	withTeam := dataOf(t, f.do("GET", "/console/api/devices?userId="+noahID+"&teamId="+teamID, adminCookie, ""))["devices"].([]any)
	if len(withTeam) != 1 {
		t.Fatalf("userId 过滤应优先于 teamId: %v", withTeam)
	}
	if rec := f.do("GET", "/console/api/devices?userId=not-a-uuid", adminCookie, ""); rec.Code != 400 {
		t.Fatalf("非法 userId 应 400: %d", rec.Code)
	}
}

func TestSessionAndDeviceRevocation(t *testing.T) {
	f := newAPIFixture(t)
	adminCookie := f.loginAdmin()

	// 创建第二管理员 bob 并登录。
	bobCreate := f.do("POST", "/console/api/users", adminCookie,
		`{"email":"bob@mendao.chat","password":"bob-password","displayName":"Bob","role":"admin"}`)
	if bobCreate.Code != 200 {
		t.Fatalf("创建 bob: %d", bobCreate.Code)
	}
	bobID := dataOf(t, bobCreate)["user"].(map[string]any)["id"].(string)
	token := "mdp_" + strings.Repeat("c", 32)
	if err := f.store.CreateDeviceToken(context.Background(), store.DeviceTokenRecord{
		ID: uuid.NewString(), UserID: bobID, Name: "测试设备",
		TokenHash: crypto.HashToken(token), TokenCiphertext: "unused", TokenHint: "abcd",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	bobLogin := f.do("POST", "/console/api/auth/login", "", `{"email":"bob@mendao.chat","password":"bob-password"}`)
	bobCookie := sessionCookie(bobLogin)
	if rec := f.do("GET", "/console/api/auth/me", bobCookie, ""); rec.Code != 200 {
		t.Fatal("bob 登录态应有效")
	}
	// 重置密码 → 旧会话 401、设备吊销。
	if rec := f.do("PATCH", "/console/api/users/"+bobID, adminCookie, `{"password":"bob-password-2"}`); rec.Code != 200 {
		t.Fatalf("重置密码: %d", rec.Code)
	}
	if rec := f.do("GET", "/console/api/auth/me", bobCookie, ""); rec.Code != 401 {
		t.Fatal("重置密码后旧会话应 401")
	}
	devices := dataOf(t, f.do("GET", "/console/api/devices", adminCookie, ""))["devices"].([]any)
	for _, raw := range devices {
		device := raw.(map[string]any)
		if device["userEmail"] == "bob@mendao.chat" {
			if device["revokedAt"] == nil {
				t.Fatal("重置密码应吊销设备凭据")
			}
		}
	}
}

func TestConsoleAuditAndQuotaAdjustments(t *testing.T) {
	f := newAPIFixture(t)
	adminCookie := f.loginAdmin()

	// 供应商三步动作留痕。
	createRec := f.do("POST", "/console/api/providers", adminCookie,
		`{"name":"AuditP","baseUrl":"https://audit.test"}`)
	if createRec.Code != 200 {
		t.Fatalf("创建供应商: %d %s", createRec.Code, createRec.Body.String())
	}
	providerID := dataOf(t, createRec)["provider"].(map[string]any)["id"].(string)
	f.do("PATCH", "/console/api/providers/"+providerID, adminCookie, `{"enabled":false}`)
	if rec := f.do("DELETE", "/console/api/providers/"+providerID, adminCookie, ""); rec.Code != 200 {
		t.Fatalf("删除供应商: %d", rec.Code)
	}
	// 团队额度调整快照。
	teamRec := f.do("POST", "/console/api/teams", adminCookie, `{"name":"审计团队","quotaCredits":"10"}`)
	teamID := dataOf(t, teamRec)["team"].(map[string]any)["id"].(string)
	if rec := f.do("PATCH", "/console/api/teams/"+teamID, adminCookie, `{"quotaCredits":"20","unlimited":true}`); rec.Code != 200 {
		t.Fatalf("调额: %d", rec.Code)
	}
	detail := dataOf(t, f.do("GET", "/console/api/teams/"+teamID, adminCookie, ""))
	adjustments := detail["quotaAdjustments"].([]any)
	if len(adjustments) != 1 {
		t.Fatalf("应有 1 条调额流水: %v", adjustments)
	}
	adjustment := adjustments[0].(map[string]any)
	if adjustment["oldQuotaCredits"] != "10.000000" || adjustment["newQuotaCredits"] != "20.000000" ||
		adjustment["oldUnlimited"] != false || adjustment["newUnlimited"] != true ||
		adjustment["actorEmail"] != "admin@mendao.chat" {
		t.Fatalf("调额流水不符: %v", adjustment)
	}
	// 审计动作。
	auditData := dataOf(t, f.do("GET", "/console/api/audit", adminCookie, ""))
	logs := auditData["logs"].([]any)
	actions := map[string]bool{}
	var deletedTarget, deletedActor any
	for _, raw := range logs {
		log := raw.(map[string]any)
		actions[log["action"].(string)] = true
		if log["action"] == "provider.delete" {
			deletedTarget, deletedActor = log["targetId"], log["actorEmail"]
		}
	}
	if !actions["provider.create"] || !actions["provider.update"] || !actions["provider.delete"] {
		t.Fatalf("审计动作缺失: %v", actions)
	}
	if deletedTarget != providerID || deletedActor != "admin@mendao.chat" {
		t.Fatalf("审计目标/操作者不符: %v %v", deletedTarget, deletedActor)
	}
	if rec := f.do("GET", "/console/api/audit", "", ""); rec.Code != 401 {
		t.Fatal("未登录审计应 401")
	}
}

func TestUsageFilters(t *testing.T) {
	f := newAPIFixture(t)
	adminCookie := f.loginAdmin()
	ctx := context.Background()
	teams, err := f.store.ListTeams(ctx)
	if err != nil || len(teams) == 0 {
		t.Fatal("演示团队缺失")
	}
	teamID := teams[0].Team.ID
	member, err := f.store.FindUserByEmail(ctx, "member@mendao.chat")
	if err != nil || member == nil {
		t.Fatal("演示成员缺失")
	}
	base := time.Date(2026, 10, 15, 12, 0, 1, 0, time.UTC)
	for i, status := range []store.UsageStatus{store.UsageStatusOk, store.UsageStatusError} {
		latency := int64(3)
		errorType := ""
		if status == store.UsageStatusError {
			errorType = "upstream_502"
		}
		if err := f.store.InsertUsage(ctx, store.UsageRecord{
			ID: uuid.NewString(), RequestID: fmt.Sprintf("t-%d", i),
			UserID: member.ID, TeamID: teamID, Model: "m-a", UpstreamModel: "m-a",
			CreditsMicro: 1000, Status: status, ErrorType: errorType, LatencyMs: &latency,
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	rec := f.do("GET", "/console/api/usage?model=m-a&status=error", adminCookie, "")
	if rec.Code != 200 {
		t.Fatalf("usage: %d", rec.Code)
	}
	data := dataOf(t, rec)
	records := data["records"].([]any)
	if len(records) != 1 || records[0].(map[string]any)["requestId"] != "t-1" ||
		records[0].(map[string]any)["errorType"] != "upstream_502" {
		t.Fatalf("筛选结果不符: %v", records)
	}
	aggregates := data["aggregates"].([]any)
	if len(aggregates) != 1 || aggregates[0].(map[string]any)["requests"] != float64(1) {
		t.Fatalf("聚合应联动收窄: %v", aggregates)
	}
	if rec := f.do("GET", "/console/api/usage", adminCookie, ""); len(dataOf(t, rec)["records"].([]any)) != 2 {
		t.Fatal("无筛选应返回两条")
	}
	if rec := f.do("GET", "/console/api/usage?status=weird", adminCookie, ""); rec.Code != 400 {
		t.Fatal("非法 status 应 400")
	}
	if rec := f.do("GET", "/console/api/usage?providerId=not-a-uuid", adminCookie, ""); rec.Code != 400 {
		t.Fatal("非法 providerId 应 400")
	}
	if rec := f.do("GET", "/console/api/usage", "", ""); rec.Code != 401 {
		t.Fatal("未登录应 401")
	}
}

func TestClientSSE(t *testing.T) {
	f := newAPIFixture(t)
	if rec := f.do("GET", "/api/v1/client/events", "", ""); rec.Code != 401 {
		t.Fatal("未登录 SSE 应 401")
	}
	member, _ := f.store.FindUserByEmail(context.Background(), "member@mendao.chat")
	token := "mdp_" + strings.Repeat("d", 32)
	if err := f.store.CreateDeviceToken(context.Background(), store.DeviceTokenRecord{
		ID: uuid.NewString(), UserID: member.ID, Name: "SSE 设备",
		TokenHash: crypto.HashToken(token), TokenCiphertext: "unused", TokenHint: "abcd", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(f.engine)
	defer server.Close()
	req, _ := http.NewRequest("GET", server.URL+"/api/v1/client/events", nil)
	req.Header.Set("authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("content-type"), "text/event-stream") {
		t.Fatalf("SSE 头不符: %d %s", resp.StatusCode, resp.Header.Get("content-type"))
	}
	reader := newSSEReader(resp.Body)
	hello := reader.readUntil(t, `"type":"hello"`)
	if !strings.Contains(hello, "retry: 5000") {
		t.Fatalf("hello 帧缺 retry: %q", hello)
	}
	f.bus.Emit(events.DomainModels)
	f.bus.Emit(events.DomainProviders)
	eventText := reader.readUntil(t, "event: config.changed")
	if !strings.Contains(eventText, "models") || !strings.Contains(eventText, "providers") {
		t.Fatalf("config.changed 缺合并 domains: %q", eventText)
	}
	f.bus.Emit(events.DomainPreferences)
	preferencesEvent := reader.readUntil(t, "event: preferences.changed")
	if strings.Contains(preferencesEvent, "event: config.changed") || !strings.Contains(preferencesEvent, `"domain":"preferences"`) {
		t.Fatalf("Agent 媒体偏好应使用独立 SSE 事件: %q", preferencesEvent)
	}
}

type sseReader struct {
	body   io.Reader
	buffer string
}

func newSSEReader(body io.Reader) *sseReader { return &sseReader{body: body} }

func (r *sseReader) readUntil(t *testing.T, needle string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 512)
	for time.Now().Before(deadline) {
		if frame, ok := r.nextFrame(); ok {
			if strings.Contains(frame, needle) {
				return frame
			}
			continue
		}
		n, err := r.body.Read(buf)
		if n > 0 {
			r.buffer += string(buf[:n])
		}
		if err != nil {
			t.Fatalf("SSE 流提前结束: %v", err)
		}
	}
	t.Fatalf("等待 %q 超时,已收到: %q", needle, r.buffer)
	return ""
}

func (r *sseReader) nextFrame() (string, bool) {
	separator := "\n\n"
	separatorIndex := strings.Index(r.buffer, separator)
	if crlfIndex := strings.Index(r.buffer, "\r\n\r\n"); crlfIndex >= 0 && (separatorIndex < 0 || crlfIndex < separatorIndex) {
		separator = "\r\n\r\n"
		separatorIndex = crlfIndex
	}
	if separatorIndex < 0 {
		return "", false
	}
	frame := r.buffer[:separatorIndex+len(separator)]
	r.buffer = r.buffer[len(frame):]
	return frame, true
}

func TestRootSetup(t *testing.T) {
	f := newAPIEmptyFixture(t)

	status := dataOf(t, f.do("GET", "/console/api/setup/status", "", ""))
	if status["rootInitialized"] != false {
		t.Fatalf("空库不应已初始化: %v", status)
	}
	// 弱口令拒绝。
	if rec := f.do("POST", "/console/api/setup/root", "",
		`{"email":"root@mendao.chat","displayName":"Root","password":"short"}`); rec.Code != 400 {
		t.Fatalf("短口令应 400: %d %s", rec.Code, rec.Body.String())
	}
	rec := f.do("POST", "/console/api/setup/root", "",
		`{"email":"root@mendao.chat","displayName":"Root","password":"root-password-123"}`)
	if rec.Code != 200 {
		t.Fatalf("初始化 root: %d %s", rec.Code, rec.Body.String())
	}
	user := dataOf(t, rec)["user"].(map[string]any)
	if user["role"] != "root" {
		t.Fatalf("初始化账号应为 root: %v", user)
	}
	cookie := sessionCookie(rec)
	if cookie == "" {
		t.Fatal("初始化后应直接登录")
	}
	// root 只能创建一次。
	if rec := f.do("POST", "/console/api/setup/root", "",
		`{"email":"root2@mendao.chat","displayName":"Root2","password":"root-password-456"}`); rec.Code != 409 {
		t.Fatalf("二次初始化应 409: %d %s", rec.Code, rec.Body.String())
	}
	status = dataOf(t, f.do("GET", "/console/api/setup/status", "", ""))
	if status["rootInitialized"] != true {
		t.Fatalf("初始化后状态应为 true: %v", status)
	}
	// 初始化口令可正常登录控制台。
	login := f.do("POST", "/console/api/auth/login", "",
		`{"email":"root@mendao.chat","password":"root-password-123"}`)
	if login.Code != 200 {
		t.Fatalf("root 登录: %d %s", login.Code, login.Body.String())
	}
	users := dataOf(t, f.do("GET", "/console/api/users", cookie, ""))["users"].([]any)
	if len(users) != 1 || users[0].(map[string]any)["role"] != "root" {
		t.Fatalf("root 清单不符: %v", users)
	}
}

func TestRootRoleHierarchy(t *testing.T) {
	f := newAPIFixture(t)
	rootCookie := f.loginAdmin()
	rootID := dataOf(t, f.do("GET", "/console/api/auth/me", rootCookie, ""))["user"].(map[string]any)["id"].(string)

	// root 创建普通管理员与普通用户。
	createAdmin := f.do("POST", "/console/api/users", rootCookie,
		`{"email":"ops@mendao.chat","password":"ops-password","displayName":"Ops","role":"admin"}`)
	if createAdmin.Code != 200 {
		t.Fatalf("root 创建 admin: %d %s", createAdmin.Code, createAdmin.Body.String())
	}
	opsID := dataOf(t, createAdmin)["user"].(map[string]any)["id"].(string)
	createAdmin2 := f.do("POST", "/console/api/users", rootCookie,
		`{"email":"ops2@mendao.chat","password":"ops2-password","displayName":"Ops2","role":"admin"}`)
	if createAdmin2.Code != 200 {
		t.Fatalf("root 创建 admin2: %d %s", createAdmin2.Code, createAdmin2.Body.String())
	}
	ops2ID := dataOf(t, createAdmin2)["user"].(map[string]any)["id"].(string)
	// root 不能通过用户接口创建第二个 root。
	if rec := f.do("POST", "/console/api/users", rootCookie,
		`{"email":"root2@mendao.chat","password":"root2-password","displayName":"Root2","role":"root"}`); rec.Code != 400 {
		t.Fatalf("创建 root 角色应 400: %d", rec.Code)
	}

	opsLogin := f.do("POST", "/console/api/auth/login", "",
		`{"email":"ops@mendao.chat","password":"ops-password"}`)
	opsCookie := sessionCookie(opsLogin)
	if opsCookie == "" {
		t.Fatalf("admin 登录失败: %d %s", opsLogin.Code, opsLogin.Body.String())
	}
	// 团队管理员通过邀请管理成员，不能创建全局账号或授予管理员。
	if rec := f.do("POST", "/console/api/users", opsCookie,
		`{"email":"bad-admin@mendao.chat","password":"bad-admin-password","displayName":"Bad","role":"admin"}`); rec.Code != 403 {
		t.Fatalf("admin 创建管理员应 403: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", "/console/api/users", opsCookie,
		`{"email":"blocked@mendao.chat","password":"alice-password","displayName":"Blocked","role":"user"}`); rec.Code != 403 {
		t.Fatalf("admin 不得创建全局用户: %d %s", rec.Code, rec.Body.String())
	}
	createUser := f.do("POST", "/console/api/users", rootCookie,
		`{"email":"alice@mendao.chat","password":"alice-password","displayName":"Alice","role":"user"}`)
	if createUser.Code != 200 {
		t.Fatalf("root 创建用户: %d %s", createUser.Code, createUser.Body.String())
	}
	aliceID := dataOf(t, createUser)["user"].(map[string]any)["id"].(string)
	// 普通管理员不能操作 root/其他管理员，也不能改角色。
	if rec := f.do("PATCH", "/console/api/users/"+rootID, opsCookie, `{"password":"hacked-password"}`); rec.Code != 403 {
		t.Fatalf("admin 改 root 应 403: %d", rec.Code)
	}
	if rec := f.do("PATCH", "/console/api/users/"+ops2ID, opsCookie, `{"disabled":true}`); rec.Code != 403 {
		t.Fatalf("admin 禁用其他管理员应 403: %d", rec.Code)
	}
	if rec := f.do("PATCH", "/console/api/users/"+aliceID, opsCookie, `{"role":"admin"}`); rec.Code != 403 {
		t.Fatalf("admin 改角色应 403: %d", rec.Code)
	}
	if rec := f.do("PATCH", "/console/api/users/"+opsID, opsCookie, `{"role":"user"}`); rec.Code != 403 {
		t.Fatalf("admin 改自己角色应 403: %d", rec.Code)
	}
	// 团队管理员不能全局禁用成员账号。
	if rec := f.do("PATCH", "/console/api/users/"+aliceID, opsCookie, `{"disabled":true}`); rec.Code != 403 {
		t.Fatalf("admin 禁用用户应403: %d %s", rec.Code, rec.Body.String())
	}
	// root 可以升降普通用户角色。
	if rec := f.do("PATCH", "/console/api/users/"+aliceID, rootCookie, `{"role":"admin"}`); rec.Code != 200 {
		t.Fatalf("root 提升用户: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("PATCH", "/console/api/users/"+aliceID, rootCookie, `{"role":"user"}`); rec.Code != 200 {
		t.Fatalf("root 降级用户: %d %s", rec.Code, rec.Body.String())
	}
	// root 不能被降级或禁用。
	if rec := f.do("PATCH", "/console/api/users/"+rootID, rootCookie, `{"role":"user"}`); rec.Code != 400 {
		t.Fatalf("root 降级自己应 400: %d", rec.Code)
	}
	if rec := f.do("PATCH", "/console/api/users/"+rootID, rootCookie, `{"disabled":true}`); rec.Code != 400 {
		t.Fatalf("root 禁用自己应 400: %d", rec.Code)
	}
}

func TestPasswordResetRevokesAuthorizedLoginFlow(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()

	initRec := httptest.NewRecorder()
	initReq := httptest.NewRequest("POST", "/api/v1/platform/auth/init",
		strings.NewReader(`{"device_name":"撤销测试机","platform":"darwin-arm64"}`))
	initReq.Header.Set("authorization", "Bearer "+testPollToken)
	initReq.Header.Set("content-type", "application/json")
	f.engine.ServeHTTP(initRec, initReq)
	if initRec.Code != 200 {
		t.Fatalf("init: %d %s", initRec.Code, initRec.Body.String())
	}
	flowID := dataOf(t, initRec)["flow_id"].(string)

	loginRec := f.form("/app/platform/login", "", map[string]string{
		"flow_id": flowID, "email": "member@mendao.chat", "password": "mendao-member",
	})
	if loginRec.Code != 303 {
		t.Fatalf("表单登录: %d", loginRec.Code)
	}
	if rec := f.form("/app/platform/authorize", sessionCookie(loginRec), map[string]string{
		"flow_id": flowID, "decision": "approve",
	}); rec.Code != 303 {
		t.Fatalf("授权: %d", rec.Code)
	}

	member, err := f.store.FindUserByEmail(ctx, "member@mendao.chat")
	if err != nil || member == nil {
		t.Fatalf("演示成员不存在: %v", err)
	}
	adminCookie := f.loginAdmin()
	reset := f.do("PATCH", "/console/api/users/"+member.ID, adminCookie, `{"password":"newpassword123"}`)
	if reset.Code != 200 {
		t.Fatalf("重置密码: %d %s", reset.Code, reset.Body.String())
	}

	pollRec := httptest.NewRecorder()
	pollReq := httptest.NewRequest("GET", "/api/v1/platform/auth/poll/"+flowID, nil)
	pollReq.Header.Set("authorization", "Bearer "+testPollToken)
	f.engine.ServeHTTP(pollRec, pollReq)
	polled := dataOf(t, pollRec)
	// 未消费的登录流已作废：不能再兑换设备凭据。
	if polled["status"] != "expired" {
		t.Fatalf("改密后未消费登录流应失效: %v", polled)
	}
	if _, exists := polled["credentials"]; exists {
		t.Fatalf("失效登录流不得签发凭据: %v", polled)
	}
}
