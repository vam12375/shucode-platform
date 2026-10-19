package apiapp

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/ratelimit"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

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

// checkLoginRateLimits 先检查 Pure-IP 全局桶，再检查 IP+归一化邮箱桶。
// 任一桶故障由调用方按 fail-closed 处理。
func checkLoginRateLimits(
	ctx context.Context,
	deps *Deps,
	c *gin.Context,
	emailLimiter ratelimit.Limiter,
	email string,
) (bool, error) {
	ip := resolveLimiterClientIP(c, deps)
	return ratelimit.CompositeAllow(ctx,
		ratelimit.Rule{Limiter: deps.LoginIPLimiter, Key: "login-ip:" + ip},
		ratelimit.Rule{
			Limiter: emailLimiter,
			Key:     "login-email:" + ip + ":" + normalizeLoginEmailKey(email),
		},
	)
}

// checkRootLoginLimit 对 root 账号叠加更紧的第二层桶；user 为 nil 时放行，
// 以便未知邮箱继续走 dummy scrypt 的通用失败路径。
func checkRootLoginLimit(ctx context.Context, deps *Deps, user *store.UserRecord) (bool, error) {
	if deps.RootLoginLimiter == nil || user == nil || user.Role != store.RoleRoot {
		return true, nil
	}
	return deps.RootLoginLimiter.Allow(ctx, "root-login:"+user.ID)
}
