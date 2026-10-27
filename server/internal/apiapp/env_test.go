package apiapp

import (
	"testing"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/turnstile"
)

func TestLoadPlatformEnvRequiresTurnstileKeyPair(t *testing.T) {
	t.Setenv("PORT", "8787")
	t.Setenv("MEMORY_DB", "1")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("TURNSTILE_VERIFY_URL", "")

	t.Setenv("TURNSTILE_SITE_KEY", "site-key")
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	if _, err := LoadPlatformEnv(); err == nil {
		t.Fatal("只配置 site key 应拒绝启动")
	}

	t.Setenv("TURNSTILE_SITE_KEY", "")
	t.Setenv("TURNSTILE_SECRET_KEY", "secret-key")
	if _, err := LoadPlatformEnv(); err == nil {
		t.Fatal("只配置 secret key 应拒绝启动")
	}

	t.Setenv("TURNSTILE_SITE_KEY", "site-key")
	t.Setenv("TURNSTILE_SECRET_KEY", "secret-key")
	env, err := LoadPlatformEnv()
	if err != nil {
		t.Fatalf("成对配置应通过: %v", err)
	}
	if env.TurnstileSiteKey != "site-key" || env.TurnstileSecretKey != "secret-key" {
		t.Fatalf("Turnstile 配置未加载: %+v", env)
	}
	if env.TurnstileVerifyURL != turnstile.DefaultEndpoint {
		t.Fatalf("默认 siteverify 地址不符: %s", env.TurnstileVerifyURL)
	}
}

func TestLoadPlatformEnvSecurityDefaultsAndValidation(t *testing.T) {
	t.Setenv("PORT", "8787")
	t.Setenv("MEMORY_DB", "1")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("TURNSTILE_SITE_KEY", "")
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	t.Setenv("ROOT_MFA_REQUIRED", "")
	t.Setenv("SESSION_IDLE_MINUTES", "")
	t.Setenv("LOGIN_MAX_CONCURRENT", "")
	t.Setenv("CSP_REPORT_URI", "")

	env, err := LoadPlatformEnv()
	if err != nil {
		t.Fatalf("默认配置应通过: %v", err)
	}
	if env.RootMFARequired {
		t.Fatal("root MFA 默认应关闭")
	}
	if env.SessionIdleTimeout != 30*time.Minute {
		t.Fatalf("会话空闲超时默认值不符: %v", env.SessionIdleTimeout)
	}
	if env.LoginMaxConcurrent != 8 {
		t.Fatalf("登录并发默认值不符: %d", env.LoginMaxConcurrent)
	}
	if env.CSPReportURI != "" {
		t.Fatalf("CSP 上报地址默认应为空: %q", env.CSPReportURI)
	}

	t.Setenv("ROOT_MFA_REQUIRED", "true")
	t.Setenv("SESSION_IDLE_MINUTES", "0")
	t.Setenv("LOGIN_MAX_CONCURRENT", "4")
	t.Setenv("CSP_REPORT_URI", "https://reports.example.test/csp")
	env, err = LoadPlatformEnv()
	if err != nil {
		t.Fatalf("显式配置应通过: %v", err)
	}
	if !env.RootMFARequired || env.SessionIdleTimeout != 0 || env.LoginMaxConcurrent != 4 ||
		env.CSPReportURI != "https://reports.example.test/csp" {
		t.Fatalf("安全配置未正确加载: %+v", env)
	}

	for _, item := range []struct {
		key, value string
	}{
		{"SESSION_IDLE_MINUTES", "-1"},
		{"LOGIN_MAX_CONCURRENT", "0"},
		{"CSP_REPORT_URI", "javascript:alert(1)"},
		{"CSP_REPORT_URI", "https://reports.example.test/a;b"},
	} {
		t.Setenv("SESSION_IDLE_MINUTES", "30")
		t.Setenv("LOGIN_MAX_CONCURRENT", "8")
		t.Setenv("CSP_REPORT_URI", "")
		t.Setenv(item.key, item.value)
		if _, err := LoadPlatformEnv(); err == nil {
			t.Fatalf("%s=%q 应拒绝启动", item.key, item.value)
		}
	}
}

func TestLoadPlatformEnvMediaCDNOrigin(t *testing.T) {
	t.Setenv("PORT", "8787")
	t.Setenv("MEMORY_DB", "1")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("TURNSTILE_SITE_KEY", "")
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	t.Setenv("PUBLIC_API_ORIGIN", "https://api.example.test")
	t.Setenv("PUBLIC_MEDIA_CDN_ORIGIN", "https://cdn.example.test/media/ignored")

	env, err := LoadPlatformEnv()
	if err != nil {
		t.Fatalf("媒体 CDN origin 应通过: %v", err)
	}
	if env.PublicAPIOrigin != "https://api.example.test" {
		t.Fatalf("API origin 被错误修改: %q", env.PublicAPIOrigin)
	}
	if env.PublicMediaCDNOrigin != "https://cdn.example.test" {
		t.Fatalf("媒体 CDN origin 未规范化: %q", env.PublicMediaCDNOrigin)
	}

	t.Setenv("PUBLIC_MEDIA_CDN_ORIGIN", "")
	env, err = LoadPlatformEnv()
	if err != nil {
		t.Fatalf("未配置媒体 CDN origin 应回退 API origin: %v", err)
	}
	if env.PublicMediaCDNOrigin != env.PublicAPIOrigin {
		t.Fatalf("媒体 origin 未回退 API origin: media=%q api=%q", env.PublicMediaCDNOrigin, env.PublicAPIOrigin)
	}
	for _, value := range []string{"http://localhost:8787", "http://127.0.0.1:8787", "http://[::1]:8787"} {
		t.Setenv("PUBLIC_MEDIA_CDN_ORIGIN", value)
		env, err = LoadPlatformEnv()
		if err != nil || env.PublicMediaCDNOrigin != value {
			t.Fatalf("本机 HTTP 媒体 origin 应仅用于开发且允许: origin=%q err=%v", value, err)
		}
	}

	for _, value := range []string{
		"javascript:alert(1)",
		"https://user:pass@cdn.example.test",
		"https://",
		"http://cdn.example.test",
		"http://localhost.example.test",
		"http://127.0.0.2",
	} {
		t.Setenv("PUBLIC_MEDIA_CDN_ORIGIN", value)
		if _, err := LoadPlatformEnv(); err == nil {
			t.Fatalf("PUBLIC_MEDIA_CDN_ORIGIN=%q 应拒绝启动", value)
		}
	}
}
