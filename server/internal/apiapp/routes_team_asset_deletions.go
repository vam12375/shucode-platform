package apiapp

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"github.com/vam12375/shucode-platform/server/internal/teamassets"
)

func registerTeamAssetDeletionRoutes(assets *gin.RouterGroup, deps *Deps, service teamassets.Service, resolveActor func(*gin.Context) (*AuthenticatedDevice, *store.TeamMembership, bool)) {
	assets.GET("/deletion-requests", func(c *gin.Context) {
		auth, membership, ok := resolveActor(c)
		if !ok {
			return
		}
		out, err := service.ListDeletionRequests(c.Request.Context(), membership.Team.ID, auth.User.ID, c.Query("cursor"))
		if err != nil {
			teamAssetRouteError(c, deps, err)
			return
		}
		c.JSON(http.StatusOK, okData(out))
	})
	assets.POST("/:id/deletion-requests", func(c *gin.Context) {
		auth, membership, ok := resolveActor(c)
		if !ok {
			return
		}
		// 团队与上传者不接受客户端指定，申请路径只能引用服务端已保存的上传者事实。
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1025))
		var payload map[string]json.RawMessage
		valid := len(body) == 0
		if len(body) > 0 && len(body) <= 1024 {
			decoder := json.NewDecoder(strings.NewReader(string(body)))
			valid = decoder.Decode(&payload) == nil && payload != nil && len(payload) == 0 && decoder.Decode(new(any)) == io.EOF
		}
		if err != nil || !valid {
			c.JSON(http.StatusBadRequest, fail(http.StatusBadRequest, "team_asset_invalid"))
			return
		}
		out, err := service.RequestDeletion(c.Request.Context(), membership.Team.ID, auth.User.ID, c.Param("id"))
		if err != nil {
			teamAssetRouteError(c, deps, err)
			return
		}
		c.JSON(http.StatusOK, okData(out))
	})
	assets.POST("/deletion-requests/:id/review", func(c *gin.Context) {
		auth, membership, ok := resolveActor(c)
		if !ok {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
		var body struct {
			Decision string `json:"decision"`
		}
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || (body.Decision != "approve" && body.Decision != "reject") {
			c.JSON(http.StatusBadRequest, fail(http.StatusBadRequest, "team_asset_invalid"))
			return
		}
		out, err := service.ReviewDeletion(c.Request.Context(), membership.Team.ID, auth.User.ID, c.Param("id"), body.Decision)
		if err != nil {
			teamAssetRouteError(c, deps, err)
			return
		}
		c.JSON(http.StatusOK, okData(out))
	})
}
