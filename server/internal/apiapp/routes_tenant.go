package apiapp

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/credits"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func writeTeamAudit(ctx context.Context, deps *Deps, actor *store.UserRecord, teamID, action, targetType, targetID string, detail any) {
	if detail == nil {
		detail = map[string]any{}
	}
	raw, err := jsonMarshalNoHTML(detail)
	if err != nil {
		raw = []byte(`{}`)
	}
	persistAudit(ctx, deps, store.InsertAuditLogInput{
		EventID: uuid.NewString(), TeamID: teamID, ActorUserID: actor.ID,
		Action: action, TargetType: targetType, TargetID: targetID, Detail: raw,
	})
}

func tenantStoreError(c *gin.Context, deps *Deps, err error) {
	switch {
	case errors.Is(err, store.ErrLastTeamAdmin), errors.Is(err, store.ErrUserAlreadyInTeam), errors.Is(err, store.ErrAmbiguousTeam):
		c.JSON(409, fail(409, err.Error()))
	case errors.Is(err, store.ErrInvitationInvalid):
		c.JSON(410, fail(410, "邀请已失效、过期或被使用"))
	case errors.Is(err, store.ErrInvitationEmailMismatch):
		c.JSON(403, fail(403, "请使用受邀邮箱对应的账号接受邀请"))
	default:
		internalError(c, deps, err)
	}
}

func invitationPublic(record store.TeamInvitationRecord, now time.Time) gin.H {
	status := "pending"
	if record.AcceptedAt != nil {
		status = "accepted"
	} else if record.RevokedAt != nil {
		status = "revoked"
	} else if !record.ExpiresAt.After(now) {
		status = "expired"
	}
	return gin.H{"id": record.ID, "teamId": record.TeamID, "email": record.Email, "createdAt": isoString(record.CreatedAt),
		"expiresAt": isoString(record.ExpiresAt), "acceptedAt": isoStringPtr(record.AcceptedAt), "revokedAt": isoStringPtr(record.RevokedAt), "status": status}
}

func parseBudget(raw any) (string, bool) {
	if raw == nil {
		return "", true
	}
	if value, ok := raw.(string); ok && strings.TrimSpace(value) == "" {
		return "", true
	}
	return parsePriceString(raw)
}

