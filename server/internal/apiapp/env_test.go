package apiapp

import (
	"testing"

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
