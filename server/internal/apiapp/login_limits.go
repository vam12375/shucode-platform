package apiapp

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/ratelimit"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// 登录限流窗口与拒绝提示保持一致：账号桶 5 分钟、IP 突发 1 分钟、
// IP 日桶 24 小时、root 专属桶 1 小时。
const (
	loginAccountWindow = 5 * time.Minute
	loginIPBurstWindow = time.Minute
	loginIPDailyWindow = 24 * time.Hour
	rootLoginWindow    = time.Hour
)

// setRetryAfter 把窗口时长换算成 HTTP Retry-After（秒，向上取整）。
func setRetryAfter(c *gin.Context, window time.Duration) {
	seconds := int(math.Ceil(window.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	c.Header("Retry-After", strconv.Itoa(seconds))
}

// writeLoginRateLimited 统一登录拒绝路径：429 + Retry-After。
func writeLoginRateLimited(c *gin.Context, message string, window time.Duration) {
	setRetryAfter(c, window)
	c.JSON(429, fail(429, message))
}

// normalizeLoginEmailKey 只用于限流桶，不改变实际登录查询的邮箱：
// 去掉 +tag；Gmail 忽略点号并把 googlemail.com 归一为 gmail.com。
func normalizeLoginEmailKey(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndexByte(email, '@')
	if at <= 0 || at == len(email)-1 {
		return email
	}
	local, domain := email[:at], email[at+1:]
	if plus := strings.IndexByte(local, '+'); plus >= 0 {
		local = local[:plus]
	}
	if domain == "googlemail.com" {
		domain = "gmail.com"
	}
	if domain == "gmail.com" {
		local = strings.ReplaceAll(local, ".", "")
	}
	return local + "@" + domain
}

// checkLoginRateLimits 按「账号 → IP 突发 → IP 日桶」顺序检查。
//
// 旧实现把邮箱和 IP 拼成一个桶，能挡住单 IP 打单账号，但挡不住两件事：
//  1. 一个 IP 换不同邮箱做密码喷洒（每个邮箱都不触发账号桶）；
//  2. 分布式 IP 对同一账号慢速猜测（每个 IP 的复合桶都独立）。
//
// 因此账号桶必须只按归一化邮箱计数；IP 维度拆成分钟级突发桶与日总量桶。
// 任一桶故障由调用方按 fail-closed 处理。
func checkLoginRateLimits(
	ctx context.Context,
	deps *Deps,
	c *gin.Context,
	accountLimiter ratelimit.Limiter,
	email string,
) (bool, time.Duration, error) {
	ip := resolveLimiterClientIP(c, deps)
	return ratelimit.CompositeAllowWithRetry(ctx,
		ratelimit.Rule{
			Limiter:    accountLimiter,
			Key:        "login-account:" + normalizeLoginEmailKey(email),
			RetryAfter: loginAccountWindow,
		},
		ratelimit.Rule{
			Limiter:    deps.LoginIPBurstLimiter,
			Key:        "login-ip-burst:" + ip,
			RetryAfter: loginIPBurstWindow,
		},
		ratelimit.Rule{
			Limiter:    deps.LoginIPLimiter,
			Key:        "login-ip:" + ip,
			RetryAfter: loginIPDailyWindow,
		},
	)
}

// checkRootLoginLimit 对 root 账号叠加更紧的第二层桶；user 为 nil 时放行，
// 以便未知邮箱继续走 dummy scrypt 的通用失败路径。
func checkRootLoginLimit(ctx context.Context, deps *Deps, user *store.UserRecord) (bool, time.Duration, error) {
	if deps.RootLoginLimiter == nil || user == nil || user.Role != store.RoleRoot {
		return true, 0, nil
	}
	allowed, err := deps.RootLoginLimiter.Allow(ctx, "root-login:"+user.ID)
	return allowed, rootLoginWindow, err
}