func registerTenantRoutes(router *gin.Engine, deps *Deps) {
	group := router.Group("/console/api")
	group.GET("/profile", func(c *gin.Context) {
		user, ok := requireConsoleUser(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		memberships, err := publicMemberships(c.Request.Context(), deps, user)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.JSON(200, okData(gin.H{"user": toPublicUser(user), "memberships": memberships}))
	})
	group.PATCH("/profile", func(c *gin.Context) {
		ctx := c.Request.Context()
		user, ok := requireConsoleUser(ctx, deps, c)
		if !ok {
			return
		}
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		for key := range body {
			if key != "displayName" && key != "currentPassword" && key != "password" {
				c.JSON(400, fail(400, "不支持的个人资料字段"))
				return
			}
		}
		patch := store.UpdateUserPatch{}
		fields := []string{}
		if raw, exists := body["displayName"]; exists {
			name, ok := trimmedRequired(raw, 1, 80)
			if !ok {
				c.JSON(400, fail(400, "显示名应为1到80个字符"))
				return
			}
			patch.DisplayName = &name
			fields = append(fields, "displayName")
		}
		passwordChanged := false
		if raw, exists := body["password"]; exists {
			password, valid := raw.(string)
			minimum := 10
			if user.Role == store.RoleRoot {
				minimum = rootPasswordMinLen
			}
			if !valid || len(password) < minimum || len(password) > 200 {
				c.JSON(400, fail(400, "新密码长度不符合要求"))
				return
			}
			current, _ := body["currentPassword"].(string)
			allowed, err := deps.ConsoleLoginLimiter.Allow(ctx, "profile-password:"+user.ID)
			if err != nil {
				c.JSON(503, fail(503, "验证暂不可用"))
				return
			}
			if !allowed {
				setRetryAfter(c, loginAccountWindow)
				c.JSON(429, fail(429, "尝试次数过多"))
				return
			}
			if !crypto.VerifyPassword(current, user.PasswordHash) {
				c.JSON(403, fail(403, "当前密码不正确"))
				return
			}
			hash, err := crypto.HashPassword(password)
			if err != nil {
				internalError(c, deps, err)
				return
			}
			patch.PasswordHash = &hash
			passwordChanged = true
			fields = append(fields, "password")
		}
		updated, err := deps.Store.UpdateUser(ctx, user.ID, patch)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if passwordChanged {
			if err := deps.Store.DeleteSessionsForUser(ctx, user.ID); err != nil {
				internalError(c, deps, err)
				return
			}
			if _, err := deps.Store.RevokeDeviceTokensForUser(ctx, user.ID, deps.Now()); err != nil {
				internalError(c, deps, err)
				return
			}
			// 已授权未消费的登录流同样能签发设备凭据，改密后必须一并作废。
			if _, err := deps.Store.RevokeLoginFlowsForUser(ctx, user.ID, deps.Now()); err != nil {
				internalError(c, deps, err)
				return
			}
			if err := loginWithSession(ctx, deps, c, user); err != nil {
				internalError(c, deps, err)
				return
			}
		}
		writeAudit(ctx, deps, user, "profile.update", "user", user.ID, map[string]any{"fields": fields})
		if passwordChanged {
			notifySecurityEvent(deps, "user.password.changed", "账号修改了自己的密码 user="+user.ID)
		}
		memberships, err := publicMemberships(ctx, deps, updated)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.JSON(200, okData(gin.H{"user": toPublicUser(updated), "memberships": memberships}))
	})

	group.PATCH("/teams/:id/members/:userId", func(c *gin.Context) {
		ctx := c.Request.Context()
		actor, ok := requireAdmin(ctx, deps, c)
		if !ok {
			return
		}
		teamID, userID := c.Param("id"), c.Param("userId")
		if _, ok := requireTeamAccess(c, deps, teamID, true); !ok {
			return
		}
		if !uuidShape(userID) {
			c.JSON(400, fail(400, "成员 ID 不合法"))
			return
		}
		target, err := deps.Store.GetTeamMember(ctx, teamID, userID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if target == nil {
			c.JSON(404, fail(404, "成员不存在"))
			return
		}
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		patch := store.TeamMemberPatch{}
		fields := []string{}
		for key, raw := range body {
			switch key {
			case "role":
				if actor.Role != store.RoleRoot {
					c.JSON(403, fail(403, "只有超级管理员可以任免团队管理员"))
					return
				}
				value, _ := raw.(string)
				role := store.TeamMemberRole(value)
				if role != store.MemberRoleOwner && role != store.MemberRoleAdmin && role != store.MemberRoleMember {
					c.JSON(400, fail(400, "团队角色不合法"))
					return
				}
				patch.Role = &role
			case "disabled":
				if actor.Role != store.RoleRoot && store.IsTeamAdmin(target.Role) {
					c.JSON(403, fail(403, "不能停用团队管理员"))
					return
				}
				value, valid := boolValue(raw)
				if !valid {
					c.JSON(400, fail(400, "disabled 必须是布尔值"))
					return
				}
				patch.HasDisabled = true
				if value {
					now := deps.Now()
					patch.DisabledAt = &now
				}
			case "monthlyBudgetCredits":
				budget, valid := parseBudget(raw)
				if !valid {
					c.JSON(400, fail(400, "成员月预算必须是非负金额，留空为不限"))
					return
				}
				patch.MonthlyBudgetCredits = &budget
			default:
				c.JSON(400, fail(400, "不支持的成员字段"))
				return
			}
			fields = append(fields, key)
		}
		member, err := deps.Store.UpdateTeamMember(ctx, teamID, userID, patch)
		if err != nil {
			tenantStoreError(c, deps, err)
			return
		}
		if member == nil {
			c.JSON(404, fail(404, "成员不存在"))
			return
		}
		writeTeamAudit(ctx, deps, actor, teamID, "member.update", "user", userID, map[string]any{"fields": fields})
		deps.ConfigEvents.Emit(events.DomainTeams)
		c.JSON(200, okData(gin.H{"member": gin.H{"user": toPublicUser(&member.User), "role": string(member.Role), "memberDisabledAt": isoStringPtr(member.MemberDisabledAt), "monthlyBudgetCredits": member.MonthlyBudgetCredits}}))
	})

	group.GET("/teams/:id/policy", func(c *gin.Context) {
		membership, ok := requireTeamAccess(c, deps, c.Param("id"), true)
		if !ok {
			return
		}
		policy, err := deps.Store.GetTeamPolicy(c.Request.Context(), membership.Team.ID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		models, err := deps.Store.ListModels(c.Request.Context(), false)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		available := make([]gin.H, 0, len(models))
		for _, model := range models {
			available = append(available, gin.H{"modelId": model.ModelID, "displayName": store.ModelDisplayName(model.ModelID, model.DisplayName)})
		}
		c.JSON(200, okData(gin.H{"teamId": policy.TeamID, "dailyBudgetCredits": policy.DailyBudgetCredits,
			"monthlyBudgetCredits": policy.MonthlyBudgetCredits, "allowedModelIds": policy.AllowedModelIDs, "budgetAlertPercent": policy.BudgetAlertPercent,
			"rpmLimit": policy.RPMLimit, "tpmLimit": policy.TPMLimit, "concurrencyLimit": policy.ConcurrencyLimit,
			"updatedAt": isoString(policy.UpdatedAt), "availableModels": available}))
	})
	group.PUT("/teams/:id/policy", func(c *gin.Context) {
		ctx := c.Request.Context()
		actor, ok := requireAdmin(ctx, deps, c)
		if !ok {
			return
		}
		membership, ok := requireTeamAccess(c, deps, c.Param("id"), true)
		if !ok {
			return
		}
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		policy, err := deps.Store.GetTeamPolicy(ctx, membership.Team.ID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		fields := []string{}
		for key, raw := range body {
			switch key {
			case "rpmLimit", "tpmLimit", "concurrencyLimit":
				value, valid := intInRange(raw, 0, 1_000_000)
				if !valid {
					c.JSON(400, fail(400, "团队流控必须是0到1000000的整数，0表示继承平台上限"))
					return
				}
				switch key {
				case "rpmLimit":
					policy.RPMLimit = value
				case "tpmLimit":
					policy.TPMLimit = value
				case "concurrencyLimit":
					policy.ConcurrencyLimit = value
				}
			case "dailyBudgetCredits", "monthlyBudgetCredits":
				budget, valid := parseBudget(raw)
				if !valid {
					c.JSON(400, fail(400, "预算必须是非负金额，留空为不限"))
					return
				}
				if key == "dailyBudgetCredits" {
					policy.DailyBudgetCredits = budget
				} else {
					policy.MonthlyBudgetCredits = budget
				}
			case "allowedModelIds":
				items, valid := raw.([]any)
				if !valid || len(items) > 1000 {
					c.JSON(400, fail(400, "模型白名单不合法"))
					return
				}
				models, err := deps.Store.ListModels(ctx, true)
				if err != nil {
					internalError(c, deps, err)
					return
				}
				known := map[string]bool{}
				for _, model := range models {
					known[model.ModelID] = true
				}
				ids := []string{}
				seen := map[string]bool{}
				for _, item := range items {
					id, valid := item.(string)
					if !valid || !known[id] {
						c.JSON(400, fail(400, "模型白名单包含未知模型"))
						return
					}
					if !seen[id] {
						ids = append(ids, id)
						seen[id] = true
					}
				}
				policy.AllowedModelIDs = ids
			case "budgetAlertPercent":
				value, valid := intInRange(raw, 1, 100)
				if !valid {
					c.JSON(400, fail(400, "提醒阈值必须在1到100之间"))
					return
				}
				policy.BudgetAlertPercent = value
			default:
				c.JSON(400, fail(400, "不支持的团队策略字段"))
				return
			}
			fields = append(fields, key)
		}
		updated, err := deps.Store.SaveTeamPolicy(ctx, *policy)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		writeTeamAudit(ctx, deps, actor, policy.TeamID, "policy.update", "team", policy.TeamID, map[string]any{"fields": fields})
		deps.ConfigEvents.Emit(events.DomainTeams)
		c.JSON(200, okData(updated))
	})

	group.GET("/teams/:id/invitations", func(c *gin.Context) {
		membership, ok := requireTeamAccess(c, deps, c.Param("id"), true)
		if !ok {
			return
		}
		invitations, err := deps.Store.ListTeamInvitations(c.Request.Context(), membership.Team.ID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		rows := make([]gin.H, 0, len(invitations))
		for _, invitation := range invitations {
			rows = append(rows, invitationPublic(invitation, deps.Now()))
		}
		c.JSON(200, okData(gin.H{"invitations": rows}))
	})
	group.POST("/teams/:id/invitations", func(c *gin.Context) {
		ctx := c.Request.Context()
		actor, ok := requireAdmin(ctx, deps, c)
		if !ok {
			return
		}
		membership, ok := requireTeamAccess(c, deps, c.Param("id"), true)
		if !ok {
			return
		}
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		email, valid := trimmedRequired(body["email"], 3, 254)
		if !valid || !emailShape(email) {
			c.JSON(400, fail(400, "邮箱不合法"))
			return
		}
		existing, err := deps.Store.FindUserByEmail(ctx, email)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if existing != nil {
			memberships, err := deps.Store.ListTeamsForUser(ctx, existing.ID)
			if err != nil {
				internalError(c, deps, err)
				return
			}
			if len(memberships) > 0 {
				c.JSON(409, fail(409, "该账号已加入团队，请通过成员管理操作"))
				return
			}
		}
		allowed, err := deps.ConsoleLoginLimiter.Allow(ctx, "team-invitation:"+actor.ID)
		if err != nil {
			c.JSON(503, fail(503, "邀请暂不可用"))
			return
		}
		if !allowed {
			c.JSON(429, fail(429, "邀请过于频繁，请稍后重试"))
			return
		}
		token := crypto.GeneratePollToken()
		now := deps.Now()
		record := store.TeamInvitationRecord{ID: uuid.NewString(), TeamID: membership.Team.ID, Email: strings.ToLower(email), TokenHash: crypto.HashToken(token), CreatedBy: actor.ID, CreatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour)}
		if err := deps.Store.CreateTeamInvitation(ctx, record); err != nil {
			tenantStoreError(c, deps, err)
			return
		}
		origin := deps.Env.PublicConsoleOrigin
		if origin == "" {
			origin = deps.Env.PublicAPIOrigin
		}
		writeTeamAudit(ctx, deps, actor, membership.Team.ID, "invitation.create", "invitation", record.ID, map[string]any{"email": record.Email})
		c.JSON(200, okData(gin.H{"invitation": invitationPublic(record, now), "inviteUrl": strings.TrimRight(origin, "/") + "/console/#/invite?token=" + token}))
	})
	group.POST("/teams/:id/invitations/:invitationId/revoke", func(c *gin.Context) {
		ctx := c.Request.Context()
		actor, ok := requireAdmin(ctx, deps, c)
		if !ok {
			return
		}
		membership, ok := requireTeamAccess(c, deps, c.Param("id"), true)
		if !ok {
			return
		}
		if !uuidShape(c.Param("invitationId")) {
			c.JSON(400, fail(400, "邀请 ID 不合法"))
			return
		}
		revoked, err := deps.Store.RevokeTeamInvitation(ctx, membership.Team.ID, c.Param("invitationId"), deps.Now())
		if err != nil {
			tenantStoreError(c, deps, err)
			return
		}
		if !revoked {
			c.JSON(404, fail(404, "未找到待接受的邀请"))
			return
		}
		writeTeamAudit(ctx, deps, actor, membership.Team.ID, "invitation.revoke", "invitation", c.Param("invitationId"), nil)
		c.JSON(200, gin.H{"code": 0})
	})
	group.POST("/invitations/accept", func(c *gin.Context) {
		ctx := c.Request.Context()
		allowed, err := deps.ConsoleLoginLimiter.Allow(ctx, "invitation-accept:"+resolveLimiterClientIP(c, deps))
		if err != nil {
			c.JSON(503, fail(503, "验证暂不可用"))
			return
		}
		if !allowed {
			c.JSON(429, fail(429, "尝试次数过多"))
			return
		}
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		token, valid := trimmedRequired(body["token"], 20, 200)
		if !valid {
			c.JSON(400, fail(400, "邀请凭据不合法"))
			return
		}
		hash := crypto.HashToken(token)
		invitation, err := deps.Store.FindTeamInvitationByHash(ctx, hash)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if invitation == nil || invitation.AcceptedAt != nil || invitation.RevokedAt != nil || !invitation.ExpiresAt.After(deps.Now()) {
			c.JSON(410, fail(410, "邀请已失效、过期或被使用"))
			return
		}
		user, err := readSessionUser(ctx, deps, c)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if user == nil {
			email, valid := trimmedRequired(body["email"], 3, 254)
			if !valid || !strings.EqualFold(email, invitation.Email) {
				c.JSON(403, fail(403, "请使用受邀邮箱注册"))
				return
			}
			name, valid := trimmedRequired(body["displayName"], 1, 80)
			password, passwordOK := body["password"].(string)
			if !valid || !passwordOK || len(password) < 10 || len(password) > 200 {
				c.JSON(400, fail(400, "请填写显示名及至少10位密码"))
				return
			}
			passwordHash, err := crypto.HashPassword(password)
			if err != nil {
				internalError(c, deps, err)
				return
			}
			user, err = deps.Store.CreateUser(ctx, store.UserInput{Email: email, DisplayName: name, PasswordHash: passwordHash, Role: store.RoleUser})
			if errors.Is(err, store.ErrEmailTaken) {
				c.JSON(409, fail(409, "受邀邮箱已有账号，请先登录再接受邀请"))
				return
			}
			if err != nil {
				internalError(c, deps, err)
				return
			}
		}
		accepted, err := deps.Store.AcceptTeamInvitation(ctx, hash, user.ID, deps.Now())
		if err != nil {
			tenantStoreError(c, deps, err)
			return
		}
		if err := loginWithSession(ctx, deps, c, user); err != nil {
			internalError(c, deps, err)
			return
		}
		writeTeamAudit(ctx, deps, user, accepted.TeamID, "invitation.accept", "invitation", accepted.ID, nil)
		deps.ConfigEvents.Emit(events.DomainTeams)
		memberships, err := publicMemberships(ctx, deps, user)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.JSON(200, okData(gin.H{"user": toPublicUser(user), "memberships": memberships, "teamId": accepted.TeamID}))
	})
	group.GET("/teams/:id/notifications", func(c *gin.Context) {
		membership, ok := requireTeamAccess(c, deps, c.Param("id"), true)
		if !ok {
			return
		}
		policy, err := deps.Store.GetTeamPolicy(c.Request.Context(), membership.Team.ID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		notifications := []gin.H{}
		threshold := policy.BudgetAlertPercent
		check := func(kind, label, limit string, used, reserved int64) {
			if limit == "" {
				return
			}
			cap, err := credits.ParseCreditsToMicro(limit)
			if err != nil {
				return
			}
			if cap == 0 || (float64(used)+float64(reserved))*100 >= float64(cap)*float64(threshold) {
				tone := "hot"
				if cap == 0 || used >= cap {
					tone = "stop"
				}
				notifications = append(notifications, gin.H{"kind": kind, "tone": tone, "message": label + "已达到提醒阈值，请检查消费与预算", "usedCredits": credits.FormatMicroCredits(used), "reservedCredits": credits.FormatMicroCredits(reserved), "limitCredits": limit})
			}
		}
		used, _ := credits.ParseCreditsToMicro(membership.Team.UsedCredits)
		if !membership.Team.Unlimited {
			check("balance", "团队额度", membership.Team.QuotaCredits, used, 0)
		}
		zone := time.FixedZone("Asia/Shanghai", 8*60*60)
		now := deps.Now().In(zone)
		for _, window := range []struct {
			kind, label, limit string
			from               time.Time
		}{
			{"daily_budget", "团队日预算", policy.DailyBudgetCredits, time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, zone)},
			{"monthly_budget", "团队月预算", policy.MonthlyBudgetCredits, time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, zone)},
		} {
			if window.limit == "" {
				continue
			}
			until := deps.Now()
			rows, err := deps.Store.AggregateUsage(c.Request.Context(), store.UsageFilter{TeamID: membership.Team.ID, From: &window.from, To: &until})
			if err != nil {
				internalError(c, deps, err)
				return
			}
			total := int64(0)
			for _, row := range rows {
				total += row.CreditsMicro
			}
			check(window.kind, window.label, window.limit, total, 0)
		}
		c.JSON(200, okData(gin.H{"notifications": notifications}))
	})
}
