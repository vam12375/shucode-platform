package apiapp

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/netutil"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestHealthzAndStaticHosting(t *testing.T) {
	f := newAPIFixture(t)
	// 无静态目录时不注册(其余测试即覆盖);此处单独构造 dist。
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html>console</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "assets", "app-abcdef12.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.deps.Env.ConsoleDistDir = dist
	engine := New(f.deps)

	health := httptest.NewRecorder()
	engine.ServeHTTP(health, httptest.NewRequest("GET", "/healthz", nil))
	if health.Code != 200 || !strings.Contains(health.Body.String(), `"memoryDb":true`) {
		t.Fatalf("healthz 不符: %d %s", health.Code, health.Body.String())
	}

	// TS 路由对尾斜杠直接 404,不能继承 Gin 默认的 301 去斜杠重定向。
	for _, path := range []string{"/healthz/", "/n/configs/"} {
		trailing := httptest.NewRecorder()
		engine.ServeHTTP(trailing, httptest.NewRequest("GET", path, nil))
		if trailing.Code != 404 {
			t.Fatalf("%s 应 404(不跟随尾斜杠重定向): %d", path, trailing.Code)
		}
	}

	redirect := httptest.NewRecorder()
	engine.ServeHTTP(redirect, httptest.NewRequest("GET", "/console", nil))
	if redirect.Code != 302 || redirect.Header().Get("location") != "/console/" {
		t.Fatalf("/console 应 302 到 /console/: %d %s", redirect.Code, redirect.Header().Get("location"))
	}

	index := httptest.NewRecorder()
	engine.ServeHTTP(index, httptest.NewRequest("GET", "/console/", nil))
	if index.Code != 200 || !strings.Contains(index.Body.String(), "console") ||
		index.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("index 托管不符: %d %q", index.Code, index.Header().Get("Cache-Control"))
	}

	asset := httptest.NewRecorder()
	engine.ServeHTTP(asset, httptest.NewRequest("GET", "/console/assets/app-abcdef12.js", nil))
	if asset.Code != 200 || !strings.Contains(asset.Header().Get("Content-Type"), "javascript") ||
		!strings.Contains(asset.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("静态资源响应头不符: %d %v", asset.Code, asset.Header())
	}

	// SPA fallback:未知路径回落 index.html。
	fallback := httptest.NewRecorder()
	engine.ServeHTTP(fallback, httptest.NewRequest("GET", "/console/teams/42", nil))
	if fallback.Code != 200 || !strings.Contains(fallback.Body.String(), "console") {
		t.Fatalf("SPA fallback 失败: %d", fallback.Code)
	}

	// 路径穿越防护。
	traversal := httptest.NewRecorder()
	engine.ServeHTTP(traversal, httptest.NewRequest("GET", "/console/../go.mod", nil))
	if traversal.Code != 404 {
		t.Fatalf("穿越路径应 404: %d", traversal.Code)
	}
}

func TestConsoleCORS(t *testing.T) {
	f := newAPIFixture(t)
	f.deps.Env.PublicConsoleOrigin = "https://console.example.com"
	engine := New(f.deps)

	preflight := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/console/api/auth/me", nil)
	req.Header.Set("origin", "https://console.example.com")
	req.Header.Set("access-control-request-headers", "content-type, cookie")
	engine.ServeHTTP(preflight, req)
	if preflight.Code != 204 ||
		preflight.Header().Get("Access-Control-Allow-Origin") != "https://console.example.com" ||
		preflight.Header().Get("Access-Control-Allow-Credentials") != "true" ||
		!strings.Contains(preflight.Header().Get("Access-Control-Allow-Headers"), "content-type") {
		t.Fatalf("CORS 预检不符: %d %v", preflight.Code, preflight.Header())
	}
	// 非 /console/api 路径不附加 CORS 头。
	other := httptest.NewRecorder()
	engine.ServeHTTP(other, httptest.NewRequest("GET", "/api/v1/client/configs", nil))
	if other.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("公开客户端路径不应带 CORS 头")
	}
}

