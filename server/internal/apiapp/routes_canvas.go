package apiapp

import (
	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/media"
)

func registerCanvasGenerationRoutes(router *gin.Engine, deps *Deps) {
	router.POST("/api/v1/media/generations", func(c *gin.Context) {
		a, m, ok := mediaIdentity(c, deps)
		if !ok {
			return
		}
		var body struct {
			ID      string              `json:"id"`
			Request media.CanvasRequest `json:"request"`
		}
		if !mediaBody(c, &body) {
			return
		}
		p, e := deps.Media.CreateCanvas(c.Request.Context(), m.Team.ID, a.User.ID, a.Device.ID, body.ID, body.Request)
		if e != nil {
			mediaError(c, deps, e)
			return
		}
		c.JSON(200, okData(p.View()))
	})
}
