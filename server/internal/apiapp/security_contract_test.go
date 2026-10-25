package apiapp

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/ratelimit"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// 本轮评审对应的回归用例：限速维度、Retry-After、登录并发/字段、公开端点
// 限速、空闲超时、可选 root MFA 门禁、响应头与导出审计。

func (f *apiFixture) doFrom(method, path, cookie, body, remoteAddr string) *httptest.ResponseRecorder {
	f.t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("content-type", "application/json")
	}
	if cookie != "" {
		req.Header.Set("cookie", cookie)
	}
	req.RemoteAddr = remoteAddr
	f.asBrowser(req)
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	return rec
}

func TestLoginIPBurstBucketAndRetryAfter(t *testing.T) {
	f := newAPIFixture(t)
	f.deps.LoginIPBurstLimiter = ratelimit.NewInProc(ratelimit.Window{Window: time.Minute, Max: 2})
	f.deps.LoginIPLimiter = nil

	for i := 0; i < 2; i++ {
		rec := f.do("POST", "/console/api/auth/login", "",
			`{"email":"probe`+string(rune('a'+i))+`@mendao.chat","password":"wrong"}`)
		if rec.Code != 401 {
			t.Fatalf("第 %d 次应进入密码失败: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	rec := f.do("POST", "/console/api/auth/login", "",
		`{"email":"probe-c@mendao.chat","password":"wrong"}`)
	if rec.Code != 429 {
		t.Fatalf("同一 IP 的第三次登录应被突发桶拒绝: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After 应为 60，实际 %q", got)
	}
}

func TestLoginAccountBucketIsIndependentOfIP(t *testing.T) {
	f := newAPIFixture(t)
	f.deps.ConsoleLoginLimiter = ratelimit.NewInProc(ratelimit.Window{Window: 5 * time.Minute, Max: 1})
	f.deps.LoginIPBurstLimiter = nil
	f.deps.LoginIPLimiter = nil

	first := f.doFrom("POST", "/console/api/auth/login", "",
		`{"email":"admin@mendao.chat","password":"wrong"}`, "203.0.113.10:1234")
	if first.Code != 401 {
		t.Fatalf("首个 IP 应进入密码失败: %d %s", first.Code, first.Body.String())
	}
	second := f.doFrom("POST", "/console/api/auth/login", "",
		`{"email":"admin@mendao.chat","password":"wrong"}`, "203.0.113.11:1234")
	if second.Code != 429 {
		t.Fatalf("换 IP 不能绕过账号桶: %d %s", second.Code, second.Body.String())
	}
	if got := second.Header().Get("Retry-After"); got != "300" {
		t.Fatalf("账号桶 Retry-After 应为 300，实际 %q", got)
	}
}

func TestLoginRejectsUnknownFields(t *testing.T) {
	f := newAPIFixture(t)
	for _, body := range []string{
		`{"email":"admin@mendao.chat","password":"mendao-admin","role":"root"}`,
		`{"email":"admin@mendao.chat","password":"mendao-admin","__proto__":{"role":"root"}}`,
	} {
		rec := f.do("POST", "/console/api/auth/login", "", body)
		if rec.Code != 400 {
			t.Fatalf("未知登录字段应 400: %d %s", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("set-cookie") != "" {
			t.Fatal("参数被拒绝时不得签发会话")
		}
	}
}

func TestPublicConfigRateLimited(t *testing.T) {
	f := newAPIFixture(t)
	f.deps.PublicReadLimiter = ratelimit.NewInProc(ratelimit.Window{Window: time.Minute, Max: 1})

	if rec := f.do("GET", "/console/api/public-config", "", ""); rec.Code != 200 {
		t.Fatalf("首次公开配置应 200: %d %s", rec.Code, rec.Body.String())
	}
	rec := f.do("GET", "/console/api/public-config", "", "")
	if rec.Code != 429 {
		t.Fatalf("公开配置第二次应 429: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After 应为 60，实际 %q", got)
	}
}

func TestSessionIdleTimeout(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	user, err := f.store.FindUserByEmail(ctx, "admin@mendao.chat")
	if err != nil || user == nil {
		t.Fatalf("演示管理员缺失: %v", err)
	}
	f.deps.Env.SessionIdleTimeout = time.Minute
	now := time.Now()
	token := uuid.NewString()
	if err := f.store.CreateSession(ctx, store.SessionRecord{
		TokenHash: crypto.HashToken(token), UserID: user.ID,
		CreatedAt: now.Add(-2 * time.Minute), LastSeenAt: now.Add(-2 * time.Minute),
		ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookieName + "=" + token
	if rec := f.do("GET", "/console/api/auth/me", cookie, ""); rec.Code != 401 {
		t.Fatalf("空闲超过阈值的会话应 401: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRootMFAOptInGate(t *testing.T) {
	fixture := newRootMFAFixture(t)
	fixture.f.deps.Env.RootMFARequired = true

	if rec := fixture.f.do("GET", "/console/api/teams", fixture.cookie, ""); rec.Code != 403 {
		t.Fatalf("未绑定 MFA 的 root 不能访问业务接口: %d %s", rec.Code, rec.Body.String())
	}
	me := dataOf(t, fixture.f.do("GET", "/console/api/auth/me", fixture.cookie, ""))
	if me["mfaSetupRequired"] != true {
		t.Fatalf("/auth/me 应提示绑定 MFA: %v", me)
	}
	if rec := fixture.f.do("GET", "/console/api/security/mfa", fixture.cookie, ""); rec.Code != 200 {
		t.Fatalf("MFA 设置接口必须保持可达: %d %s", rec.Code, rec.Body.String())
	}

	secret := fixture.setup(t)
	fixture.enable(t, secret)
	if rec := fixture.f.do("GET", "/console/api/teams", fixture.cookie, ""); rec.Code != 200 {
		t.Fatalf("绑定后应恢复业务访问: %d %s", rec.Code, rec.Body.String())
	}
	me = dataOf(t, fixture.f.do("GET", "/console/api/auth/me", fixture.cookie, ""))
	if me["mfaSetupRequired"] == true {
		t.Fatalf("绑定后不应再提示 MFA: %v", me)
	}
}

func TestSecurityHeadersIncludeIsolationAndCSPReport(t *testing.T) {
	f := newAPIFixture(t)
	f.deps.Env.CSPReportURI = "https://reports.example.test/csp"
	f.engine = New(f.deps)

	rec := f.do("GET", "/healthz", "", "")
	if got := rec.Header().Get("Cross-Origin-Opener-Policy"); got != "same-origin" {
		t.Fatalf("COOP 头不符: %q", got)
	}
	if got := rec.Header().Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
		t.Fatalf("CORP 头不符: %q", got)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "report-uri https://reports.example.test/csp") ||
		!strings.Contains(csp, "report-to csp") {
		t.Fatalf("CSP 缺少上报指令: %q", csp)
	}
	if got := rec.Header().Get("Reporting-Endpoints"); !strings.Contains(got, "reports.example.test") {
		t.Fatalf("Reporting-Endpoints 缺失: %q", got)
	}
}

func TestUnauthenticatedConsoleRoutesStayClosedAndCORSIsNotReflected(t *testing.T) {
	f := newAPIFixture(t)
	for _, path := range []string{
		"/console/api/teams", "/console/api/users", "/console/api/usage",
		"/console/api/audit", "/console/api/providers", "/console/api/config",
		"/console/api/releases", "/console/api/models", "/console/api/devices",
		"/console/api/quota-requests",
	} {
		rec := f.do("GET", path, "", "")
		if rec.Code != 401 || !strings.Contains(rec.Body.String(), "请先登录") {
			t.Fatalf("%s 未登录访问应统一 401: %d %s", path, rec.Code, rec.Body.String())
		}
	}

	f.deps.Env.PublicConsoleOrigin = "https://console.example.test"
	f.engine = New(f.deps)
	rec := f.doForge("GET", "/console/api/public-config", "", "", map[string]string{
		"origin": "https://evil.example.test",
	})
	if rec.Code != 200 {
		t.Fatalf("public-config 应可公开读取: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://console.example.test" {
		t.Fatalf("CORS 必须固定回显白名单 origin，不能反射请求 Origin: %q", got)
	}
	if strings.Contains(rec.Body.String(), "evil.example.test") {
		t.Fatal("响应体不得回显伪造 Origin")
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Origin") {
		t.Fatalf("CORS 响应缺少 Vary: Origin: %q", got)
	}
}

func TestUsageExportWritesAudit(t *testing.T) {
	f := newTenantFixture(t)
	if rec := f.do("GET", "/console/api/usage/export", f.adminCookie, ""); rec.Code != 200 {
		t.Fatalf("导出应成功: %d %s", rec.Code, rec.Body.String())
	}
	logs := dataOf(t, f.do("GET", "/console/api/audit", f.adminCookie, ""))["logs"].([]any)
	found := false
	for _, raw := range logs {
		if raw.(map[string]any)["action"] == "usage.export" {
			found = true
		}
	}
	if !found {
		t.Fatalf("导出未写入审计: %v", logs)
	}
}