func TestConfigKVAndPublicEnvelope(t *testing.T) {
	f := newAPIFixture(t)
	// 本用例验证配置透传；强制更新本身需要显式启用真实更新清单。
	f.deps.Env.ReleaseManifestEnabled = true
	adminCookie := f.loginAdmin()
	put := f.do("PUT", "/console/api/config", adminCookie,
		`{"help":{"feedback_url":"https://help.example.com","feedback_use_external_form":true},"forceUpdate":{"minimalVersion":" 9.9.9 "}}`)
	if put.Code != 200 {
		t.Fatalf("写配置: %d %s", put.Code, put.Body.String())
	}
	publicRec := f.do("GET", "/api/v1/client/configs", "", "")
	if publicRec.Code != 200 {
		t.Fatalf("公开配置: %d", publicRec.Code)
	}
	configs := dataOf(t, publicRec)["configs"].(map[string]any)
	help := configs["feedbackUrl"].(map[string]any)
	if help["feedback_url"] != "https://help.example.com" {
		t.Fatalf("help 透传不符: %v", help)
	}
	force := configs["forceUpdate"].(map[string]any)
	if force["minimalVersion"] != "9.9.9" {
		t.Fatalf("强制更新版本应 trim: %v", force)
	}
	consoleCfg := dataOf(t, f.do("GET", "/console/api/config", adminCookie, ""))
	if consoleCfg["help"] == nil || consoleCfg["forceUpdate"] == nil {
		t.Fatalf("控制台配置读取不符: %v", consoleCfg)
	}
}

func TestForceUpdateRejectedUntilManifestEnabled(t *testing.T) {
	f := newAPIFixture(t)
	adminCookie := f.loginAdmin()
	rejected := f.do("PUT", "/console/api/config", adminCookie,
		`{"forceUpdate":{"minimalVersion":"9.9.9"}}`)
	if rejected.Code != 400 || !strings.Contains(rejected.Body.String(), "更新清单") {
		t.Fatalf("未接入更新清单时应拒绝强制更新: %d %s", rejected.Code, rejected.Body.String())
	}
	// 数据库里即使残留旧版本号，未启用更新清单时公开配置也不得下发。
	if err := f.store.SetConfigValue(context.Background(), "forceUpdate",
		[]byte(`{"minimalVersion":"9.9.9"}`)); err != nil {
		t.Fatal(err)
	}
	publicRec := f.do("GET", "/api/v1/client/configs", "", "")
	configs := dataOf(t, publicRec)["configs"].(map[string]any)
	if _, exists := configs["forceUpdate"]; exists {
		t.Fatalf("未启用更新清单时不应下发 forceUpdate: %v", configs)
	}
	f.deps.Env.ReleaseManifestEnabled = true
	accepted := f.do("PUT", "/console/api/config", adminCookie,
		`{"forceUpdate":{"minimalVersion":"9.9.9"}}`)
	if accepted.Code != 200 {
		t.Fatalf("启用更新清单后应允许强制更新: %d %s", accepted.Code, accepted.Body.String())
	}
	publicRec = f.do("GET", "/api/v1/client/configs", "", "")
	configs = dataOf(t, publicRec)["configs"].(map[string]any)
	if configs["forceUpdate"] == nil {
		t.Fatalf("启用更新清单后应下发 forceUpdate: %v", configs)
	}
}

func TestSecurityHeadersAndCSRFGuard(t *testing.T) {
	f := newAPIFixture(t)

	health := f.do("GET", "/healthz", "", "")
	if health.Header().Get("X-Content-Type-Options") != "nosniff" ||
		health.Header().Get("X-Frame-Options") != "DENY" ||
		health.Header().Get("Referrer-Policy") != "no-referrer" ||
		!strings.Contains(health.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("安全响应头缺失: %v", health.Header())
	}
	if health.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("HTTP 公开地址不应下发 HSTS")
	}

	// 跨站写请求：即使 cookie 存在也拒绝。
	loginBody := `{"email":"admin@mendao.chat","password":"mendao-admin"}`
	req := httptest.NewRequest("POST", "/console/api/auth/login", strings.NewReader(loginBody))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", "https://evil.example")
	req.Header.Set("sec-fetch-site", "cross-site")
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("跨站写请求应 403: %d %s", rec.Code, rec.Body.String())
	}

	// 同源请求（Vite dev 代理场景）允许。
	req = httptest.NewRequest("POST", "/console/api/auth/login", strings.NewReader(loginBody))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", "http://localhost:5177")
	req.Header.Set("sec-fetch-site", "same-origin")
	rec = httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("同源写请求应放行: %d %s", rec.Code, rec.Body.String())
	}
}

