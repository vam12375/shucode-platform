package apiapp

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

// 安全事件审计与告警。审计只落元数据（结果、原因、来源 IP、目标用户 ID），
// 口令、Turnstile token、恢复码与 TOTP 永不进入这里。
func writeAuthAudit(ctx context.Context, deps *Deps, userID, action, result, ip string, extra map[string]any) {
	detail := map[string]any{"result": result, "ip": ip}
	for key, value := range extra {
		detail[key] = value
	}
	raw, err := jsonMarshalNoHTML(detail)
	if err != nil {
		raw = []byte(`{}`)
	}
	persistAudit(ctx, deps, store.InsertAuditLogInput{
		EventID: uuid.NewString(), ActorUserID: userID, Action: action,
		TargetType: "auth", TargetID: userID, Detail: raw,
	})
}

// notifySecurityEvent:安全事件走运维 webhook；通知失败不能影响已完成的登录/
// 改密动作，因此异步发送，且复用 sendAlertWebhook 的 5 秒超时。
func notifySecurityEvent(deps *Deps, name, message string) {
	if deps == nil || deps.Logger == nil {
		return
	}
	deps.Logger.Warn("安全事件", "event", name, "message", message)
	if deps.Env == nil || deps.Env.AlertWebhookURL == "" {
		return
	}
	go sendAlertWebhook(context.Background(), deps, name, "security", message, 1, 0)
}

// loginFailureReason:把失败结果收敛成有限枚举，避免把账号是否存在直接写进审计。
func loginFailureReason(user *store.UserRecord) string {
	if user == nil {
		return "unknown_account"
	}
	if user.DisabledAt != nil {
		return "account_disabled"
	}
	return "invalid_credentials"
}

// verifyLoginPasswordWithGate:在进入高成本 scrypt 前占用实例槽位，槽位耗尽
// 立即返回 busy，由调用方转 429；不能把请求堆在队列里继续消耗内存。
func verifyLoginPasswordWithGate(deps *Deps, user *store.UserRecord, password string) (valid, busy bool) {
	if deps.LoginGate != nil {
		if !deps.LoginGate.TryAcquire() {
			return false, true
		}
		defer deps.LoginGate.Release()
	}
	return verifyLoginPassword(user, password), false
}

// authRequestIP:审计里记录限流所用的可信客户端 IP，和登录分桶保持同一口径。
func authRequestIP(c *gin.Context, deps *Deps) string {
	return resolveLimiterClientIP(c, deps)
}
