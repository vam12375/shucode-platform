package apiapp

import (
	"errors"
	"regexp"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/credits"
	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

var addedCreditAmountPattern = regexp.MustCompile(`^\d{1,12}(\.\d{1,6})?$`)

func registerTeamCreditRoutes(router *gin.Engine, deps *Deps) {
	router.POST("/console/api/teams/:id/credits", func(c *gin.Context) {
		ctx := c.Request.Context()
		actor, ok := requireRoot(ctx, deps, c)
		if !ok {
			return
		}
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		amount, ok := body["amountCredits"].(string)
		micro, err := credits.ParseCreditsToMicro(amount)
		if !ok || !addedCreditAmountPattern.MatchString(amount) || err != nil || micro <= 0 || micro > 999_999_999_999_999_999 {
			c.JSON(400, fail(400, "添加金额必须为正数 credit，最多 6 位小数且不得超出额度上限"))
			return
		}
		requestID, _ := body["requestId"].(string)
		key, err := uuid.Parse(requestID)
		if err != nil {
			c.JSON(400, fail(400, "requestId 必须为 UUID"))
			return
		}
		teamID := c.Param("id")
		teamKey, err := uuid.Parse(teamID)
		if err != nil {
			c.JSON(400, fail(400, "团队 ID 不合法"))
			return
		}
		teamID = teamKey.String()
		detail, _ := jsonMarshalNoHTML(map[string]any{"amountCredits": credits.FormatMicroCredits(micro), "requestId": key.String()})
		team, err := deps.Store.UpdateTeamWithAccounting(ctx, teamID, store.UpdateTeamPatch{QuotaIncreaseMicro: &micro}, "add_credit", &store.InsertAuditLogInput{
			EventID: "team-credit:" + teamID + ":" + actor.ID + ":" + key.String(), TeamID: teamID, ActorUserID: actor.ID,
			Action: "team.credit.add", TargetType: "team", TargetID: teamID, Detail: detail,
		})
		if errors.Is(err, store.ErrInvalidInput) {
			c.JSON(400, fail(400, "添加后总额度超过上限"))
			return
		}
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
}
