package apiapp

import (
	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/media"
)

func registerMediaPreviewRoutes(router *gin.Engine, deps *Deps) {
	cache := media.NewPreviewCache()
	handler := func(c *gin.Context) {
		s := deps.Media
		team, id := c.Param("team"), c.Param("id")
		if !s.Blobs.Verify(team, id, c.Query("expires"), c.Query("signature")) {
			c.Status(403)
			return
		}
		assets, err := s.Repo.Assets(c.Request.Context(), team)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		for _, asset := range assets {
			if asset.ID != id || asset.Internal || (asset.Kind != "image" && asset.Kind != "video") {
				continue
			}
			path, err := cache.Get(c.Request.Context(), s.Blobs, team, asset)
			if err != nil {
				// 缩略图失败只影响当前读取；不得重试原始生成或改写原件/任务状态。
				c.Header("Cache-Control", "no-store")
				c.JSON(422, fail(422, "media_preview_failed"))
				return
			}
			c.Header("Cross-Origin-Resource-Policy", "cross-origin")
			c.Header("Content-Type", "image/jpeg")
			c.Header("Content-Disposition", "inline")
			c.Header("Cache-Control", "private, max-age=300")
			c.File(path)
			return
		}
		c.Status(404)
	}
	router.GET("/api/v1/media/previews/:team/:id", handler)
	router.HEAD("/api/v1/media/previews/:team/:id", handler)
}
