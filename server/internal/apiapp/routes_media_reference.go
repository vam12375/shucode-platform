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
		// kind 缺省为 video，保持既有客户端行为不变；音频上限更小，按 kind 选择读体上限。
		kind := c.Request.FormValue("kind")
		if kind == "" {
			kind = "video"
		}
		bodyLimit := int64(media.MaxVideo)
		if kind == "audio" {
			bodyLimit = media.MaxAudio
		} else if kind != "video" {
			c.JSON(http.StatusBadRequest, fail(http.StatusBadRequest, "reference_clip_kind_invalid"))
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, bodyLimit+(1<<20))
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
		var asset media.Asset
		if kind == "audio" {
			asset, err = deps.Media.UploadReferenceAudio(c.Request.Context(), m.Team.ID, c.Request.FormValue("sourceAssetId"), c.Request.FormValue("sourceChecksum"), start, end, file)
		} else {
			asset, err = deps.Media.UploadReferenceClip(c.Request.Context(), m.Team.ID, c.Request.FormValue("sourceAssetId"), c.Request.FormValue("sourceChecksum"), start, end, file)
		}
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
	case "source_parser_unconfigured", "source_parser_auth_failed", "source_parser_credits_exhausted", "source_parser_configuration_unavailable", "reference_pricing_unavailable", "source_media_dependencies_missing":
		status = 503
	case "source_parser_rate_limited":
		status = 429
	case "insufficient_credits", "budget_exhausted":
		status = 402
	case "team_inactive":
		status = 403
	case "source_parser_unavailable", "source_parse_failed", "source_parser_invalid_response", "source_video_unavailable", "source_stream_merge_failed":
		status = 502
	case "source_import_in_progress", "source_import_unavailable", "source_import_failed", "source_import_settlement_pending":
		status = 409
	case "source_video_too_large":
		status = 413
	case "invalid_source_url", "invalid_source_video", "source_video_probe_failed", "source_video_limits_exceeded", "source_import_cancelled", "source_stream_duration_mismatch", "video_reference_clip_invalid", "video_reference_audio_invalid", "video_reference_audio_unsupported", "reference_asset_not_found", "reference_asset_not_ready", "video_source_changed":
		status = 422
	case "video_reference_clip_size_limit", "video_reference_audio_size_limit":
		status = 413
	case "video_reference_clip_required":
		status = 400
	case "video_reference_clip_store_failed", "video_reference_audio_store_failed", "source_import_save_failed":
		status = 500
	default:
		mediaError(c, deps, err)
		return
	}
	c.JSON(status, fail(status, err.Error()))
}
