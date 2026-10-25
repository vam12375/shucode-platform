package apiapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

// 控制台 JSON 请求体上限；模型探测等路径的响应体另有独立限制。
const maxConsoleJSONBodyBytes = 1 << 20

// 登录/初始化只接收凭据与少量字段，单独收紧，避免长字段放大限流键与内存。
const maxLoginJSONBodyBytes = 64 << 10

// 控制台路由公共辅助(console.ts 内部 closures 的包级化)。

func readJSONBody(c *gin.Context) (any, bool) {
	return readJSONBodyLimited(c, maxConsoleJSONBodyBytes)
}

func readJSONBodyLimited(c *gin.Context, maxBytes int64) (any, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
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
	return readObjectBodyLimited(c, maxConsoleJSONBodyBytes)
}

func readObjectBodyLimited(c *gin.Context, maxBytes int64) (map[string]any, bool) {
	value, ok := readJSONBodyLimited(c, maxBytes)
	if !ok {
		return nil, false
	}
	obj, ok := value.(map[string]any)
	return obj, ok
}

// rejectUnknownFields:登录/初始化等凭据入口只接受白名单字段。静默忽略未知字段
// 在将来某个 handler 复用 body 时就是 mass assignment 的温床；这里直接 400。
func rejectUnknownFields(c *gin.Context, body map[string]any, allowed ...string) bool {
	known := make(map[string]bool, len(allowed))
	for _, field := range allowed {
		known[field] = true
	}
	for key := range body {
		if !known[key] {
			c.JSON(400, fail(400, "请求参数不合法"))
			return false
		}
	}
	return true
}

// redactSecretString:上游错误正文可能回显密钥，落库/返回前统一脱敏。
func redactSecretString(text, secret string) string {
	if secret == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, "[redacted]")
}

// requireRoot:平台级密钥/上游地址变更属于 root 专属，普通管理员不能把已有 key
// 重新绑定到自己的目标地址。
func requireRoot(ctx context.Context, deps *Deps, c *gin.Context) (*store.UserRecord, bool) {
	user, ok := requireConsoleUser(ctx, deps, c)
	if !ok {
		return nil, false
	}
	if user.Role != store.RoleRoot {
		c.JSON(403, fail(403, "需要超级管理员权限"))
		return nil, false
	}
	return user, true
}

// requireAdmin 检查当前团队的管理身份，全局 admin 标签不再授予跨租户权限。
func requireAdmin(ctx context.Context, deps *Deps, c *gin.Context) (*store.UserRecord, bool) {
	user, ok := requireConsoleUser(ctx, deps, c)
	if !ok {
		return nil, false
	}
	if user.Role == store.RoleRoot {
		return user, true
	}
	if _, ok := selectedTeam(c, deps, true); !ok {
		return nil, false
	}
	return user, true
}

// writeAudit:管理动作审计;只放元数据,凭据/密码/请求正文永不入审计。
// 入库失败时落本地 outbox，后台按 event_id 幂等重放；两者都失败才只告警。
// 使用脱离请求取消的 ctx，客户端断开不能吞掉刚完成操作的审计。
func writeAudit(ctx context.Context, deps *Deps, actor *store.UserRecord, action, targetType, targetID string, detail any) {
	if detail == nil {
		detail = map[string]any{}
	}
	raw, err := jsonMarshalNoHTML(detail)
	if err != nil {
		raw = []byte(`{}`)
	}
	persistAudit(ctx, deps, store.InsertAuditLogInput{
		EventID: uuid.NewString(), ActorUserID: actor.ID,
		Action: action, TargetType: targetType, TargetID: targetID, Detail: raw,
	})
}

func persistAudit(ctx context.Context, deps *Deps, entry store.InsertAuditLogInput) {
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if deps.AuditSink != nil {
		spooled, writeErr := deps.AuditSink.Write(auditCtx, entry)
		if writeErr != nil {
			deps.Logger.Error("审计写入失败且 outbox 落盘失败", "action", entry.Action, "error", writeErr)
		} else if spooled {
			deps.Logger.Warn("审计入库失败，已转入本地 outbox", "action", entry.Action)
		}
		return
	}
	if err := deps.Store.InsertAuditLog(auditCtx, entry); err != nil {
		deps.Logger.Warn("审计写入失败", "action", entry.Action, "error", err)
	}
}
