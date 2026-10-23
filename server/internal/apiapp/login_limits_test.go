package apiapp

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/ratelimit"
)

func TestNormalizeLoginEmailKey(t *testing.T) {
	cases := map[string]string{
		"Victim+tag@Gmail.com":           "victim@gmail.com",
		"victim.name+tag@googlemail.com": "victimname@gmail.com",
		"victim+tag@corp.com":            "victim@corp.com",
		"victim.name@corp.com":           "victim.name@corp.com",
		"  USER@Example.COM ":            "user@example.com",
		"not-an-email":                   "not-an-email",
	}
	for input, want := range cases {
		if got := normalizeLoginEmailKey(input); got != want {
			t.Fatalf("%q -> %q，期望 %q", input, got, want)
		}
	}
}

func TestLoginIPGlobalBucket(t *testing.T) {
	f := newAPIFixture(t)
	f.deps.LoginIPLimiter = ratelimit.NewInProc(ratelimit.Window{Window: time.Hour, Max: 1})

	rec := f.do("POST", "/console/api/auth/login", "",
		`{"email":"admin@mendao.chat","password":"mendao-admin"}`)
	if rec.Code != 200 {
		t.Fatalf("首个登录应成功: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.do("POST", "/console/api/auth/login", "",
		`{"email":"member@mendao.chat","password":"mendao-member"}`)
	if rec.Code != 429 {
		t.Fatalf("同一 IP 的第二个登录应被全局桶拒绝: %d %s", rec.Code, rec.Body.String())
	}
}

func TestLoginEmailAliasSharesBucket(t *testing.T) {
	f := newAPIFixture(t)
	f.deps.ConsoleLoginLimiter = ratelimit.NewInProc(ratelimit.Window{Window: time.Hour, Max: 1})

	rec := f.do("POST", "/console/api/auth/login", "",
		`{"email":"admin+one@mendao.chat","password":"wrong"}`)
	if rec.Code != 401 {
		t.Fatalf("首个别名登录应进入密码失败: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.do("POST", "/console/api/auth/login", "",
		`{"email":"admin+two@mendao.chat","password":"wrong"}`)
	if rec.Code != 429 {
		t.Fatalf("+tag 别名应共享同一邮箱桶: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRootLoginBucket(t *testing.T) {
	f := newAPIFixture(t)
	f.deps.RootLoginLimiter = ratelimit.NewInProc(ratelimit.Window{Window: time.Hour, Max: 1})

	rec := f.do("POST", "/console/api/auth/login", "",
		`{"email":"admin@mendao.chat","password":"wrong"}`)
	if rec.Code != 401 {
		t.Fatalf("首次 root 失败应返回 401: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.do("POST", "/console/api/auth/login", "",
		`{"email":"admin@mendao.chat","password":"wrong"}`)
	if rec.Code != 429 {
		t.Fatalf("root 第二次尝试应被专属桶拒绝: %d %s", rec.Code, rec.Body.String())
	}
}

func TestLoginBodyAndEmailLimits(t *testing.T) {
	f := newAPIFixture(t)

	hugePassword := strings.Repeat("a", 70<<10)
	rec := f.do("POST", "/console/api/auth/login", "",
		`{"email":"admin@mendao.chat","password":"`+hugePassword+`"}`)
	if rec.Code != 400 {
		t.Fatalf("超过 64 KiB 的登录体应 400: %d", rec.Code)
	}

	longEmail := strings.Repeat("a", 250) + "@x.com"
	rec = f.do("POST", "/console/api/auth/login", "",
		`{"email":"`+longEmail+`","password":"x"}`)
	if rec.Code != 400 {
		t.Fatalf("超过 254 字符的邮箱应 400: %d", rec.Code)
	}
}

func TestLoginSessionTTLByRole(t *testing.T) {
	f := newAPIFixture(t)

	root := f.do("POST", "/console/api/auth/login", "",
		`{"email":"admin@mendao.chat","password":"mendao-admin"}`)
	if root.Code != 200 {
		t.Fatalf("root 登录: %d %s", root.Code, root.Body.String())
	}
	if cookie := root.Header().Get("set-cookie"); !strings.Contains(cookie, "Max-Age=86400") {
		t.Fatalf("root 会话应 24 小时: %s", cookie)
	}

	member := f.do("POST", "/console/api/auth/login", "",
		`{"email":"member@mendao.chat","password":"mendao-member"}`)
	if member.Code != 200 {
		t.Fatalf("member 登录: %d %s", member.Code, member.Body.String())
	}
	if cookie := member.Header().Get("set-cookie"); !strings.Contains(cookie, "Max-Age=604800") {
		t.Fatalf("普通用户会话应 7 天: %s", cookie)
	}
}

func TestSetupHostGuard(t *testing.T) {
	f := newAPIFixture(t)
	f.deps.Env.SetupAllowedHosts = []string{"setup.example.test"}
	f.engine = New(f.deps)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/console/api/setup/status", nil)
	req.Host = "console.example.test"
	f.engine.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("非 setup Host 应 404: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/console/api/setup/status", nil)
	req.Host = "setup.example.test"
	f.engine.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("setup Host 应 200: %d %s", rec.Code, rec.Body.String())
	}
}

// HSTS 只在 https 公开地址下下发；includeSubDomains 改为显式 opt-in
// （apex/www 证书错配时它会锁死用户，见 security.go / env.go 的注释）。
func TestHSTSEmittedForHTTPSOrigin(t *testing.T) {
	f := newAPIFixture(t)
	f.deps.Env.PublicAPIOrigin = "https://console.example.test"
	f.deps.Env.PublicAppOrigin = "https://console.example.test"
	f.deps.Env.HSTSMaxAgeSeconds = 31536000
	f.engine = New(f.deps)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Host = "console.example.test"
	req.Header.Set("x-forwarded-proto", "https")
	f.engine.ServeHTTP(rec, req)
	if got := rec.Header().Get("Strict-Transport-Security"); got != "max-age=31536000" {
		t.Fatalf("HSTS 头不符: %q", got)
	}
}

// 明文本地开发不应收到 HSTS：否则浏览器会记住回环地址的升级要求。
func TestHSTSNotEmittedWithoutHTTPSOrigin(t *testing.T) {
	f := newAPIFixture(t)
	f.deps.Env.HSTSMaxAgeSeconds = 31536000
	f.engine = New(f.deps)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Host = "localhost:8787"
	f.engine.ServeHTTP(rec, req)
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("明文部署不应下发 HSTS，实际 %q", got)
	}
}
