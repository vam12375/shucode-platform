package apiapp

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本轮安全加固的回归用例。这些断言直接对应线上实测到的缺陷：
//   - nginx `return` 短路导致跳转响应 0 个安全头 → 入口跳转改由应用出；
//   - HSTS includeSubDomains 在 apex/www 证书错配时会把用户锁死 → 改为 env 驱动且默认关闭；
//   - CSRF 守卫「没有 Origin 就放行」→ 带会话 Cookie 的写操作必须证明来源；
//   - 设备授权页内联脚本 → 改用外部脚本，CSP 收掉 'unsafe-inline'。

func headerOf(rec *httptest.ResponseRecorder, name string) string {
	return rec.Header().Get(name)
}

// withConsoleDist 给夹具挂一个最小控制台产物目录，让静态托管中间件生效。
// 入口跳转（/ 与 /console）由该中间件负责，所以这条用例必须有它。
func withConsoleDist(t *testing.T, f *apiFixture) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>t</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.deps.Env.ConsoleDistDir = dir
	f.engine = New(f.deps)
}

func TestConsoleEntryRedirectCarriesSecurityHeaders(t *testing.T) {
	f := newAPIFixture(t)
	withConsoleDist(t, f)
	for _, path := range []string{"/", "/console"} {
		rec := f.do("GET", path, "", "")
		if rec.Code != http.StatusFound {
			t.Fatalf("%s: 期望 302，实际 %d", path, rec.Code)
		}
		if loc := headerOf(rec, "location"); loc != "/console/" {
			t.Fatalf("%s: Location=%q", path, loc)
		}
		// 这四个头是 nginx 短路时曾经全部丢失的。
		for _, name := range []string{
			"Content-Security-Policy", "X-Frame-Options",
			"X-Content-Type-Options", "Referrer-Policy",
		} {
			if headerOf(rec, name) == "" {
				t.Fatalf("%s 的 3xx 响应缺少 %s", path, name)
			}
		}
	}
}

func TestHSTSDefaultsToNoIncludeSubdomains(t *testing.T) {
	f := newAPIFixture(t)
	// 换成 https 公开地址并重建引擎，让 hstsValue 按新配置重新计算。
	f.deps.Env.PublicAPIOrigin = "https://console.example.com"
	f.deps.Env.PublicAppOrigin = "https://console.example.com"
	f.deps.Env.HSTSMaxAgeSeconds = 31536000
	f.deps.Env.HSTSIncludeSubdomains = false
	engine := New(f.deps)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Host = "console.example.com"
	req.Header.Set("x-forwarded-proto", "https")
	engine.ServeHTTP(rec, req)

	hsts := headerOf(rec, "Strict-Transport-Security")
	if hsts == "" {
		t.Fatal("https 公开地址下应下发 HSTS")
	}
	if strings.Contains(hsts, "includeSubDomains") {
		t.Fatalf("默认不得下发 includeSubDomains（会锁死证书错配的子域），实际 %q", hsts)
	}
	if !strings.Contains(hsts, "max-age=31536000") {
		t.Fatalf("max-age 未按配置下发：%q", hsts)
	}

	// 显式打开后才允许出现 includeSubDomains。
	f.deps.Env.HSTSIncludeSubdomains = true
	engine = New(f.deps)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/healthz", nil)
	req.Host = "console.example.com"
	req.Header.Set("x-forwarded-proto", "https")
	engine.ServeHTTP(rec, req)
	if hsts := headerOf(rec, "Strict-Transport-Security"); !strings.Contains(hsts, "includeSubDomains") {
		t.Fatalf("显式开启后应包含 includeSubDomains：%q", hsts)
	}
}

func TestCSRFGuardRejectsCookieWritesWithoutProvenance(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()

	body := `{"name":"csrf-probe","baseUrl":"https://example.com","protocol":"openai-chat-completions"}`

	// 1. 有会话 Cookie、既无 Origin 也无 Sec-Fetch-Site → 必须拒绝。
	rec := f.doForge("POST", "/console/api/providers", cookie, body, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("缺来源证明的写操作应 403，实际 %d: %s", rec.Code, rec.Body.String())
	}

	// 2. 同站（子域互打）也必须拒绝：SameSite 是 site 粒度，挡不住这个。
	rec = f.doForge("POST", "/console/api/providers", cookie, body,
		map[string]string{"sec-fetch-site": "same-site"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("same-site 写操作应 403，实际 %d", rec.Code)
	}

	// 3. 跨站同样拒绝。
	rec = f.doForge("POST", "/console/api/providers", cookie, body,
		map[string]string{"origin": "https://evil.example.com"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨站 Origin 应 403，实际 %d", rec.Code)
	}

	// 4. 正常浏览器请求放行。
	rec = f.do("POST", "/console/api/providers", cookie, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("同源写操作应通过，实际 %d: %s", rec.Code, rec.Body.String())
	}

	// 5. 显式 API 凭据不走 Cookie，不存在 CSRF，无需来源证明。
	rec = f.doForge("POST", "/api/v1/platform/auth/login", "",
		`{"email":"a@b.co","password":"x"}`,
		map[string]string{"x-api-key": "some-key"})
	if rec.Code == http.StatusForbidden {
		t.Fatalf("Bearer/x-api-key 调用不应被 CSRF 守卫拦截")
	}
}

func TestAppPagesDoNotRequireUnsafeInlineScript(t *testing.T) {
	f := newAPIFixture(t)
	rec := f.do("GET", "/app/platform/login", "", "")
	csp := headerOf(rec, "Content-Security-Policy")
	if csp == "" {
		t.Fatal("缺少 CSP")
	}
	scriptDir := csp
	if idx := strings.Index(csp, "script-src"); idx >= 0 {
		scriptDir = csp[idx:]
		if end := strings.Index(scriptDir, ";"); end > 0 {
			scriptDir = scriptDir[:end]
		}
	}
	if strings.Contains(scriptDir, "unsafe-inline") {
		t.Fatalf("设备授权页不应再需要 script-src 'unsafe-inline'：%q", scriptDir)
	}
	if !strings.Contains(csp, "object-src 'none'") {
		t.Fatalf("CSP 缺少 object-src 'none'：%q", csp)
	}
}

func TestDeviceRedirectScriptIsServedWithoutInline(t *testing.T) {
	f := newAPIFixture(t)
	rec := f.do("GET", "/app/redirect.js", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("redirect.js 应 200，实际 %d", rec.Code)
	}
	if ct := headerOf(rec, "content-type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("content-type 异常：%q", ct)
	}
	if !strings.Contains(rec.Body.String(), "data-redirect") {
		t.Fatal("脚本应读取 body[data-redirect]")
	}
}

func TestSecurityTxtOnlyServedWhenContactConfigured(t *testing.T) {
	f := newAPIFixture(t)
	if rec := f.do("GET", "/.well-known/security.txt", "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("未配置 SECURITY_CONTACT 时应 404，实际 %d", rec.Code)
	}

	f.deps.Env.SecurityContact = "mailto:security@example.com"
	engine := New(f.deps)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest("GET", "/.well-known/security.txt", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("配置后应 200，实际 %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Contact: mailto:security@example.com", "Expires:", "Preferred-Languages:"} {
		if !strings.Contains(body, want) {
			t.Fatalf("security.txt 缺少 %q：\n%s", want, body)
		}
	}
}
