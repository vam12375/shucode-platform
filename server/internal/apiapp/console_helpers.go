package apiapp

import (
	"context"
	"encoding/json"
	"io"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

// 控制台路由公共辅助(console.ts 内部 closures 的包级化)。

func readJSONBody(c *gin.Context) (any, bool) {
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, false
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	return value, true
}

func readObjectBody(c *gin.Context) (map[string]any, bool) {
	value, ok := readJSONBody(c)
	if !ok {
		return nil, false
	}
	obj, ok := value.(map[string]any)
	return obj, ok
}

// requireAdmin:控制台最低门槛是普通管理员；root 拥有全部管理员权限。
func requireAdmin(ctx context.Context, deps *Deps, c *gin.Context) (*store.UserRecord, bool) {
	user, err := readSessionUser(ctx, deps, c)
	if err != nil {
		internalError(c, deps, err)
		return nil, false
	}
	if user == nil || (user.Role != store.RoleRoot && user.Role != store.RoleAdmin) {
		c.JSON(401, fail(401, "需要管理员登录"))
		return nil, false
	}
	return user, true
}

// writeAudit:管理动作审计;只放元数据,凭据/密码/请求正文永不入审计。
// 写失败只告警不阻塞主操作。
func writeAudit(ctx context.Context, deps *Deps, actor *store.UserRecord, action, targetType, targetID string, detail any) {
	if detail == nil {
		detail = map[string]any{}
	}
	raw, err := jsonMarshalNoHTML(detail)
	if err != nil {
		raw = []byte(`{}`)
	}
	if err := deps.Store.InsertAuditLog(ctx, store.InsertAuditLogInput{
		ActorUserID: actor.ID, Action: action, TargetType: targetType, TargetID: targetID, Detail: raw,
	}); err != nil {
		deps.Logger.Warn("审计写入失败", "action", action, "error", err)
	}
}
