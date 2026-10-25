package apiapp

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/audit"
	"github.com/vam12375/shucode-platform/server/internal/contracts"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/netutil"
	"github.com/vam12375/shucode-platform/server/internal/ratelimit"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// context.ts 的镜像:api 进程的装配依赖。
type Deps struct {
	Media                *media.Service
	Store                store.PlatformStore
	Env                  *Env
	Now                  func() time.Time
	SecretBox            *crypto.SecretBox // 内存体验模式且未配置密钥时为 nil
	ConfigEvents         events.Bus
	ConsoleLoginLimiter  ratelimit.Limiter
	PlatformLoginLimiter ratelimit.Limiter
	// LoginIPLimiter:Pure-IP 日总量桶；LoginIPBurstLimiter:分钟级突发桶。
	// RootLoginLimiter:root 专属更紧的桶。
	LoginIPLimiter      ratelimit.Limiter
	LoginIPBurstLimiter ratelimit.Limiter
	RootLoginLimiter    ratelimit.Limiter
	// PublicReadLimiter:setup/status、public-config 等匿名只读端点的轻量限速。
	PublicReadLimiter ratelimit.Limiter
	// LoginGate:限制同实例内同时执行 scrypt 的登录数；nil 表示不限。
	LoginGate *LoginGate
	// ModelProbeLimiter:用户端模型实测的按用户桶；nil 表示不限流（测试夹具）。
	ModelProbeLimiter ratelimit.Limiter
	// Turnstile:登录页人机验证；nil 表示未配置，登录行为保持原样。
	Turnstile  TurnstileVerifier
	Logger     *slog.Logger
	HTTPClient *http.Client // 上游探测(模型列表/连通测试)
	Trusted    netutil.TrustedProxies
	// AuditSink:审计入库失败时的本地可靠 outbox；nil 时退回旧的最佳努力行为。
	AuditSink *audit.Sink
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
