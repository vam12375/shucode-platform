package apiapp

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func quotaRequestError(c *gin.Context, deps *Deps, err error) {
	switch {
	case errors.Is(err, store.ErrQuotaRequestReviewed):
		c.JSON(409, fail(409, "额度申请已审核，不能重复处理"))
	case errors.Is(err, store.ErrQuotaRequestInvalid):
		c.JSON(409, fail(409, "额度申请无效、团队已停用或审批后额度超出上限"))
	default:
		internalError(c, deps, err)
	}
}
func quotaRequestAmount(raw any) (string, bool) {
	value, ok := raw.(string)
	if !ok {
		if number, valid := raw.(float64); valid {
			value = strconv.FormatFloat(number, 'f', -1, 64)
		} else {
			return "", false
		}
	}
	normalized, err := store.NormalizeQuotaRequestAmount(value)
	return normalized, err == nil
}

func registerQuotaRequestRoutes(router *gin.Engine, deps *Deps) {
	group := router.Group("/console/api")
	group.GET("/quota-requests", func(c *gin.Context) {
		ctx := c.Request.Context()
		actor, ok := requireAdmin(ctx, deps, c)
		if !ok {
			return
		}
		teamID := c.Query("teamId")
		if actor.Role != store.RoleRoot {
			membership, ok := selectedTeam(c, deps, true)
			if !ok {
				return
			}
			teamID = membership.Team.ID
		}
		records, err := deps.Store.ListQuotaRequests(ctx, teamID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.JSON(200, okData(gin.H{"requests": records}))
	})
	group.POST("/teams/:id/quota-requests", func(c *gin.Context) {
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
		for key := range body {
			if key != "amountCredits" && key != "reason" {
				c.JSON(400, fail(400, "不支持的额度申请字段"))
				return
			}
		}
		amount, valid := quotaRequestAmount(body["amountCredits"])
		if !valid {
			c.JSON(400, fail(400, "申请金额必须大于零，最多12位整数和6位小数"))
			return
		}
		reason, valid := body["reason"].(string)
		reason = strings.TrimSpace(reason)
		if !valid || utf8.RuneCountInString(reason) < 1 || utf8.RuneCountInString(reason) > 500 {
			c.JSON(400, fail(400, "申请原因应为1到500个字符"))
			return
		}
		record := store.QuotaRequestRecord{ID: uuid.NewString(), TeamID: membership.Team.ID, RequestedBy: actor.ID, AmountCredits: amount, Reason: reason, Status: "pending", CreatedAt: deps.Now()}
		if err := deps.Store.CreateQuotaRequest(ctx, record); err != nil {
			quotaRequestError(c, deps, err)
			return
		}
		writeTeamAudit(ctx, deps, actor, record.TeamID, "quota_request.create", "quota_request", record.ID, map[string]any{"amountCredits": record.AmountCredits})
		c.JSON(200, okData(gin.H{"request": record}))
	})
	group.POST("/quota-requests/:id/review", func(c *gin.Context) {
		ctx := c.Request.Context()
		actor, ok := requireRoot(ctx, deps, c)
		if !ok {
			return
		}
		if !uuidShape(c.Param("id")) {
			c.JSON(400, fail(400, "申请 ID 不合法"))
			return
		}
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		for key := range body {
			if key != "decision" && key != "note" {
				c.JSON(400, fail(400, "不支持的审批字段"))
				return
			}
		}
		decision, _ := body["decision"].(string)
		if decision != "approve" && decision != "reject" {
			c.JSON(400, fail(400, "审批结果必须是 approve 或 reject"))
			return
		}
		note := ""
		if raw, exists := body["note"]; exists {
			var valid bool
			note, valid = raw.(string)
			if !valid {
				c.JSON(400, fail(400, "审批说明必须是文本"))
				return
			}
		}
		note = strings.TrimSpace(note)
		if utf8.RuneCountInString(note) > 500 {
			c.JSON(400, fail(400, "审批说明最多500个字符"))
			return
		}
		record, err := deps.Store.ReviewQuotaRequest(ctx, c.Param("id"), actor.ID, decision, note, deps.Now())
		if err != nil {
			quotaRequestError(c, deps, err)
			return
		}
		writeTeamAudit(ctx, deps, actor, record.TeamID, "quota_request."+record.Status, "quota_request", record.ID, map[string]any{"amountCredits": record.AmountCredits})
		if record.Status == "approved" {
			deps.ConfigEvents.Emit(events.DomainTeams)
		}
		c.JSON(200, okData(gin.H{"request": record}))
	})
}
