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
