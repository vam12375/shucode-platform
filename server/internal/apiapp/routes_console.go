package apiapp

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/contracts"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/pricing"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// console.ts 的镜像:全部位于 /console/api/*,要求 admin/root 角色的会话；
// 用户角色升降级只允许 root（见用户管理路由）。

// root 首次初始化口令下限：超级管理员口令比普通用户更严格。
const rootPasswordMinLen = 12

func registerConsoleRoutes(router *gin.Engine, deps *Deps) {
	group := router.Group("/console/api")

	// ---- setup ----
	// root 只能创建一次且凭据只以 scrypt 哈希入库；初始化完成后两个接口都不再允许写入。

	group.GET("/setup/status", func(c *gin.Context) {
		if !allowPublicRead(c, deps, "setup-status") {
			return
		}
		root, err := deps.Store.FindRootUser(c.Request.Context())
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.Header("cache-control", "no-store")
		c.JSON(200, okData(gin.H{"rootInitialized": root != nil}))
	})

	// 登录页公开配置：site key 本身是公开值，secret 永远不下发。
	group.GET("/public-config", func(c *gin.Context) {
		if !allowPublicRead(c, deps, "public-config") {
			return
		}
		c.Header("cache-control", "no-store")
		c.JSON(200, okData(gin.H{
			"turnstileSiteKey": deps.Env.TurnstileSiteKey,
			"turnstileEnabled": deps.Env.TurnstileSiteKey != "" && deps.Turnstile != nil,
		}))
	})

	group.POST("/setup/root", func(c *gin.Context) {
		ctx := c.Request.Context()
		c.Header("cache-control", "no-store")
		root, err := deps.Store.FindRootUser(ctx)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if root != nil {
			c.JSON(409, fail(409, "系统已完成初始化，请直接登录"))
			return
		}
		// 新部署必须先证明来自部署者：生产模式强制配置一次性 bootstrap secret，
		// 内存体验模式保留免密初始化以便本地使用。
		if deps.Env.BootstrapSecret == "" {
			if !deps.Env.MemoryDB {
				c.JSON(503, fail(503, "未配置 BOOTSTRAP_SECRET，拒绝首次初始化"))
				return
			}
		} else if !secretsEqual(c.GetHeader("x-bootstrap-secret"), deps.Env.BootstrapSecret) {
			c.JSON(403, fail(403, "初始化凭据无效"))
			return
		}
		if deps.ConsoleLoginLimiter != nil {
			allowed, limitErr := deps.ConsoleLoginLimiter.Allow(ctx, resolveLimiterClientIP(c, deps)+":root-setup")
			if limitErr != nil {
				// 管理面不采用 fail-open：限流后端故障时宁可暂停初始化，也不放开爆破窗口。
				deps.Logger.Error("初始化限流不可用，拒绝初始化", "error", limitErr)
				c.JSON(503, fail(503, "限流服务不可用，请稍后重试"))
				return
			}
			if !allowed {
				writeLoginRateLimited(c, "尝试次数过多，请稍后再试", loginAccountWindow)
				return
			}
		}
		body, ok := readObjectBodyLimited(c, maxLoginJSONBodyBytes)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		if !rejectUnknownFields(c, body, "email", "displayName", "password") {
			return
		}
		email, okEmail := trimmedRequired(body["email"], 3, 254)
		displayName, okName := trimmedRequired(body["displayName"], 1, 80)
		password, okPassword := body["password"].(string)
		if !okEmail || !emailShape(email) || !okName || !okPassword ||
			len(password) < rootPasswordMinLen || len(password) > 200 {
			c.JSON(400, fail(400, "请求参数不合法（超级管理员密码至少 12 位）"))
			return
		}
		hash, err := crypto.HashPassword(password)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		user, err := deps.Store.CreateUser(ctx, store.UserInput{
			Email: email, PasswordHash: hash, DisplayName: displayName, Role: store.RoleRoot,
		})
		if err != nil {
			switch {
			case errors.Is(err, store.ErrRootUserExists):
				c.JSON(409, fail(409, "系统已完成初始化，请直接登录"))
			case errors.Is(err, store.ErrEmailTaken):
				c.JSON(409, fail(409, "该邮箱已存在"))
			default:
				internalError(c, deps, err)
			}
			return
		}
		writeAudit(ctx, deps, user, "root.setup", "user", user.ID, nil)
		if err := loginWithSession(ctx, deps, c, user); err != nil {
			internalError(c, deps, err)
			return
		}
		c.JSON(200, okData(gin.H{
			"user": toPublicUser(user), "rootInitialized": true,
			"mfaSetupRequired": rootMFAEnrollmentRequired(ctx, deps, c, user),
		}))
	})

	// ---- auth ----

	group.POST("/auth/login", func(c *gin.Context) {
		ctx := c.Request.Context()
		body, ok := readObjectBodyLimited(c, maxLoginJSONBodyBytes)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		if !rejectUnknownFields(c, body, "email", "password", "turnstileToken", "code", "recoveryCode") {
			return
		}
		email, okEmail := trimmedRequired(body["email"], 3, 254)
		password, okPassword := body["password"].(string)
		if !okEmail || !okPassword || password == "" {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		ip := authRequestIP(c, deps)
		if deps.ConsoleLoginLimiter == nil {
			c.JSON(503, fail(503, "登录限流服务不可用，请稍后重试"))
			return
		}
		allowed, retryAfter, err := checkLoginRateLimits(ctx, deps, c, deps.ConsoleLoginLimiter, email)
		if err != nil {
			// 管理面 fail-closed：限流后端故障时不能退化为无限次口令尝试。
			deps.Logger.Error("登录限流不可用，拒绝登录", "error", err)
			c.JSON(503, fail(503, "登录限流服务不可用，请稍后重试"))
			return
		}
		if !allowed {
			writeLoginRateLimited(c, "尝试次数过多，请稍后再试", retryAfter)
			return
		}
		turnstileToken, _ := body["turnstileToken"].(string)
		if err := requireTurnstile(ctx, deps, c, turnstileToken); err != nil {
			deps.Logger.Warn("登录人机验证未通过", "error", err)
			writeTurnstileJSONError(c, err)
			return
		}
		user, err := deps.Store.FindUserByEmail(ctx, email)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		allowed, retryAfter, err = checkRootLoginLimit(ctx, deps, user)
		if err != nil {
			deps.Logger.Error("root 登录限流不可用，拒绝登录", "error", err)
			c.JSON(503, fail(503, "登录限流服务不可用，请稍后重试"))
			return
		}
		if !allowed {
			deps.Logger.Warn("root 登录触发专属限流", "ip", ip, "userId", user.ID)
			notifySecurityEvent(deps, "root.login.blocked", "root 登录触发限流 user="+user.ID+" ip="+ip)
			writeLoginRateLimited(c, "尝试次数过多，请稍后再试", retryAfter)
			return
		}
		valid, gateBusy := verifyLoginPasswordWithGate(deps, user, password)
		if gateBusy {
			writeLoginRateLimited(c, "登录请求过多，请稍后重试", time.Second)
			return
		}
		if !valid {
			userID := ""
			if user != nil {
				userID = user.ID
			}
			writeAuthAudit(ctx, deps, userID, "auth.login", "failure", ip,
				map[string]any{"reason": loginFailureReason(user)})
			c.JSON(401, fail(401, "邮箱或密码不正确"))
			return
		}
		if !verifyRootMFA(ctx, deps, c, user, body) {
			return
		}
		rehashPasswordIfNeeded(ctx, deps, user, password)
		if err := loginWithSession(ctx, deps, c, user); err != nil {
			internalError(c, deps, err)
			return
		}
		writeAuthAudit(ctx, deps, user.ID, "auth.login", "success", ip, nil)
		if user.Role == store.RoleRoot {
			notifySecurityEvent(deps, "root.login", "超级管理员登录成功 user="+user.ID+" ip="+ip)
		}
		memberships, err := publicMemberships(ctx, deps, user)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.JSON(200, okData(gin.H{
			"user": toPublicUser(user), "memberships": memberships,
			"mfaSetupRequired": rootMFAEnrollmentRequired(ctx, deps, c, user),
		}))
	})

	group.POST("/auth/logout", func(c *gin.Context) {
		if err := logoutSession(c.Request.Context(), deps, c); err != nil {
			internalError(c, deps, err)
			return
		}
		c.JSON(200, gin.H{"code": 0})
	})

	group.GET("/auth/me", func(c *gin.Context) {
		user, ok := requireConsoleUser(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		memberships, err := publicMemberships(c.Request.Context(), deps, user)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.JSON(200, okData(gin.H{
			"user": toPublicUser(user), "memberships": memberships,
			"mfaSetupRequired": rootMFAEnrollmentRequired(c.Request.Context(), deps, c, user),
		}))
	})

	// ---- teams ----

	group.GET("/teams", func(c *gin.Context) {
		user, ok := requireConsoleUser(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		teams, err := deps.Store.ListTeams(c.Request.Context())
		if err != nil {
			internalError(c, deps, err)
			return
		}
		memberships, err := deps.Store.ListTeamsForUser(c.Request.Context(), user.ID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		allowed := map[string]store.TeamMembership{}
		for _, membership := range memberships {
			if membership.Team.DisabledAt == nil && membership.MemberDisabledAt == nil {
				allowed[membership.Team.ID] = membership
			}
		}
		rows := make([]any, 0, len(teams))
		for _, entry := range teams {
			membership, belongs := allowed[entry.Team.ID]
			if user.Role != store.RoleRoot && !belongs {
				continue
			}
			row := toPublicTeam(&entry.Team)
			rows = append(rows, gin.H{
				"id": row.ID, "name": row.Name, "quotaCredits": row.QuotaCredits,
				"usedCredits": row.UsedCredits, "reservedCredits": row.ReservedCredits,
				"unlimited": row.Unlimited, "createdAt": row.CreatedAt, "disabledAt": row.DisabledAt,
				"memberCount": entry.MemberCount, "memberRole": string(membership.Role),
				"rpmLimit": row.RPMLimit, "tpmLimit": row.TPMLimit, "concurrencyLimit": row.ConcurrencyLimit,
			})
		}
		c.JSON(200, okData(gin.H{"teams": rows}))
	})

	group.POST("/teams", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		name, okName := trimmedRequired(body["name"], 1, 80)
		if !okName {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		quotaCredits := "0.000000"
		if raw, exists := body["quotaCredits"]; exists {
			parsed, okQuota := parsePriceString(raw)
			if !okQuota {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			quotaCredits = parsed
		}
		unlimited := false
		if raw, exists := body["unlimited"]; exists {
			parsed, okBool := boolValue(raw)
			if !okBool {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			unlimited = parsed
		}
		team, err := deps.Store.CreateTeam(c.Request.Context(), store.CreateTeamInput{
			Name: name, QuotaCredits: quotaCredits, Unlimited: unlimited,
		})
		if err != nil {
			c.JSON(409, fail(409, err.Error()))
			return
		}
		c.JSON(200, okData(gin.H{"team": toPublicTeam(team)}))
	})

	group.GET("/teams/:id", func(c *gin.Context) {
		user, ok := requireConsoleUser(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		teamID := c.Param("id")
		membership, ok := requireTeamAccess(c, deps, teamID, false)
		if !ok {
			return
		}
		canManage := user.Role == store.RoleRoot || store.IsTeamAdmin(membership.Role)
		team, err := deps.Store.GetTeam(ctx, teamID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if team == nil {
			c.JSON(404, fail(404, "团队不存在"))
			return
		}
		usageCursorRaw := c.Query("usageCursor")
		cursorCreatedAt, cursorID, cursorOK := decodeUsageCursor(usageCursorRaw)
		if usageCursorRaw != "" && !cursorOK {
			c.JSON(400, fail(400, "分页游标不合法"))
			return
		}
		usageLimit := int64(51)
		usageFilter := store.UsageFilter{TeamID: teamID, Limit: &usageLimit}
		if !canManage {
			usageFilter.UserID = user.ID
		}
		if cursorOK {
			parsed, okTime := parseQueryTime(cursorCreatedAt)
			if !okTime {
				c.JSON(400, fail(400, "分页游标不合法"))
				return
			}
			usageFilter.CursorCreatedAt = &parsed
			usageFilter.CursorID = cursorID
		}
		members, err := deps.Store.ListMembers(ctx, teamID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		aggregates, err := deps.Store.AggregateUsage(ctx, store.UsageFilter{TeamID: teamID, UserID: usageFilter.UserID})
		if err != nil {
			internalError(c, deps, err)
			return
		}
		usagePage, err := deps.Store.ListUsage(ctx, usageFilter)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		quotaAdjustments, err := deps.Store.ListQuotaAdjustments(ctx, teamID, 10)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		hasMore := len(usagePage) > 50
		recent := usagePage
		if hasMore {
			recent = usagePage[:50]
		}
		var nextCursor *string
		if hasMore && len(recent) > 0 {
			last := recent[len(recent)-1]
			cursor := encodeUsageCursor(last.CreatedAt, last.ID)
			nextCursor = &cursor
		}
		memberRows := make([]any, 0, len(members))
		for _, member := range members {
			if !canManage && member.User.ID != user.ID {
				continue
			}
			memberRows = append(memberRows, gin.H{
				"user":             toPublicUser(&member.User),
				"role":             string(member.Role),
				"joinedAt":         isoString(member.CreatedAt),
				"memberDisabledAt": isoStringPtr(member.MemberDisabledAt), "monthlyBudgetCredits": member.MonthlyBudgetCredits,
			})
		}
		if !prepareModelDisplay(c, deps) {
			return
		}
		aggregateRows := aggregateUsageForConsole(c, aggregates)
		usageRows := make([]any, 0, len(recent))
		for i := range recent {
			usageRows = append(usageRows, usageForConsole(c, &recent[i]))
		}
		adjustmentRows := make([]quotaAdjustmentPublic, 0, len(quotaAdjustments))
		for _, row := range quotaAdjustments {
			if !canManage {
				continue
			}
			if user.Role != store.RoleRoot {
				row.ActorEmail = ""
			}
			adjustmentRows = append(adjustmentRows, toPublicQuotaAdjustment(row))
		}
		c.JSON(200, okData(gin.H{
			"team": toPublicTeam(team), "members": memberRows, "aggregates": aggregateRows,
			"recentUsage": usageRows, "recentUsageNextCursor": nextCursor,
			"quotaAdjustments": adjustmentRows,
		}))
	})

	group.PATCH("/teams/:id", func(c *gin.Context) {
		admin, ok := requireAdmin(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		if admin.Role != store.RoleRoot {
			for field := range body {
				if field != "name" {
					c.JSON(403, fail(403, "团队管理员只能修改团队名称；额度和平台上限由超级管理员调整"))
					return
				}
			}
		}
		var patch store.UpdateTeamPatch
		if raw, exists := body["disabled"]; exists {
			value, ok := boolValue(raw)
			if !ok {
				c.JSON(400, fail(400, "disabled 必须是布尔值"))
				return
			}
			patch.HasDisabled = true
			if value {
				now := deps.Now()
				patch.DisabledAt = &now
			}
		}
		if raw, exists := body["name"]; exists {
			name, okName := trimmedRequired(raw, 1, 80)
			if !okName {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			patch.Name = &name
		}
		if raw, exists := body["quotaCredits"]; exists {
			quota, okQuota := parsePriceString(raw)
			if !okQuota {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			patch.QuotaCredits = &quota
		}
		if raw, exists := body["unlimited"]; exists {
			unlimited, okBool := boolValue(raw)
			if !okBool {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			patch.Unlimited = &unlimited
		}
		for key, target := range map[string]**int64{
			"rpmLimit": &patch.RPMLimit, "tpmLimit": &patch.TPMLimit, "concurrencyLimit": &patch.ConcurrencyLimit,
		} {
			raw, exists := body[key]
			if !exists {
				continue
			}
			number, okNumber := raw.(float64)
			if !okNumber || number < 0 || number > 1_000_000 || number != float64(int64(number)) {
				c.JSON(400, fail(400, key+" 必须是非负整数"))
				return
			}
			value := int64(number)
			*target = &value
		}
		teamID := c.Param("id")
		// 仅用于 404 与名称变更提示；额度旧值由存储层在事务内锁定读取。
		previousTeam, err := deps.Store.GetTeam(ctx, teamID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if previousTeam == nil {
			c.JSON(404, fail(404, "团队不存在"))
			return
		}
		previousName := previousTeam.Name
		changed := []string{}
		if patch.HasDisabled {
			changed = append(changed, "disabled")
		}
		if patch.Name != nil && *patch.Name != previousName {
			changed = append(changed, "name")
		}
		if patch.RPMLimit != nil || patch.TPMLimit != nil || patch.ConcurrencyLimit != nil {
			changed = append(changed, "limits")
		}
		if patch.QuotaCredits != nil || patch.Unlimited != nil {
			changed = append(changed, "quota")
		}
		// 团队更新、额度流水与审计在同一事务提交：任何一步失败都不留下
		// “额度已改但流水/审计缺失”的半完成状态。
		var auditEntry *store.InsertAuditLogInput
		if len(changed) > 0 {
			raw, marshalErr := jsonMarshalNoHTML(map[string]any{"fields": changed})
			if marshalErr != nil {
				raw = []byte(`{}`)
			}
			eventID, _ := contracts.RandomUUIDv4()
			auditEntry = &store.InsertAuditLogInput{
				EventID: eventID, TeamID: teamID, ActorUserID: admin.ID,
				Action: "team.update", TargetType: "team", TargetID: teamID, Detail: raw,
			}
		}
		adjustmentAction := ""
		if patch.QuotaCredits != nil || patch.Unlimited != nil {
			adjustmentAction = "set_quota"
		}
		team, err := deps.Store.UpdateTeamWithAccounting(ctx, teamID, patch, adjustmentAction, auditEntry)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if team == nil {
			c.JSON(404, fail(404, "团队不存在"))
			return
		}
		deps.ConfigEvents.Emit(events.DomainTeams)
		c.JSON(200, okData(gin.H{"team": toPublicTeam(team)}))
	})

	group.POST("/teams/:id/members", func(c *gin.Context) {
		actor, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		email, okEmail := trimmedRequired(body["email"], 3, 254)
		if !okEmail {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		role := store.MemberRoleMember
		if raw, exists := body["role"]; exists {
			roleStr, okRole := raw.(string)
			if !okRole || (roleStr != "owner" && roleStr != "admin" && roleStr != "member") {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			role = store.TeamMemberRole(roleStr)
		}
		user, err := deps.Store.FindUserByEmail(ctx, email)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if user == nil {
			c.JSON(404, fail(404, "用户不存在："+email+"（请先在用户页创建）"))
			return
		}
		if err := deps.Store.AddMember(ctx, c.Param("id"), user.ID, role); err != nil {
			tenantStoreError(c, deps, err)
			return
		}
		writeTeamAudit(ctx, deps, actor, c.Param("id"), "member.add", "user", user.ID, map[string]any{"role": string(role)})
		deps.ConfigEvents.Emit(events.DomainTeams)
		c.JSON(200, okData(gin.H{"user": toPublicUser(user)}))
	})

	group.DELETE("/teams/:id/members/:userId", func(c *gin.Context) {
		actor, ok := requireAdmin(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		target, err := deps.Store.GetTeamMember(c.Request.Context(), c.Param("id"), c.Param("userId"))
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if target != nil && store.IsTeamAdmin(target.Role) && actor.Role != store.RoleRoot {
			c.JSON(403, fail(403, "只有超级管理员可以移除团队管理员"))
			return
		}
		removed, err := deps.Store.RemoveMember(c.Request.Context(), c.Param("id"), c.Param("userId"))
		if err != nil {
			tenantStoreError(c, deps, err)
			return
		}
		if !removed {
			c.JSON(404, fail(404, "成员不存在"))
			return
		}
		writeTeamAudit(c.Request.Context(), deps, actor, c.Param("id"), "member.remove", "user", c.Param("userId"), nil)
		deps.ConfigEvents.Emit(events.DomainTeams)
		c.JSON(200, gin.H{"code": 0})
	})

	// ---- users ----

	group.GET("/users", func(c *gin.Context) {
		user, ok := requireConsoleUser(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		users := []store.UserRecord{}
		teamByUser := map[string]store.AllMembershipRow{}
		if user.Role == store.RoleRoot {
			memberships, err := deps.Store.ListAllMemberships(ctx)
			if err != nil {
				internalError(c, deps, err)
				return
			}
			// 单团队约束由写入侧保证;历史脏数据出现多条时取最早一条展示(与列表升序一致)。
			for _, row := range memberships {
				if _, exists := teamByUser[row.UserID]; !exists {
					teamByUser[row.UserID] = row
				}
			}
			users, err = deps.Store.ListUsers(ctx)
			if err != nil {
				internalError(c, deps, err)
				return
			}
		} else {
			membership, ok := selectedTeam(c, deps, false)
			if !ok {
				return
			}
			if store.IsTeamAdmin(membership.Role) {
				members, err := deps.Store.ListMembers(ctx, membership.Team.ID)
				if err != nil {
					internalError(c, deps, err)
					return
				}
				for _, member := range members {
					users = append(users, member.User)
				}
			} else {
				users = append(users, *user)
			}
		}
		rows := make([]userPublic, 0, len(users))
		for i := range users {
			record := &users[i]
			row, hasTeam := teamByUser[record.ID]
			if hasTeam {
				rows = append(rows, toPublicUserInTeam(record, &row))
			} else {
				rows = append(rows, toPublicUser(record))
			}
		}
		c.JSON(200, okData(gin.H{"users": rows}))
	})

	group.POST("/users", func(c *gin.Context) {
		admin, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法（密码至少 8 位）"))
			return
		}
		email, okEmail := trimmedRequired(body["email"], 1, 254)
		password, okPassword := body["password"].(string)
		displayName, okName := trimmedRequired(body["displayName"], 1, 80)
		if !okEmail || !emailShape(email) || !okPassword || len(password) < 8 || len(password) > 200 || !okName {
			c.JSON(400, fail(400, "请求参数不合法（密码至少 8 位）"))
			return
		}
		role := store.RoleUser
		if raw, exists := body["role"]; exists {
			roleStr, okRole := raw.(string)
			if !okRole || (roleStr != "admin" && roleStr != "user") {
				c.JSON(400, fail(400, "请求参数不合法（密码至少 8 位）"))
				return
			}
			role = store.UserRole(roleStr)
		}
		// 普通管理员只能创建普通用户；root 不可通过用户管理接口授予。
		if role == store.RoleAdmin && admin.Role != store.RoleRoot {
			c.JSON(403, fail(403, "只有超级管理员可以创建管理员"))
			return
		}
		hash, err := crypto.HashPassword(password)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		user, err := deps.Store.CreateUser(ctx, store.UserInput{
			Email: email, PasswordHash: hash, DisplayName: displayName, Role: role,
		})
		if err != nil {
			if errors.Is(err, store.ErrEmailTaken) {
				c.JSON(409, fail(409, err.Error()))
				return
			}
			internalError(c, deps, err)
			return
		}
		writeAudit(ctx, deps, admin, "user.create", "user", user.ID, map[string]any{"role": string(user.Role)})
		c.JSON(200, okData(gin.H{"user": toPublicUser(user)}))
	})

	group.PATCH("/users/:id", func(c *gin.Context) {
		admin, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法（密码至少 8 位）"))
			return
		}
		var (
			hasDisabled bool
			disabledVal bool
			hasPassword bool
			password    string
			displayName *string
			role        *store.UserRole
			auditFields []string
		)
		if raw, exists := body["disabled"]; exists {
			parsed, okBool := boolValue(raw)
			if !okBool {
				c.JSON(400, fail(400, "请求参数不合法（密码至少 8 位）"))
				return
			}
			hasDisabled, disabledVal = true, parsed
			auditFields = append(auditFields, "disabled")
		}
		if raw, exists := body["password"]; exists {
			parsed, okPassword := raw.(string)
			if !okPassword || len(parsed) < 8 || len(parsed) > 200 {
				c.JSON(400, fail(400, "请求参数不合法（密码至少 8 位）"))
				return
			}
			hasPassword, password = true, parsed
			auditFields = append(auditFields, "password")
		}
		if raw, exists := body["displayName"]; exists {
			parsed, okName := trimmedRequired(raw, 1, 80)
			if !okName {
				c.JSON(400, fail(400, "请求参数不合法（密码至少 8 位）"))
				return
			}
			displayName = &parsed
			auditFields = append(auditFields, "displayName")
		}
		if raw, exists := body["role"]; exists {
			roleStr, okRole := raw.(string)
			if !okRole || (roleStr != "admin" && roleStr != "user") {
				c.JSON(400, fail(400, "请求参数不合法（密码至少 8 位）"))
				return
			}
			parsed := store.UserRole(roleStr)
			role = &parsed
			auditFields = append(auditFields, "role")
		}
		targetID := c.Param("id")
		target, err := deps.Store.FindUserByID(ctx, targetID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if target == nil {
			c.JSON(404, fail(404, "用户不存在"))
			return
		}
		isSelf := target.ID == admin.ID
		// 自我保护:任何人都不能禁用自己或变更自己的角色。
		if isSelf && (hasDisabled && disabledVal || role != nil) {
			c.JSON(400, fail(400, "不能禁用自己的账号或变更自己的角色"))
			return
		}
		// root 账号只能由自己改密码/显示名；不能被禁用、降级或由他人操作。
		if target.Role == store.RoleRoot {
			if !isSelf {
				c.JSON(403, fail(403, "不能操作超级管理员账号"))
				return
			}
			if hasDisabled {
				c.JSON(400, fail(400, "超级管理员不能被禁用"))
				return
			}
			if role != nil {
				c.JSON(400, fail(400, "超级管理员不能被降级"))
				return
			}
		}
		if hasPassword && target.Role == store.RoleRoot && len(password) < rootPasswordMinLen {
			c.JSON(400, fail(400, "超级管理员密码至少 12 位"))
			return
		}
		// 普通管理员只能管理普通用户；创建/升降管理员是 root 专属。
		if admin.Role != store.RoleRoot {
			if !isSelf && target.Role != store.RoleUser {
				c.JSON(403, fail(403, "管理员只能管理普通用户"))
				return
			}
			if role != nil {
				c.JSON(403, fail(403, "只有超级管理员可以调整角色"))
				return
			}
		}
		patch := store.UpdateUserPatch{DisplayName: displayName, Role: role}
		if hasDisabled {
			patch.HasDisabled = true
			if disabledVal {
				now := deps.Now()
				patch.DisabledAt = &now
			}
		}
		if hasPassword {
			hash, err := crypto.HashPassword(password)
			if err != nil {
				internalError(c, deps, err)
				return
			}
			patch.PasswordHash = &hash
		}
		user, err := deps.Store.UpdateUser(ctx, targetID, patch)
		if err != nil {
			switch {
			case errors.Is(err, store.ErrRootUserProtected):
				c.JSON(400, fail(400, "超级管理员不能被禁用或降级"))
			case errors.Is(err, store.ErrRootUserExists):
				c.JSON(409, fail(409, "超级管理员已存在"))
			case errors.Is(err, store.ErrLastTeamAdmin):
				c.JSON(409, fail(409, "请先为团队设置其他有效管理员"))
			default:
				internalError(c, deps, err)
			}
			return
		}
		if user == nil {
			c.JSON(404, fail(404, "用户不存在"))
			return
		}
		// 重置密码 / 禁用 / 降级后吊销全部登录态(启用不触发回补)。
		// 登录流也是凭据签发入口：已授权未消费的流若不作废，仍可在有效期内兑换新设备 token。
		if hasPassword || (hasDisabled && disabledVal) || role != nil {
			if err := deps.Store.DeleteSessionsForUser(ctx, user.ID); err != nil {
				internalError(c, deps, err)
				return
			}
			if _, err := deps.Store.RevokeDeviceTokensForUser(ctx, user.ID, deps.Now()); err != nil {
				internalError(c, deps, err)
				return
			}
			if _, err := deps.Store.RevokeLoginFlowsForUser(ctx, user.ID, deps.Now()); err != nil {
				internalError(c, deps, err)
				return
			}
		}
		writeAudit(ctx, deps, admin, "user.update", "user", user.ID, map[string]any{"fields": auditFields})
		if hasPassword {
			notifySecurityEvent(deps, "user.password.reset", "管理员重置了用户密码 target="+user.ID+" actor="+admin.ID)
		}
		c.JSON(200, okData(gin.H{"user": toPublicUser(user)}))
	})

	// ---- releases ----

	group.GET("/releases", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		releases, err := deps.Store.ListReleases(c.Request.Context())
		if err != nil {
			internalError(c, deps, err)
			return
		}
		rows := make([]releaseSummary, 0, len(releases))
		for _, release := range releases {
			rows = append(rows, releaseSummary{
				Revision: release.Revision, PublishedAt: isoString(release.PublishedAt), IsActive: release.IsActive,
			})
		}
		c.JSON(200, okData(gin.H{"releases": rows}))
	})

	group.GET("/releases/:revision", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		ctx := c.Request.Context()
		revision, ok := parseLeadingInt(c.Param("revision"))
		var release *store.ReleaseRecord
		var err error
		if ok {
			release, err = deps.Store.GetRelease(ctx, revision)
			if err != nil {
				internalError(c, deps, err)
				return
			}
		}
		if release == nil {
			c.JSON(404, fail(404, "Release 不存在"))
			return
		}
		content, err := jsonDecode(release.Content)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.JSON(200, okData(gin.H{"revision": release.Revision, "content": content}))
	})

	group.POST("/releases", func(c *gin.Context) {
		admin, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		candidate, exists := body["content"]
		if !exists {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		if text, isString := candidate.(string); isString {
			var parsed any
			if err := json.Unmarshal([]byte(text), &parsed); err != nil {
				c.JSON(400, fail(400, "内容不是合法 JSON"))
				return
			}
			candidate = parsed
		}
		looseRevision, content, err := contracts.ParseBuiltinReleaseLoose(candidate)
		if err != nil {
			c.JSON(400, fail(400, err.Error()))
			return
		}
		_ = looseRevision
		max, err := deps.Store.GetMaxReleaseRevision(ctx)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		var revision int64
		if max != nil {
			revision = *max + 1
		} else {
			revision = 1
		}
		encoded, err := jsonMarshalNoHTML(content)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if _, err := deps.Store.InsertRelease(ctx, store.InsertReleaseInput{
			Revision: revision, Content: encoded, PublishedBy: admin.ID,
		}); err != nil {
			c.JSON(400, fail(400, err.Error()))
			return
		}
		if _, err := deps.Store.ActivateRelease(ctx, revision); err != nil {
			internalError(c, deps, err)
			return
		}
		writeAudit(ctx, deps, admin, "release.publish", "release", formatInt(revision), nil)
		deps.ConfigEvents.Emit(events.DomainReleases)
		c.JSON(200, okData(gin.H{"revision": revision}))
	})

	group.POST("/releases/:revision/activate", func(c *gin.Context) {
		admin, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		revision, parsedOK := parseLeadingInt(c.Param("revision"))
		var release *store.ReleaseRecord
		var err error
		if parsedOK {
			release, err = deps.Store.ActivateRelease(ctx, revision)
			if err != nil {
				internalError(c, deps, err)
				return
			}
		}
		if release == nil {
			c.JSON(404, fail(404, "Release 不存在"))
			return
		}
		deps.ConfigEvents.Emit(events.DomainReleases)
		writeAudit(ctx, deps, admin, "release.activate", "release", formatInt(release.Revision), nil)
		c.JSON(200, okData(gin.H{"revision": release.Revision}))
	})

	// ---- models ----

	group.GET("/models", func(c *gin.Context) {
		user, ok := requireConsoleUser(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		var policy *store.TeamPolicyRecord
		if user.Role != store.RoleRoot {
			membership, ok := selectedTeam(c, deps, false)
			if !ok {
				return
			}
			var err error
			policy, err = deps.Store.GetTeamPolicy(c.Request.Context(), membership.Team.ID)
			if err != nil {
				internalError(c, deps, err)
				return
			}
		}
		models, err := deps.Store.ListModels(c.Request.Context(), user.Role == store.RoleRoot && c.Query("includeDisabled") == "1")
		if err != nil {
			internalError(c, deps, err)
			return
		}
		rows := make([]any, 0, len(models))
		for i := range models {
			model := &models[i]
			if user.Role == store.RoleRoot {
				rows = append(rows, toPublicModel(model))
				continue
			}
			if !store.TeamModelAllowed(policy, model.ModelID) {
				continue
			}
			// 售价属于平台经营信息，普通用户目录只下发展示与排序所需字段。
			rows = append(rows, gin.H{"modelId": model.ModelID, "displayName": store.ModelDisplayName(model.ModelID, model.DisplayName),
				"enabled": model.Enabled, "sortOrder": model.SortOrder})
		}
		c.JSON(200, okData(gin.H{"models": rows}))
	})

	registerModelDisplayRoutes(group, deps)
	registerModelProbeRoute(group, deps)
	registerModelCapabilityRoutes(group, deps)

	group.PUT("/models/:modelId", func(c *gin.Context) {
		admin, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		displayName, okName := trimmedRequired(body["displayName"], 1, 120)
		if !okName {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		inputPrice, okInput := parsePriceString(body["inputPricePerMtok"])
		outputPrice, okOutput := parsePriceString(body["outputPricePerMtok"])
		if !okInput || !okOutput {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		cacheRead := "0.000000"
		if raw, exists := body["cacheReadPricePerMtok"]; exists {
			parsed, okPrice := parsePriceString(raw)
			if !okPrice {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			cacheRead = parsed
		}
		cacheWrite := "0.000000"
		if raw, exists := body["cacheWritePricePerMtok"]; exists {
			parsed, okPrice := parsePriceString(raw)
			if !okPrice {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			cacheWrite = parsed
		}
		offPeak := "1.000000"
		if raw, exists := body["offPeakMultiplier"]; exists {
			parsed, okPrice := parsePriceString(raw)
			if !okPrice {
				c.JSON(400, fail(400, "offPeakMultiplier 必须是非负数"))
				return
			}
			multiplier, err := strconv.ParseFloat(parsed, 64)
			if err != nil || multiplier > 10 {
				c.JSON(400, fail(400, "offPeakMultiplier 不能超过 10"))
				return
			}
			offPeak = parsed
		}
		enabled := true
		if raw, exists := body["enabled"]; exists {
			parsed, okBool := boolValue(raw)
			if !okBool {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			enabled = parsed
		}
		sortOrder := int64(0)
		if raw, exists := body["sortOrder"]; exists {
			parsed, okInt := intInRange(raw, -10000, 10000)
			if !okInt {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			sortOrder = parsed
		}
		// upstreamModel / upstreamProviderId 恒随请求下发:null = 解除绑定。
		upstreamModel := ""
		if raw, exists := body["upstreamModel"]; exists {
			if raw != nil {
				parsed, okModel := trimmedOptional(raw, 200)
				if !okModel {
					c.JSON(400, fail(400, "请求参数不合法"))
					return
				}
				upstreamModel = parsed
			}
		}
		providerID := ""
		if raw, exists := body["upstreamProviderId"]; exists && raw != nil {
			parsed, okUUID := raw.(string)
			if !okUUID || !uuidShape(parsed) {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			providerID = parsed
		}
		model, err := deps.Store.UpsertModel(ctx, store.UpsertModelInput{
			ModelID: c.Param("modelId"), DisplayName: displayName,
			ProvideUpstreamModel: true, UpstreamModel: upstreamModel,
			ProvideUpstreamProviderID: true, UpstreamProviderID: providerID,
			InputPricePerMtok: inputPrice, OutputPricePerMtok: outputPrice,
			CacheReadPricePerMtok: &cacheRead, CacheWritePricePerMtok: &cacheWrite,
			OffPeakMultiplier: &offPeak,
			Enabled:           &enabled, SortOrder: &sortOrder,
		})
		if err != nil {
			internalError(c, deps, err)
			return
		}
		deps.ConfigEvents.Emit(events.DomainModels)
		writeAudit(ctx, deps, admin, "model.upsert", "model", model.ModelID, map[string]any{"displayName": store.ModelDisplayName(model.ModelID, model.DisplayName)})
		c.JSON(200, okData(gin.H{"model": toPublicModel(model)}))
	})

	group.POST("/models/import", func(c *gin.Context) {
		admin, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		providerID, okProvider := body["providerId"].(string)
		if !okProvider || !uuidShape(providerID) {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		rawIDs, okIDs := body["modelIds"].([]any)
		if !okIDs || len(rawIDs) < 1 || len(rawIDs) > 500 {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		provider, err := deps.Store.GetUpstreamProvider(ctx, providerID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if provider == nil {
			c.JSON(404, fail(404, "供应商不存在"))
			return
		}
		requested := make([]string, 0, len(rawIDs))
		seen := map[string]struct{}{}
		for _, raw := range rawIDs {
			modelID, okModelID := trimmedRequired(raw, 1, 200)
			if !okModelID {
				c.JSON(400, fail(400, "请求参数不合法"))
				return
			}
			if _, exists := seen[modelID]; exists {
				continue
			}
			seen[modelID] = struct{}{}
			requested = append(requested, modelID)
		}
		pricingEntries, err := loadPricingEntries(ctx, deps.Store)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		pricingMap := map[string]int{}
		for i, entry := range pricingEntries {
			pricingMap[entry.ModelID] = i
		}
		created := []string{}
		priced := []string{}
		skipped := []string{}
		for _, modelID := range requested {
			existing, err := deps.Store.GetModel(ctx, modelID)
			if err != nil {
				internalError(c, deps, err)
				return
			}
			if existing != nil {
				skipped = append(skipped, modelID)
				continue
			}
			var hit *pricing.PricingEntry
			if index, ok := pricingMap[modelID]; ok {
				hit = &pricingEntries[index]
				priced = append(priced, modelID)
			}
			displayName := modelID
			inputPrice, outputPrice, cacheRead, cacheWrite := "0.000000", "0.000000", "0.000000", "0.000000"
			if hit != nil {
				displayName = hit.DisplayName
				inputPrice, outputPrice = hit.InputCostPerMillion, hit.OutputCostPerMillion
				cacheRead, cacheWrite = hit.CacheReadCostPerMillion, hit.CacheCreationCostPerMillion
			}
			enabled := false
			sortOrder := int64(0)
			if _, err := deps.Store.UpsertModel(ctx, store.UpsertModelInput{
				ModelID: modelID, DisplayName: displayName,
				ProvideUpstreamModel: true, UpstreamModel: modelID,
				ProvideUpstreamProviderID: true, UpstreamProviderID: provider.ID,
				InputPricePerMtok: inputPrice, OutputPricePerMtok: outputPrice,
				CacheReadPricePerMtok: &cacheRead, CacheWritePricePerMtok: &cacheWrite,
				Enabled: &enabled, SortOrder: &sortOrder,
			}); err != nil {
				internalError(c, deps, err)
				return
			}
			created = append(created, modelID)
		}
		if len(created) > 0 {
			deps.ConfigEvents.Emit(events.DomainModels)
		}
		writeAudit(ctx, deps, admin, "model.import", "provider", provider.ID, map[string]any{
			"created": len(created), "priced": len(priced), "skipped": len(skipped),
		})
		c.JSON(200, okData(gin.H{"created": created, "priced": priced, "skipped": skipped}))
	})

	group.DELETE("/models/:modelId", func(c *gin.Context) {
		admin, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		modelID := c.Param("modelId")
		removed, err := deps.Store.DeleteModel(ctx, modelID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if !removed {
			c.JSON(404, fail(404, "模型不存在"))
			return
		}
		deps.ConfigEvents.Emit(events.DomainModels)
		writeAudit(ctx, deps, admin, "model.delete", "model", modelID, nil)
		c.JSON(200, gin.H{"code": 0})
	})
}

func formatInt(value int64) string {
	return strconv.FormatInt(value, 10)
}
