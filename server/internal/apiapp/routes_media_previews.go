package apiapp

import (
	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/media"
)

func registerMediaPreviewRoutes(router *gin.Engine, deps *Deps) {
	cache := media.NewPreviewCache()
	handler := func(c *gin.Context) {
		prepareSignedMediaResponse(c)
		s := deps.Media
		team, id := c.Param("team"), c.Param("id")
		params, valid := signedMediaQuery(c, false)
		if !valid || !s.Blobs.Verify(team, id, params.Get("expires"), params.Get("signature")) {
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
			version := asset.Checksum
			if version == "" {
				version = asset.ID
			}
			version = media.MediaPreviewVersion + "-" + version
			if params.Has("v") && params.Get("v") != version {
				c.Status(403)
				return
			}
			path, err := cache.Get(c.Request.Context(), s.Blobs, team, asset)
			if err != nil {
				// 缩略图失败只影响当前读取；不得重试原始生成或改写原件/任务状态。
				c.Header("Cache-Control", "no-store")
				c.JSON(422, fail(422, "media_preview_failed"))
				return
			}
			serveSignedMediaFile(c, path, "image/jpeg", version, params.Get("expires"))
			return
		}
		c.Status(404)
	}
	router.GET("/api/v1/media/previews/:team/:id", handler)
	router.HEAD("/api/v1/media/previews/:team/:id", handler)
}
