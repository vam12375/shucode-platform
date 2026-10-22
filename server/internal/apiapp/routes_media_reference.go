package apiapp

import (
	"math"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/media"
)

func registerMediaReferenceRoutes(router *gin.Engine, deps *Deps) {
	router.POST("/api/v1/media/references/import", func(c *gin.Context) {
		a, m, ok := mediaIdentity(c, deps)
		if !ok {
			return
		}
		var body struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		}
		if !mediaBody(c, &body) {
			return
		}
		asset, err := deps.Media.ImportReference(c.Request.Context(), m.Team.ID, a.User.ID, body.ID, body.URL)
		if err != nil {
			referenceError(c, deps, err)
			return
		}
		c.JSON(200, okData(asset))
	})
	router.POST("/api/v1/media/references/upload", func(c *gin.Context) {
		_, m, ok := mediaIdentity(c, deps)
		if !ok {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, media.MaxReferenceBytes+(1<<20))
		if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
			c.JSON(413, fail(413, "source_video_too_large"))
			return
		}
		if c.Request.MultipartForm != nil {
			defer c.Request.MultipartForm.RemoveAll()
		}
		file, header, err := c.Request.FormFile("file")
		if err != nil {
			c.JSON(400, fail(400, "source_video_required"))
			return
		}
		defer file.Close()
		asset, err := deps.Media.UploadReference(c.Request.Context(), m.Team.ID, header.Filename, file, "")
		if err != nil {
			referenceError(c, deps, err)
			return
		}
		c.JSON(200, okData(asset))
	})
	router.POST("/api/v1/media/reference-clips", func(c *gin.Context) {
		_, m, ok := mediaIdentity(c, deps)
		if !ok {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, media.MaxVideo+(1<<20))
		if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
			c.JSON(http.StatusRequestEntityTooLarge, fail(http.StatusRequestEntityTooLarge, "video_reference_clip_size_limit"))
			return
		}
		if c.Request.MultipartForm != nil {
			defer c.Request.MultipartForm.RemoveAll()
		}
		file, _, err := c.Request.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, fail(http.StatusBadRequest, "video_reference_clip_required"))
			return
		}
		defer file.Close()
		start, startErr := strconv.ParseFloat(c.Request.FormValue("startSeconds"), 64)
		end, endErr := strconv.ParseFloat(c.Request.FormValue("endSeconds"), 64)
		if c.Request.FormValue("sourceAssetId") == "" || c.Request.FormValue("sourceChecksum") == "" || startErr != nil || endErr != nil || math.IsNaN(start) || math.IsInf(start, 0) || math.IsNaN(end) || math.IsInf(end, 0) {
			c.JSON(http.StatusBadRequest, fail(http.StatusBadRequest, "video_reference_clip_invalid"))
			return
		}
		asset, err := deps.Media.UploadReferenceClip(c.Request.Context(), m.Team.ID, c.Request.FormValue("sourceAssetId"), c.Request.FormValue("sourceChecksum"), start, end, file)
		if err != nil {
			referenceError(c, deps, err)
			return
		}
		c.JSON(http.StatusOK, okData(asset))
	})
	registerSourceParserConfigRoutes(router, deps)
}

func referenceError(c *gin.Context, deps *Deps, err error) {
	status := 422
	switch err.Error() {
	case "source_parser_unconfigured", "source_parser_auth_failed", "source_parser_credits_exhausted", "source_parser_configuration_unavailable", "reference_pricing_unavailable":
		status = 503
	case "source_parser_rate_limited":
		status = 429
	case "insufficient_credits", "budget_exhausted":
		status = 402
	case "team_inactive":
		status = 403
	case "source_parser_unavailable", "source_parse_failed", "source_parser_invalid_response", "source_video_unavailable":
		status = 502
	case "source_import_in_progress", "source_import_unavailable", "source_import_failed", "source_import_settlement_pending":
		status = 409
	case "source_video_too_large":
		status = 413
	case "invalid_source_url", "invalid_source_video", "source_video_probe_failed", "source_video_limits_exceeded", "source_import_cancelled", "video_reference_clip_invalid", "reference_asset_not_found", "reference_asset_not_ready", "video_source_changed":
		status = 422
	case "video_reference_clip_size_limit":
		status = 413
	case "video_reference_clip_required":
		status = 400
	case "video_reference_clip_store_failed":
		status = 500
	default:
		mediaError(c, deps, err)
		return
	}
	c.JSON(status, fail(status, err.Error()))
}
