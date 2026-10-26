package apiapp

import (
	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func auditForConsole(c *gin.Context, record store.AdminAuditLogView) auditPublic {
	row := toPublicAudit(record)
	user, _ := c.Get(consoleUserKey)
	if user != nil && user.(*store.UserRecord).Role == store.RoleRoot {
		return row
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
		if kind != "usage" && record.TargetType == kind && uuidShape(record.TargetID) {
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
		copyString("role")
	case "member.update":
		target("user")
		fields("role", "disabled", "monthlyBudgetCredits")
	case "member.remove":
		target("user")
	case "policy.update":
		target("team")
		fields("rpmLimit", "tpmLimit", "concurrencyLimit", "dailyBudgetCredits", "monthlyBudgetCredits", "allowedModelIds", "budgetAlertPercent")
	case "invitation.create":
		target("invitation")
		copyString("email")
	case "invitation.revoke", "invitation.accept":
		target("invitation")
	case "quota_request.create", "quota_request.approved", "quota_request.rejected":
		target("quota_request")
		copyString("amountCredits")
	default:
		return row
	}
	if len(public) > 0 {
		row.Detail = public
	}
	return row
}
