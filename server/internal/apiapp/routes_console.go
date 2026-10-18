package apiapp

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/contracts"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/pricing"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// console.ts 的镜像:全部位于 /console/api/*,要求 admin 角色的会话。

func registerConsoleRoutes(router *gin.Engine, deps *Deps) {
	group := router.Group("/console/api")

	// ---- auth ----

	group.POST("/auth/login", func(c *gin.Context) {
		ctx := c.Request.Context()
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		email, okEmail := trimmedRequired(body["email"], 3, 1<<30)
		password, okPassword := body["password"].(string)
		if !okEmail || !okPassword || password == "" {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		ip := "local"
		if xff := c.GetHeader("x-forwarded-for"); xff != "" {
			if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
				ip = first
			}
		}
		allowed, err := deps.ConsoleLoginLimiter.Allow(ctx, ip+":"+strings.ToLower(email))
		if err != nil {
			deps.Logger.Warn("登录限流不可用，按 fail-open 放行", "error", err)
			allowed = true
		}
		if !allowed {
			c.JSON(429, fail(429, "尝试次数过多，请稍后再试"))
			return
		}
		user, err := deps.Store.FindUserByEmail(ctx, email)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if user == nil || user.DisabledAt != nil || user.Role != store.RoleAdmin ||
			!crypto.VerifyPassword(password, user.PasswordHash) {
			c.JSON(401, fail(401, "邮箱或密码不正确"))
			return
		}
		if err := loginWithSession(ctx, deps, c, user.ID); err != nil {
			internalError(c, deps, err)
			return
		}
		c.JSON(200, okData(gin.H{"user": toPublicUser(user)}))
	})

	group.POST("/auth/logout", func(c *gin.Context) {
		if err := logoutSession(c.Request.Context(), deps, c); err != nil {
			internalError(c, deps, err)
			return
		}
		c.JSON(200, gin.H{"code": 0})
	})

	group.GET("/auth/me", func(c *gin.Context) {
		admin, ok := requireAdmin(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		c.JSON(200, okData(gin.H{"user": toPublicUser(admin)}))
	})

	// ---- teams ----

	group.GET("/teams", func(c *gin.Context) {
		if _, ok := requireAdmin(c.Request.Context(), deps, c); !ok {
			return
		}
		teams, err := deps.Store.ListTeams(c.Request.Context())
		if err != nil {
			internalError(c, deps, err)
			return
		}
		rows := make([]any, 0, len(teams))
		for _, entry := range teams {
			row := toPublicTeam(&entry.Team)
			rows = append(rows, gin.H{
				"id": row.ID, "name": row.Name, "quotaCredits": row.QuotaCredits,
				"usedCredits": row.UsedCredits, "reservedCredits": row.ReservedCredits,
				"unlimited": row.Unlimited, "createdAt": row.CreatedAt, "disabledAt": row.DisabledAt,
				"memberCount": entry.MemberCount,
			})
		}
		c.JSON(200, okData(gin.H{"teams": rows}))
	})

	group.POST("/teams", func(c *gin.Context) {
		if _, ok := requireAdmin(c.Request.Context(), deps, c); !ok {
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
		if _, ok := requireAdmin(c.Request.Context(), deps, c); !ok {
			return
		}
		ctx := c.Request.Context()
		teamID := c.Param("id")
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
		aggregates, err := deps.Store.AggregateUsage(ctx, store.UsageFilter{TeamID: teamID})
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
			cursor := encodeUsageCursor(isoString(last.CreatedAt), last.ID)
			nextCursor = &cursor
		}
		memberRows := make([]any, 0, len(members))
		for _, member := range members {
			memberRows = append(memberRows, gin.H{
				"user":     toPublicUser(&member.User),
				"role":     string(member.Role),
				"joinedAt": isoString(member.CreatedAt),
			})
		}
		aggregateRows := make([]aggregatePublic, 0, len(aggregates))
		for _, row := range aggregates {
			aggregateRows = append(aggregateRows, toPublicAggregate(row))
		}
		usageRows := make([]usagePublic, 0, len(recent))
		for i := range recent {
			usageRows = append(usageRows, toPublicUsage(&recent[i]))
		}
		adjustmentRows := make([]quotaAdjustmentPublic, 0, len(quotaAdjustments))
		for _, row := range quotaAdjustments {
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
		var patch store.UpdateTeamPatch
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
		// 额度调整流水:旧值必须在 update 之前取标量快照。
		previousTeam, err := deps.Store.GetTeam(ctx, teamID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if previousTeam == nil {
			c.JSON(404, fail(404, "团队不存在"))
			return
		}
		previousQuota, previousUnlimited, previousName := previousTeam.QuotaCredits, previousTeam.Unlimited, previousTeam.Name
		team, err := deps.Store.UpdateTeam(ctx, teamID, patch)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if team == nil {
			c.JSON(404, fail(404, "团队不存在"))
			return
		}
		if patch.QuotaCredits != nil || patch.Unlimited != nil {
			if err := deps.Store.InsertQuotaAdjustment(ctx, store.InsertQuotaAdjustmentInput{
				TeamID: teamID, ActorUserID: admin.ID, Action: "set_quota",
				OldQuotaCredits: previousQuota, NewQuotaCredits: team.QuotaCredits,
				OldUnlimited: previousUnlimited, NewUnlimited: team.Unlimited,
			}); err != nil {
				internalError(c, deps, err)
				return
			}
		}
		changed := []string{}
		if patch.Name != nil && *patch.Name != previousName {
			changed = append(changed, "name")
		}
		if patch.RPMLimit != nil || patch.TPMLimit != nil || patch.ConcurrencyLimit != nil {
			changed = append(changed, "limits")
		}
		if patch.QuotaCredits != nil || patch.Unlimited != nil {
			changed = append(changed, "quota")
		}
		if len(changed) > 0 {
			writeAudit(ctx, deps, admin, "team.update", "team", team.ID, map[string]any{"fields": changed})
		}
		deps.ConfigEvents.Emit(events.DomainTeams)
		c.JSON(200, okData(gin.H{"team": toPublicTeam(team)}))
	})

	group.POST("/teams/:id/members", func(c *gin.Context) {
		if _, ok := requireAdmin(c.Request.Context(), deps, c); !ok {
			return
		}
		ctx := c.Request.Context()
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		email, okEmail := trimmedRequired(body["email"], 3, 1<<30)
		if !okEmail {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		role := store.MemberRoleMember
		if raw, exists := body["role"]; exists {
			roleStr, okRole := raw.(string)
			if !okRole || (roleStr != "owner" && roleStr != "member") {
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
			internalError(c, deps, err)
			return
		}
		deps.ConfigEvents.Emit(events.DomainTeams)
		c.JSON(200, okData(gin.H{"user": toPublicUser(user)}))
	})

	group.DELETE("/teams/:id/members/:userId", func(c *gin.Context) {
		if _, ok := requireAdmin(c.Request.Context(), deps, c); !ok {
			return
		}
		removed, err := deps.Store.RemoveMember(c.Request.Context(), c.Param("id"), c.Param("userId"))
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if !removed {
			c.JSON(404, fail(404, "成员不存在"))
			return
		}
		deps.ConfigEvents.Emit(events.DomainTeams)
		c.JSON(200, gin.H{"code": 0})
	})

	// ---- users ----

	group.GET("/users", func(c *gin.Context) {
		if _, ok := requireAdmin(c.Request.Context(), deps, c); !ok {
			return
		}
		users, err := deps.Store.ListUsers(c.Request.Context())
		if err != nil {
			internalError(c, deps, err)
			return
		}
		rows := make([]userPublic, 0, len(users))
		for i := range users {
			rows = append(rows, toPublicUser(&users[i]))
		}
		c.JSON(200, okData(gin.H{"users": rows}))
	})

	group.POST("/users", func(c *gin.Context) {
		admin, ok := requireAdmin(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法（密码至少 8 位）"))
			return
		}
		email, okEmail := trimmedRequired(body["email"], 1, 1<<30)
		password, okPassword := body["password"].(string)
		displayName, okName := trimmedRequired(body["displayName"], 1, 80)
		if !okEmail || !emailShape(email) || !okPassword || len(password) < 8 || len(password) > 200 || !okName {
			c.JSON(400, fail(400, "请求参数不合法（密码至少 8 位）"))
			return
		}
		role := store.RoleMember
		if raw, exists := body["role"]; exists {
			roleStr, okRole := raw.(string)
			if !okRole || (roleStr != "admin" && roleStr != "member") {
				c.JSON(400, fail(400, "请求参数不合法（密码至少 8 位）"))
				return
			}
			role = store.UserRole(roleStr)
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
			c.JSON(409, fail(409, err.Error()))
			return
		}
		writeAudit(ctx, deps, admin, "user.create", "user", user.ID, map[string]any{"role": string(user.Role)})
		c.JSON(200, okData(gin.H{"user": toPublicUser(user)}))
	})

	group.PATCH("/users/:id", func(c *gin.Context) {
		admin, ok := requireAdmin(c.Request.Context(), deps, c)
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
			if !okRole || (roleStr != "admin" && roleStr != "member") {
				c.JSON(400, fail(400, "请求参数不合法（密码至少 8 位）"))
				return
			}
			parsed := store.UserRole(roleStr)
			role = &parsed
			auditFields = append(auditFields, "role")
		}
		targetID := c.Param("id")
		// 自我保护:避免管理员禁用/降级自己导致无人可管理控制台。
		if targetID == admin.ID && (hasDisabled && disabledVal || role != nil) {
			c.JSON(400, fail(400, "不能禁用自己的账号或变更自己的角色"))
			return
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
			internalError(c, deps, err)
			return
		}
		if user == nil {
			c.JSON(404, fail(404, "用户不存在"))
			return
		}
		// 重置密码 / 禁用 / 降级后吊销全部登录态(启用不触发回补)。
		if hasPassword || (hasDisabled && disabledVal) || role != nil {
			if err := deps.Store.DeleteSessionsForUser(ctx, user.ID); err != nil {
				internalError(c, deps, err)
				return
			}
			if _, err := deps.Store.RevokeDeviceTokensForUser(ctx, user.ID, deps.Now()); err != nil {
				internalError(c, deps, err)
				return
			}
		}
		writeAudit(ctx, deps, admin, "user.update", "user", user.ID, map[string]any{"fields": auditFields})
		c.JSON(200, okData(gin.H{"user": toPublicUser(user)}))
	})

	// ---- releases ----

	group.GET("/releases", func(c *gin.Context) {
		if _, ok := requireAdmin(c.Request.Context(), deps, c); !ok {
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
		if _, ok := requireAdmin(c.Request.Context(), deps, c); !ok {
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
		admin, ok := requireAdmin(c.Request.Context(), deps, c)
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
		if _, ok := requireAdmin(c.Request.Context(), deps, c); !ok {
			return
		}
		models, err := deps.Store.ListModels(c.Request.Context(), c.Query("includeDisabled") == "1")
		if err != nil {
			internalError(c, deps, err)
			return
		}
		rows := make([]modelPublic, 0, len(models))
		for i := range models {
			rows = append(rows, toPublicModel(&models[i]))
		}
		c.JSON(200, okData(gin.H{"models": rows}))
	})

	group.PUT("/models/:modelId", func(c *gin.Context) {
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
		writeAudit(ctx, deps, admin, "model.upsert", "model", model.ModelID, map[string]any{"displayName": model.DisplayName})
		c.JSON(200, okData(gin.H{"model": toPublicModel(model)}))
	})

	group.POST("/models/import", func(c *gin.Context) {
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
		admin, ok := requireAdmin(c.Request.Context(), deps, c)
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
