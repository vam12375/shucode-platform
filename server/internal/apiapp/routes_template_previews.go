package apiapp

import (
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/templatelibrary"
)

func registerTemplatePreviewRoutes(router *gin.Engine, deps *Deps) {
	router.POST("/console/api/template-library/previews", func(c *gin.Context) {
		actor, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, templatelibrary.MaxPreviewSize))
		if err != nil {
			c.JSON(400, fail(400, "素材不得超过 64 MiB"))
			return
		}
		name, err := templatelibrary.SavePreview(deps.Media.Blobs.Root, raw)
		if err != nil {
			libraryError(c, deps, err)
			return
		}
		writeAudit(c.Request.Context(), deps, actor, "template-library.preview.upload", "template-preview", name, nil)
		c.Header("Cache-Control", "no-store")
		c.JSON(200, okData(gin.H{"name": name, "url": templatelibrary.PreviewURL(deps.Media.Blobs.Origin, name)}))
	})
	serve := func(c *gin.Context) {
		name := c.Param("name")
		c.Header("Cache-Control", "no-store")
		if !templatelibrary.ValidPreviewName(name) {
			c.Status(404)
			return
		}
		file, err := os.Open(templatelibrary.PreviewPath(deps.Media.Blobs.Root, name))
		if err != nil {
			c.Status(404)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			c.Status(404)
			return
		}
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
		c.Header("ETag", `"`+strings.Split(name, ".")[0]+`"`)
		c.Header("Cross-Origin-Resource-Policy", "cross-origin")
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("X-Content-Type-Options", "nosniff")
		http.ServeContent(c.Writer, c.Request, name, info.ModTime(), file)
	}
	router.GET("/api/v1/media/template-assets/:name", serve)
	router.HEAD("/api/v1/media/template-assets/:name", serve)
}

func uploadedPreviewExists(deps *Deps, p templatelibrary.Preview) bool {
	if !p.Valid() {
		return false
	}
	for _, name := range []string{p.Cover, p.Video} {
		if name == "" {
			continue
		}
		info, err := os.Stat(templatelibrary.PreviewPath(deps.Media.Blobs.Root, name))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}
