package apiapp

import (
	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

const privateAccountKey = "console.private-account"

func prepareAccountPrivacy(c *gin.Context, deps *Deps) bool {
	if _, exists := c.Get(privateAccountKey); exists {
		return true
	}
	account, err := deps.Store.FindRootUser(c.Request.Context())
	if err != nil {
		internalError(c, deps, err)
		return false
	}
	id := ""
	if account != nil {
		id = account.ID
	}
	c.Set(privateAccountKey, id)
	return true
}

func consoleAccountID(c *gin.Context, id string) string {
	user, _ := c.Get(consoleUserKey)
	if user == nil || user.(*store.UserRecord).Role == store.RoleRoot {
		return id
	}
	if privateID, exists := c.Get(privateAccountKey); exists && id == privateID {
		return ""
	}
	return id
}

func auditForConsole(c *gin.Context, record store.AdminAuditLogView) auditPublic {
	row := toPublicAudit(record)
	user, _ := c.Get(consoleUserKey)
	if user != nil && user.(*store.UserRecord).Role == store.RoleRoot {
		return row
	}
	// 跨团队管理操作也会进入团队审计，不能借操作人或目标字段暴露内部账号。
	privateID, prepared := c.Get(privateAccountKey)
	if !prepared || record.ActorUserID == privateID {
		row.ActorUserID, row.ActorEmail = nil, nil
	}
	// 团队审计也可能由 root 写入：按 teamId 隔离不能阻止导出日志里的
	// providerId/内部模型泄露。非 root 按动作白名单投影，未知动作不透传详情与目标。
	detail, _ := row.Detail.(map[string]any)
	row.Detail, row.TargetType, row.TargetID = nil, nil, nil
	public := map[string]any{}
	copyString := func(key string) {
		if value, ok := detail[key].(string); ok {
			public[key] = value
		}
	}
	target := func(kind string) {
		row.TargetType = &kind
		// 旧日志的目标类型也必须匹配已知动作；仅凭 UUID 形状仍可能泄露供应商身份。
		if kind != "usage" && record.TargetType == kind && uuidShape(record.TargetID) && prepared && record.TargetID != privateID {
			row.TargetID = &record.TargetID
		}
	}
	fields := func(allowed ...string) {
		values, _ := detail["fields"].([]any)
		visible := []string{}
		for _, raw := range values {
			value, ok := raw.(string)
			if !ok {
				continue
			}
			for _, known := range allowed {
				if value == known {
					visible = append(visible, value)
					break
				}
			}
		}
		public["fields"] = visible
	}
	switch row.Action {
	case "usage.export":
		target("usage")
		copyString("teamId")
		copyString("userId")
		if model, ok := detail["model"].(string); ok {
			public["model"] = ""
			if model != "" {
				public["model"] = usageDisplay(c, model).Name
			}
		}
		if count, ok := detail["count"].(float64); ok {
			public["count"] = count
		}
		if truncated, ok := detail["truncated"].(bool); ok {
			public["truncated"] = truncated
		}
	case "member.add":
		target("user")
		if role, _ := detail["role"].(string); role == "owner" || role == "admin" || role == "member" || role == "user" {
			public["role"] = role
		}
	case "member.update":
		target("user")
		fields("role", "disabled", "monthlyBudgetCredits")
	case "member.remove":
		target("user")
	case "policy.update":
		target("team")
		fields("rpmLimit", "tpmLimit", "concurrencyLimit", "dailyBudgetCredits", "monthlyBudgetCredits", "allowedModelIds", "budgetAlertPercent")
	case "team.canvas_tool_policy.update":
		target("team")
		fields("enabled", "allowedOperations", "requireStructureConfirmation", "requireMediaConfirmation", "maxMediaQuoteMicroPerOperation", "maxMediaOperationsPerDay", "maxConcurrentMediaOperations")
	case "invitation.create":
		target("invitation")
		copyString("email")
	case "invitation.revoke", "invitation.accept":
		target("invitation")
	case "quota_request.create", "quota_request.approved", "quota_request.rejected":
		target("quota_request")
		copyString("amountCredits")
	default:
		row.Action = "system.update"
		return row
	}
	if len(public) > 0 {
		row.Detail = public
	}
	return row
}
