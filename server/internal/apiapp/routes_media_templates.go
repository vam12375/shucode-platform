package apiapp

import (
	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/media"
)

func registerMediaTemplateRoutes(router *gin.Engine, deps *Deps) {
	const path = "/console/api/media/templates/reviews"
	router.GET("/console/api/media/templates", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		templates, err := deps.Media.VideoTemplates(c.Request.Context())
		if err != nil {
			mediaError(c, deps, err)
			return
		}
		c.JSON(200, okData(templates))
	})
	router.GET(path, func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		reviews, err := deps.Media.Repo.TemplateReviews(c.Request.Context())
		if err != nil {
			mediaError(c, deps, err)
			return
		}
		c.JSON(200, okData(reviews))
	})
	router.POST(path, func(c *gin.Context) {
		actor, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		var input media.TemplateReviewInput
		if !mediaBody(c, &input) {
			return
		}
		review, err := deps.Media.ReviewVideoTemplate(c.Request.Context(), actor.ID, input)
		if err != nil {
			mediaError(c, deps, err)
			return
		}
		writeAudit(c.Request.Context(), deps, actor, "media.template.review", "video_template", input.TemplateID, gin.H{"templateVersion": input.TemplateVersion, "modelId": review.ModelID, "approved": review.Approved, "score": review.Score})
		c.JSON(200, okData(review))
	})
}