func TestHTTPSEnforcementWhenPublicOriginIsHTTPS(t *testing.T) {
	f := newAPIFixture(t)
	f.deps.Env.PublicAPIOrigin = "https://api.example.com"
	f.deps.Env.PublicAppOrigin = "https://api.example.com"
	f.deps.Env.SecureCookies = true
	engine := New(f.deps)

	req := httptest.NewRequest("GET", "/api/v1/client/configs", nil)
	req.Host = "api.example.com"
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != 308 || rec.Header().Get("Location") != "https://api.example.com/api/v1/client/configs" {
		t.Fatalf("明文 GET 应 308: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	req = httptest.NewRequest("POST", "/console/api/auth/login",
		strings.NewReader(`{"email":"admin@mendao.chat","password":"mendao-admin"}`))
	req.Host = "api.example.com"
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-forwarded-proto", "http")
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("明文写请求应 403: %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest("GET", "/api/v1/client/configs", nil)
	req.Host = "api.example.com"
	req.Header.Set("x-forwarded-proto", "https")
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("反代声明 https 后应放行: %d %s", rec.Code, rec.Body.String())
	}
}

func TestLoginLimiterIgnoresSpoofedForwardedHeaders(t *testing.T) {
	f := newAPIFixture(t)
	doLogin := func(forwarded string) int {
		req := httptest.NewRequest("POST", "/console/api/auth/login",
			strings.NewReader(`{"email":"nobody@mendao.chat","password":"wrong-password"}`))
		req.Header.Set("content-type", "application/json")
		if forwarded != "" {
			req.Header.Set("x-forwarded-for", forwarded)
		}
		rec := httptest.NewRecorder()
		f.engine.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 0; i < 10; i++ {
		if code := doLogin("203.0.113." + strings.Repeat("1", i%3+1)); code != 401 {
			t.Fatalf("前 10 次应为 401: 第 %d 次 %d", i+1, code)
		}
	}
	if code := doLogin("198.51.100.9"); code != 429 {
		t.Fatalf("不可信对端的 XFF 不应切换限流桶，应 429: %d", code)
	}

	// 显式可信的 TCP 对端才解析转发头：不同 XFF 进入不同桶。
	f2 := newAPIFixture(t)
	f2.deps.Trusted = netutil.NewTrustedProxies([]string{"192.0.2.1"})
	forwarded := []string{"203.0.113.1", "203.0.113.2", "203.0.113.3", "203.0.113.4", "203.0.113.5"}
	for i := range forwarded {
		req := httptest.NewRequest("POST", "/console/api/auth/login",
			strings.NewReader(`{"email":"nobody@mendao.chat","password":"wrong-password"}`))
		req.Header.Set("content-type", "application/json")
		req.Header.Set("x-forwarded-for", forwarded[i])
		rec := httptest.NewRecorder()
		f2.engine.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("可信代理的不同客户端应各自计数: %d %s", rec.Code, rec.Body.String())
		}
	}

	// 可信代理不会清洗 CF-Connecting-IP：默认必须忽略它，按固定 XFF 桶计数。
	f3 := newAPIFixture(t)
	f3.deps.Trusted = netutil.NewTrustedProxies([]string{"192.0.2.1"})
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("POST", "/console/api/auth/login",
			strings.NewReader(`{"email":"nobody@mendao.chat","password":"wrong-password"}`))
		req.Header.Set("content-type", "application/json")
		req.Header.Set("x-forwarded-for", "203.0.113.50")
		req.Header.Set("cf-connecting-ip", "198.51.100."+string(rune('1'+i%9)))
		rec := httptest.NewRecorder()
		f3.engine.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("默认信任模式下前 10 次应为 401: %d", rec.Code)
		}
	}
	req := httptest.NewRequest("POST", "/console/api/auth/login",
		strings.NewReader(`{"email":"nobody@mendao.chat","password":"wrong-password"}`))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-forwarded-for", "203.0.113.50")
	req.Header.Set("cf-connecting-ip", "198.51.100.99")
	rec := httptest.NewRecorder()
	f3.engine.ServeHTTP(rec, req)
	if rec.Code != 429 {
		t.Fatalf("CF-Connecting-IP 不应绕过默认限流桶: %d", rec.Code)
	}
}

type failingLimiter struct{}

func (failingLimiter) Allow(context.Context, string) (bool, error) {
	return false, errors.New("limiter backend down")
}

