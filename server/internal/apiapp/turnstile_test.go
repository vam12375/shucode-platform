package apiapp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"github.com/vam12375/shucode-platform/server/internal/turnstile"
)

type fakeTurnstile struct {
	calls    int
	token    string
	remoteIP string
	err      error
}

func (f *fakeTurnstile) Verify(_ context.Context, token, remoteIP string) error {
	f.calls++
	f.token = token
	f.remoteIP = remoteIP
	return f.err
}

func TestConsoleLoginRequiresTurnstileWhenEnabled(t *testing.T) {
	f := newAPIFixture(t)
	fake := &fakeTurnstile{}
	f.deps.Turnstile = fake

	// 缺少 token 时不调用 siteverify，直接拒绝。
	rec := f.do("POST", "/console/api/auth/login", "",
		`{"email":"admin@mendao.chat","password":"mendao-admin"}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "人机验证") {
		t.Fatalf("缺少 token 应 400: %d %s", rec.Code, rec.Body.String())
	}
	if fake.calls != 0 {
		t.Fatal("缺少 token 不应调用 siteverify")
	}

	fake.err = turnstile.ErrVerificationFailed
	rec = f.do("POST", "/console/api/auth/login", "",
		`{"email":"admin@mendao.chat","password":"mendao-admin","turnstileToken":"bad-token"}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "人机验证") {
		t.Fatalf("无效 token 应 400: %d %s", rec.Code, rec.Body.String())
	}
	if fake.calls != 1 || fake.token != "bad-token" || fake.remoteIP == "" {
		t.Fatalf("siteverify 参数不符: %+v", fake)
	}

	fake.err = turnstile.ErrUnavailable
	rec = f.do("POST", "/console/api/auth/login", "",
		`{"email":"admin@mendao.chat","password":"mendao-admin","turnstileToken":"token-2"}`)
	if rec.Code != 503 {
		t.Fatalf("siteverify 不可用应 503: %d %s", rec.Code, rec.Body.String())
	}

	fake.err = nil
	rec = f.do("POST", "/console/api/auth/login", "",
		`{"email":"admin@mendao.chat","password":"mendao-admin","turnstileToken":"token-3"}`)
	if rec.Code != 200 {
		t.Fatalf("token 有效时登录应成功: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPublicConfigExposesTurnstileSiteKey(t *testing.T) {
	f := newAPIFixture(t)
	f.deps.Env.TurnstileSiteKey = "1x00000000000000000000AA"
	f.deps.Turnstile = &fakeTurnstile{}

	rec := f.do("GET", "/console/api/public-config", "", "")
	if rec.Code != 200 {
		t.Fatalf("public-config: %d %s", rec.Code, rec.Body.String())
	}
	data := dataOf(t, rec)
	if data["turnstileSiteKey"] != "1x00000000000000000000AA" || data["turnstileEnabled"] != true {
		t.Fatalf("public-config 内容不符: %v", data)
	}
}

func TestPlatformLoginPageRendersAndEnforcesTurnstile(t *testing.T) {
	f := newAPIFixture(t)
	f.deps.Env.TurnstileSiteKey = "1x00000000000000000000AA"
	flowID := uuid.NewString()
	now := time.Now()
	if err := f.store.CreateLoginFlow(context.Background(), store.LoginFlowRecord{
		ID:            flowID,
		PollTokenHash: crypto.HashToken("poll-token-" + flowID),
		Status:        store.FlowStatusPending,
		DeviceName:    "测试设备",
		CreatedAt:     now,
		ExpiresAt:     now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	rec := f.do("GET", "/app/platform/login?flow_id="+flowID, "", "")
	if rec.Code != 200 {
		t.Fatalf("登录页: %d %s", rec.Code, rec.Body.String())
	}
	html := rec.Body.String()
	for _, want := range []string{
		`class="turnstile cf-turnstile"`,
		`data-sitekey="1x00000000000000000000AA"`,
		`data-action="login"`,
		"https://challenges.cloudflare.com/turnstile/v0/api.js",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("登录页缺少 %q", want)
		}
	}

	fake := &fakeTurnstile{}
	f.deps.Turnstile = fake
	rec = f.form("/app/platform/login", "", map[string]string{
		"flow_id":  flowID,
		"email":    "admin@mendao.chat",
		"password": "mendao-admin",
	})
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "人机验证") {
		t.Fatalf("表单缺少 token 应 400: %d %s", rec.Code, rec.Body.String())
	}
	if fake.calls != 0 {
		t.Fatal("表单缺少 token 不应调用 siteverify")
	}
}

func TestCSPAllowsTurnstileOnlyWhenConfigured(t *testing.T) {
	f := newAPIFixture(t)

	without := f.do("GET", "/healthz", "", "")
	csp := without.Header().Get("Content-Security-Policy")
	if strings.Contains(csp, "challenges.cloudflare.com") {
		t.Fatalf("未配置时不应放宽 CSP: %s", csp)
	}

	f.deps.Env.TurnstileSiteKey = "1x00000000000000000000AA"
	with := f.do("GET", "/healthz", "", "")
	csp = with.Header().Get("Content-Security-Policy")
	for _, want := range []string{
		"script-src 'self' https://challenges.cloudflare.com",
		"connect-src 'self' https://challenges.cloudflare.com",
		"frame-src 'self' https://challenges.cloudflare.com",
	} {
		if !strings.Contains(csp, want) {
			t.Fatalf("启用后 CSP 缺少 %q: %s", want, csp)
		}
	}
}
