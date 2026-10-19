package apiapp

import (
	"context"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/turnstile"
)

const maxTurnstileTokenBytes = 4096

// TurnstileVerifier 允许测试注入假的挑战校验器；nil 表示未启用。
type TurnstileVerifier interface {
	Verify(ctx context.Context, token, remoteIP string) error
}

// requireTurnstile 在未配置 Turnstile 时直接放行；启用后 token 必须经
// Cloudflare siteverify 校验通过。remoteIP 使用与登录限流一致的可信来源解析。
func requireTurnstile(ctx context.Context, deps *Deps, c *gin.Context, token string) error {
	if deps.Turnstile == nil {
		return nil
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return turnstile.ErrMissingToken
	}
	if len(token) > maxTurnstileTokenBytes {
		return turnstile.ErrVerificationFailed
	}
	return deps.Turnstile.Verify(ctx, token, resolveLimiterClientIP(c, deps))
}

func writeTurnstileJSONError(c *gin.Context, err error) {
	if errors.Is(err, turnstile.ErrUnavailable) {
		c.JSON(503, fail(503, "人机验证服务暂不可用，请稍后重试"))
		return
	}
	c.JSON(400, fail(400, "人机验证失败，请重试"))
}

func turnstileHTMLMessage(err error) string {
	if errors.Is(err, turnstile.ErrUnavailable) {
		return "人机验证服务暂不可用，请稍后重试。"
	}
	return "请完成人机验证后重试。"
}

func turnstileHTMLStatus(err error) int {
	if errors.Is(err, turnstile.ErrUnavailable) {
		return 503
	}
	return 400
}
