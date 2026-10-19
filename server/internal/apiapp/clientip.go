package apiapp

import (
	"crypto/sha256"
	"crypto/subtle"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/netutil"
)

// resolveLimiterClientIP:与网关同一套可信反代规则。
// TCP 对端不在白名单时忽略 CF-Connecting-IP / X-Forwarded-For / X-Real-IP，
// 防止直连源站的调用者通过伪造转发头切换限流桶。
func resolveLimiterClientIP(c *gin.Context, deps *Deps) string {
	values, present := c.Request.Header["X-Forwarded-For"]
	// CF-Connecting-IP 只在显式配置时采用：普通反向代理不会清洗这个头，
	// 直连代理的客户端可以伪造它来切换限流桶。
	cfConnectingIP := ""
	if deps.Env.TrustCFConnectingIP {
		cfConnectingIP = c.GetHeader("cf-connecting-ip")
	}
	ip := netutil.ResolveClientIP(
		c.Request.RemoteAddr, values, present,
		cfConnectingIP, c.GetHeader("x-real-ip"), deps.Trusted)
	if ip == nil {
		return "unknown"
	}
	return *ip
}

// secretsEqual:比较长度无关的 sha256 摘要，避免 bootstrap secret 的时序侧信道。
func secretsEqual(provided, expected string) bool {
	providedHash := sha256.Sum256([]byte(provided))
	expectedHash := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) == 1
}
