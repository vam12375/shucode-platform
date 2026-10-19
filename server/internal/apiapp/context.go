package apiapp

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/contracts"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/netutil"
	"github.com/vam12375/shucode-platform/server/internal/ratelimit"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// context.ts 的镜像:api 进程的装配依赖。
type Deps struct {
	Store                store.PlatformStore
	Env                  *Env
	Now                  func() time.Time
	SecretBox            *crypto.SecretBox // 内存体验模式且未配置密钥时为 nil
	ConfigEvents         events.Bus
	ConsoleLoginLimiter  ratelimit.Limiter
	PlatformLoginLimiter ratelimit.Limiter
	// LoginIPLimiter:Pure-IP 全局登录桶；RootLoginLimiter:root 专属更紧的桶。
	LoginIPLimiter   ratelimit.Limiter
	RootLoginLimiter ratelimit.Limiter
	// Turnstile:登录页人机验证；nil 表示未配置，登录行为保持原样。
	Turnstile  TurnstileVerifier
	Logger     *slog.Logger
	HTTPClient *http.Client // 上游探测(模型列表/连通测试)
	Trusted    netutil.TrustedProxies
}

// jsonMarshalNoHTML:与 JSON.stringify 一致的紧凑序列化(禁用 <>& 转义)。
func jsonMarshalNoHTML(v any) ([]byte, error) {
	return contracts.EncodeJSONCompact(v)
}

// jsonDecode:JSON 文本 → any(jsonb/KV 内容统一按 JSON 语义处理)。
func jsonDecode(raw []byte) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func okData(data any) gin.H {
	return gin.H{"code": 0, "data": data}
}

func fail(code int, message string) gin.H {
	return gin.H{"code": code, "msg": message}
}
