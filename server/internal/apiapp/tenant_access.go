package apiapp

import (
	"context"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

const consoleUserKey = "console.user"
const consoleTeamKey = "console.team"

func requireConsoleUser(ctx context.Context, deps *Deps, c *gin.Context) (*store.UserRecord, bool) {
	if cached, exists := c.Get(consoleUserKey); exists {
		return cached.(*store.UserRecord), true
	}
	user, err := readSessionUser(ctx, deps, c)
	if err != nil {
		internalError(c, deps, err)
		return nil, false
	}
	if user == nil {
		c.JSON(401, fail(401, "请先登录"))
		return nil, false
	}
	c.Set(consoleUserKey, user)
	return user, true
}

// consoleAccessGuard 默认拒绝非 root 的平台管理接口；新增路由必须显式声明团队边界。
// 根因是过去仅检查全局 admin 身份，任意 teamId 都会被当作合法的管理范围。
func consoleAccessGuard(deps *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 跨域预检没有会话 cookie，交给后续限定 origin 的 CORS 中间件处理。
		if c.Request.Method == "OPTIONS" {
			c.Next()
			return
		}
		if !strings.HasPrefix(c.Request.URL.Path, "/console/api/") {
			c.Next()
			return
		}
		path := strings.TrimPrefix(c.FullPath(), "/console/api")
		if strings.HasPrefix(path, "/setup/") || path == "/auth/login" ||
			path == "/auth/logout" || path == "/invitations/accept" || path == "/public-config" {
			c.Next()
			return
		}
		user, ok := requireConsoleUser(c.Request.Context(), deps, c)
		if !ok {
			c.Abort()
			return
		}
		c.Header("Cache-Control", "no-store")
		if teamID := c.Query("teamId"); teamID != "" && !uuidShape(teamID) {
			c.AbortWithStatusJSON(400, fail(400, "teamId 不合法"))
			return
		}
		if user.Role == store.RoleRoot {
			c.Next()
			return
		}
		read := c.Request.Method == "GET"
		allowed := path == "/auth/me" || path == "/profile" ||
			(read && (path == "/teams" || path == "/users" || path == "/models" || path == "/devices" || path == "/quota-requests" || strings.HasPrefix(path, "/usage"))) ||
			path == "/devices/:id/revoke" || path == "/audit" || strings.HasPrefix(path, "/teams/:id")
		if !allowed {
			c.AbortWithStatusJSON(403, fail(403, "此功能仅限超级管理员"))
			return
		}
		if strings.HasPrefix(path, "/teams/:id") {
			if _, ok := requireTeamAccess(c, deps, c.Param("id"), !read); !ok {
				c.Abort()
				return
			}
		}
		c.Next()
	}
}

func requireTeamAccess(c *gin.Context, deps *Deps, teamID string, manage bool) (*store.TeamMembership, bool) {
	user, ok := requireConsoleUser(c.Request.Context(), deps, c)
	if !ok {
		return nil, false
	}
	if !uuidShape(teamID) {
		c.JSON(400, fail(400, "团队 ID 不合法"))
		return nil, false
	}
	if user.Role == store.RoleRoot {
		team, err := deps.Store.GetTeam(c.Request.Context(), teamID)
		if err != nil {
			internalError(c, deps, err)
			return nil, false
		}
		if team == nil {
			c.JSON(404, fail(404, "团队不存在"))
			return nil, false
		}
		membership := &store.TeamMembership{Team: *team, Role: store.MemberRoleOwner}
		c.Set(consoleTeamKey, membership)
		return membership, true
	}
	memberships, err := deps.Store.ListTeamsForUser(c.Request.Context(), user.ID)
	if err != nil {
		internalError(c, deps, err)
		return nil, false
	}
	for _, membership := range memberships {
		if membership.Team.ID != teamID || membership.Team.DisabledAt != nil || membership.MemberDisabledAt != nil {
			continue
		}
		if manage && !store.IsTeamAdmin(membership.Role) {
			c.JSON(403, fail(403, "需要本团队管理员权限"))
			return nil, false
		}
		c.Set(consoleTeamKey, &membership)
		return &membership, true
	}
	c.JSON(403, fail(403, "没有该团队的访问权限"))
	return nil, false
}

func selectedTeam(c *gin.Context, deps *Deps, manage bool) (*store.TeamMembership, bool) {
	if cached, exists := c.Get(consoleTeamKey); exists {
		membership := cached.(*store.TeamMembership)
		if !manage || store.IsTeamAdmin(membership.Role) {
			return membership, true
		}
	}
	user, ok := requireConsoleUser(c.Request.Context(), deps, c)
	if !ok {
		return nil, false
	}
	if teamID := c.Query("teamId"); teamID != "" {
		return requireTeamAccess(c, deps, teamID, manage)
	}
	membership, err := store.ResolveUserTeam(c.Request.Context(), deps.Store, user.ID)
	if err != nil {
		if errors.Is(err, store.ErrAmbiguousTeam) {
			c.JSON(409, fail(409, "账号存在多个团队归属，请联系超级管理员处理"))
		} else {
			internalError(c, deps, err)
		}
		return nil, false
	}
	if membership == nil {
		c.JSON(403, fail(403, "尚未加入可用团队，或团队访问已暂停"))
		return nil, false
	}
	if manage && !store.IsTeamAdmin(membership.Role) {
		c.JSON(403, fail(403, "需要本团队管理员权限"))
		return nil, false
	}
	c.Set(consoleTeamKey, membership)
	return membership, true
}

func scopeUsageFilter(c *gin.Context, deps *Deps, filter *store.UsageFilter) bool {
	user, ok := requireConsoleUser(c.Request.Context(), deps, c)
	if !ok {
		return false
	}
	if user.Role == store.RoleRoot {
		return true
	}
	membership, ok := selectedTeam(c, deps, false)
	if !ok {
		return false
	}
	filter.TeamID = membership.Team.ID
	filter.ProviderID = ""
	if !store.IsTeamAdmin(membership.Role) {
		filter.UserID = user.ID
	}
	return true
}

func publicMemberships(ctx context.Context, deps *Deps, user *store.UserRecord) ([]gin.H, error) {
	memberships, err := deps.Store.ListTeamsForUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	rows := make([]gin.H, 0, len(memberships))
	for _, membership := range memberships {
		// 停用关系不再具备团队读取权，登录资料也不能成为财务数据旁路。
		if membership.MemberDisabledAt != nil || membership.Team.DisabledAt != nil {
			continue
		}
		rows = append(rows, gin.H{
			"team": toPublicTeam(&membership.Team), "role": string(membership.Role),
			"memberDisabledAt": isoStringPtr(membership.MemberDisabledAt), "monthlyBudgetCredits": membership.MonthlyBudgetCredits,
		})
	}
	return rows, nil
}

func usageForConsole(c *gin.Context, record *store.UsageRecord) any {
	row := toPublicUsage(record)
	user, _ := c.Get(consoleUserKey)
	if user != nil && user.(*store.UserRecord).Role == store.RoleRoot {
		return row
	}
	return gin.H{
		"id": row.ID, "requestId": row.RequestID, "userId": row.UserID, "teamId": row.TeamID,
		"deviceTokenId": row.DeviceTokenID, "model": row.Model, "inputTokens": row.InputTokens,
		"outputTokens": row.OutputTokens, "cacheReadTokens": row.CacheReadTokens,
		"cacheCreationTokens": row.CacheCreationTokens, "creditsMicro": row.CreditsMicro,
		"status": row.Status, "errorType": row.ErrorType, "latencyMs": row.LatencyMs, "createdAt": row.CreatedAt,
	}
}