func TestConsoleLoginFailsClosedWhenLimiterUnavailable(t *testing.T) {
	f := newAPIFixture(t)
	f.deps.ConsoleLoginLimiter = failingLimiter{}
	rec := f.do("POST", "/console/api/auth/login", "",
		`{"email":"admin@mendao.chat","password":"mendao-admin"}`)
	if rec.Code != 503 {
		t.Fatalf("管理面限流后端故障应 fail-closed: %d %s", rec.Code, rec.Body.String())
	}
}

func TestProviderBaseURLValidation(t *testing.T) {
	cases := []struct {
		raw       string
		allowHTTP bool
		want      string
		wantErr   bool
	}{
		{"https://api.example.com/v1", false, "https://api.example.com", false},
		{"https://api.example.com/api/v3/", false, "https://api.example.com/api/v3", false},
		{"http://api.example.com", false, "", true},
		{"http://127.0.0.1:8080", true, "http://127.0.0.1:8080", false},
		{"https://user:pass@api.example.com", false, "", true},
		{"https://api.example.com?x=1", false, "", true},
		{"ftp://api.example.com", false, "", true},
	}
	for _, tc := range cases {
		got, err := validateUpstreamBaseURL(tc.raw, tc.allowHTTP)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Fatalf("%s: got=%q err=%v want=%q wantErr=%v", tc.raw, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestRootSetupRequiresBootstrapSecret(t *testing.T) {
	f := newAPIEmptyFixture(t)
	f.deps.Env.MemoryDB = false
	f.deps.Env.BootstrapSecret = ""
	body := `{"email":"root@mendao.chat","displayName":"Root","password":"root-password-123"}`
	if rec := f.do("POST", "/console/api/setup/root", "", body); rec.Code != 503 {
		t.Fatalf("未配置 bootstrap secret 应拒绝初始化: %d %s", rec.Code, rec.Body.String())
	}

	f.deps.Env.BootstrapSecret = "bootstrap-test-secret"
	if rec := f.do("POST", "/console/api/setup/root", "", body); rec.Code != 403 {
		t.Fatalf("缺失/错误 bootstrap secret 应 403: %d %s", rec.Code, rec.Body.String())
	}
	req := httptest.NewRequest("POST", "/console/api/setup/root", strings.NewReader(body))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-bootstrap-secret", "bootstrap-test-secret")
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("正确 bootstrap secret 应允许初始化: %d %s", rec.Code, rec.Body.String())
	}
}

func TestUsageSeriesBuckets(t *testing.T) {
	f := newAPIFixture(t)
	adminCookie := f.loginAdmin()
	ctx := context.Background()
	teams, _ := f.store.ListTeams(ctx)
	teamID := teams[0].Team.ID
	member, _ := f.store.FindUserByEmail(ctx, "member@mendao.chat")
	// 上海墙钟日界:15:59:59Z 属 17 日,16:00:00Z 属 18 日。
	for i, at := range []time.Time{
		time.Date(2026, 10, 17, 15, 59, 59, 0, time.UTC),
		time.Date(2026, 10, 17, 16, 0, 0, 0, time.UTC),
	} {
		if err := f.store.InsertUsage(ctx, store.UsageRecord{
			ID: uuid.NewString(), RequestID: "series-" + time.Duration(i).String(),
			UserID: member.ID, TeamID: teamID, Model: "m-s", UpstreamModel: "m-s",
			CreditsMicro: 100, Status: store.UsageStatusOk, CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	rec := f.do("GET", "/console/api/usage/series?granularity=day&teamId="+teamID, adminCookie, "")
	if rec.Code != 200 {
		t.Fatalf("series: %d %s", rec.Code, rec.Body.String())
	}
	series := dataOf(t, rec)["series"].([]any)
	if len(series) != 2 {
		t.Fatalf("应有两个日桶: %v", series)
	}
	first := series[0].(map[string]any)
	second := series[1].(map[string]any)
	if first["bucketStart"] != "2026-10-17T00:00:00" || second["bucketStart"] != "2026-10-18T00:00:00" {
		t.Fatalf("上海墙钟日桶不符: %v", series)
	}
	// 小时粒度 + 31 天上限。
	if rec := f.do("GET",
		"/console/api/usage/series?granularity=hour&from=2026-09-01T00:00:00&to=2026-10-20T00:00:00",
		adminCookie, ""); rec.Code != 400 {
		t.Fatalf("小时粒度超 31 天应 400: %d", rec.Code)
	}
}
