package apiapp

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// 绑定失败不能混同为团队异常；客户端保留原因，不误触发登出或换账本。
func respondBillingBindingError(c *gin.Context, err error) bool {
	status, reason, message := 403, "", ""
	switch {
	case errors.Is(err, store.ErrBillingBindingActorMismatch):
		reason, message = "billing_binding_actor_mismatch", "此会话属于其他账号，请切回创建此会话的账号后继续"
	case errors.Is(err, store.ErrBillingBindingDeviceMismatch):
		reason, message = "billing_binding_device_mismatch", "当前设备尚未恢复此会话的授权，请重新打开会话后重试"
	case errors.Is(err, store.ErrBillingBindingNoActiveTeam):
		reason, message = "billing_binding_no_active_team", "当前没有有效团队，请加入团队或切换为个人消费后新建会话"
	case errors.Is(err, store.ErrAmbiguousTeam):
		status, reason, message = 409, "billing_binding_ambiguous_team", "当前存在多个有效团队，请联系管理员明确团队归属"
	case errors.Is(err, store.ErrBillingBindingAccountUnavailable):
		reason, message = "billing_binding_account_unavailable", "此会话的消费账本已停用或访问权限已撤销，请联系管理员"
	case errors.Is(err, store.ErrBillingBindingNotFound):
		status, reason, message = 404, "billing_binding_parent_not_found", "会话或父会话绑定不存在"
	case errors.Is(err, store.ErrBillingBindingConflict):
		status, reason, message = 409, "billing_binding_identity_conflict", "此会话的绑定身份与原记录不一致"
	case errors.Is(err, store.ErrBillingBindingForbidden):
		reason, message = "billing_binding_forbidden", "当前账号或设备无权使用此会话的消费账本"
	default:
		return false
	}
	c.JSON(status, gin.H{"code": status, "msg": message, "details": gin.H{"reason": reason}})
	return true
}
