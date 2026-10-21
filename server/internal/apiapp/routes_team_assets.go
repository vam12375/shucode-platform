package apiapp

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"github.com/vam12375/shucode-platform/server/internal/teamassets"
)

const teamAssetMultipartOverhead int64 = 1 << 20

func registerTeamAssetRoutes(router *gin.Engine, deps *Deps) {
	assets := router.Group("/api/v1/team-assets")
	assets.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Next()
	})
	service := teamassets.Service{
		Repository: teamassets.Repository{Store: deps.Store},
		Blobs:      media.Blobs{Root: filepath.Join(deps.Media.Blobs.Root, "team-assets")},
		Now:        deps.Now,
	}

	resolveActor := func(c *gin.Context) (*AuthenticatedDevice, *store.TeamMembership, bool) {
		auth, err := authenticateDevice(c.Request.Context(), deps, c)
		if err != nil {
			internalError(c, deps, err)
			return nil, nil, false
		}
		if auth == nil {
			c.JSON(http.StatusUnauthorized, fail(http.StatusUnauthorized, "team_asset_unauthorized"))
			return nil, nil, false
		}
		membership, err := store.ResolveUserTeam(c.Request.Context(), deps.Store, auth.User.ID)
		if errors.Is(err, store.ErrAmbiguousTeam) || (err == nil && membership == nil) {
			c.JSON(http.StatusForbidden, fail(http.StatusForbidden, "team_asset_team_required"))
			return nil, nil, false
		}
		if err != nil {
			internalError(c, deps, err)
			return nil, nil, false
		}
		return auth, membership, true
	}

	assets.GET("", func(c *gin.Context) {
		_, membership, ok := resolveActor(c)
		if !ok {
			return
		}
		page, err := service.List(c.Request.Context(), membership.Team.ID, c.Query("cursor"))
		if err != nil {
			teamAssetRouteError(c, deps, err)
			return
		}
		c.JSON(http.StatusOK, okData(gin.H{
			"team": gin.H{
				"id": membership.Team.ID, "name": membership.Team.Name,
				"storage": page.Storage,
			},
			"assets": page.Assets, "nextCursor": page.NextCursor,
		}))
	})

	assets.POST("", func(c *gin.Context) {
		auth, membership, ok := resolveActor(c)
		if !ok {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, teamassets.MaxAssetBytes+teamAssetMultipartOverhead)
		if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				c.JSON(http.StatusRequestEntityTooLarge, fail(http.StatusRequestEntityTooLarge, "team_asset_upload_size_limit"))
			} else {
				c.JSON(http.StatusBadRequest, fail(http.StatusBadRequest, "team_asset_invalid"))
			}
			return
		}
		if c.Request.MultipartForm != nil {
			defer c.Request.MultipartForm.RemoveAll()
		}
		form := c.Request.MultipartForm
		if form == nil || len(form.Value) != 1 || len(form.Value["id"]) != 1 || len(form.File) != 1 || len(form.File["file"]) != 1 {
			c.JSON(http.StatusBadRequest, fail(http.StatusBadRequest, "team_asset_invalid"))
			return
		}
		id := form.Value["id"][0]
		parsedID, parseErr := uuid.Parse(id)
		if parseErr != nil || len(id) != 36 || parsedID.String() != id {
			c.JSON(http.StatusBadRequest, fail(http.StatusBadRequest, "team_asset_invalid"))
			return
		}
		header := form.File["file"][0]
		file, err := header.Open()
		if err != nil {
			internalError(c, deps, err)
			return
		}
		asset, uploadErr := service.Upload(c.Request.Context(), membership.Team.ID, auth.User.ID, auth.User.DisplayName, id, header.Filename, file)
		closeErr := file.Close()
		if uploadErr != nil {
			teamAssetRouteError(c, deps, uploadErr)
			return
		}
		if closeErr != nil {
			internalError(c, deps, closeErr)
			return
		}
		c.JSON(http.StatusOK, okData(asset))
	})

	assets.GET("/:id", func(c *gin.Context) {
		_, membership, ok := resolveActor(c)
		if !ok {
			return
		}
		asset, err := service.Get(c.Request.Context(), membership.Team.ID, c.Param("id"))
		if err != nil {
			teamAssetRouteError(c, deps, err)
			return
		}
		if asset == nil {
			c.JSON(http.StatusNotFound, fail(http.StatusNotFound, "team_asset_not_found"))
			return
		}
		c.JSON(http.StatusOK, okData(asset))
	})

	assets.GET("/:id/content", func(c *gin.Context) {
		_, membership, ok := resolveActor(c)
		if !ok {
			return
		}
		asset, file, err := service.Open(c.Request.Context(), membership.Team.ID, c.Param("id"))
		if err != nil {
			teamAssetRouteError(c, deps, err)
			return
		}
		defer file.Close()
		contentDisposition := mime.FormatMediaType("attachment", map[string]string{"filename": asset.Name})
		if contentDisposition == "" {
			teamAssetRouteError(c, deps, fmt.Errorf("invalid team asset filename for download"))
			return
		}
		c.Header("Content-Type", asset.Mime)
		c.Header("Content-Disposition", contentDisposition)
		c.Header("Content-Length", fmt.Sprintf("%d", asset.Size))
		c.Header("Cache-Control", "private, no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		c.DataFromReader(http.StatusOK, asset.Size, asset.Mime, file, nil)
	})
}

func teamAssetRouteError(c *gin.Context, deps *Deps, err error) {
	switch {
	case errors.Is(err, teamassets.ErrInvalid):
		c.JSON(http.StatusBadRequest, fail(http.StatusBadRequest, "team_asset_invalid"))
	case errors.Is(err, teamassets.ErrSizeLimit):
		c.JSON(http.StatusRequestEntityTooLarge, fail(http.StatusRequestEntityTooLarge, "team_asset_upload_size_limit"))
	case errors.Is(err, teamassets.ErrStorageLimit):
		c.JSON(http.StatusRequestEntityTooLarge, fail(http.StatusRequestEntityTooLarge, "team_asset_storage_limit"))
	case errors.Is(err, teamassets.ErrConflict):
		c.JSON(http.StatusConflict, fail(http.StatusConflict, "team_asset_id_conflict"))
	case errors.Is(err, teamassets.ErrBusy):
		c.JSON(http.StatusConflict, fail(http.StatusConflict, "team_asset_write_busy"))
	case errors.Is(err, teamassets.ErrNotFound):
		c.JSON(http.StatusNotFound, fail(http.StatusNotFound, "team_asset_not_found"))
	default:
		internalError(c, deps, err)
	}
}
