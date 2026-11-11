package apiapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func mediaAssetActor(auth *AuthenticatedDevice, membership *store.TeamMembership) media.AssetActor {
	return media.AssetActor{UserID: auth.User.ID, Name: auth.User.DisplayName, Admin: store.IsTeamAdmin(membership.Role)}
}
func decodeAssetCommand(c *gin.Context, target any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	raw = bytes.TrimSpace(raw)
	if err != nil || len(raw) == 0 || raw[0] != '{' {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && decoder.Decode(new(any)) == io.EOF
}
func mediaLibraryError(c *gin.Context, deps *Deps, err error) {
	code := 422
	switch {
	case errors.Is(err, media.ErrAssetForbidden):
		code = 403
	case errors.Is(err, media.ErrAssetNotFound):
		code = 404
	case errors.Is(err, media.ErrAssetNameInvalid), errors.Is(err, media.ErrInvalid):
		code = 400
	case errors.Is(err, media.ErrAssetConflict), errors.Is(err, media.ErrAssetInUse):
		code = 409
	default:
		internalError(c, deps, err)
		return
	}
	c.JSON(code, fail(code, err.Error()))
}
func registerMediaLibraryRoutes(router *gin.Engine, deps *Deps) {
	router.POST("/api/v1/media/assets/:id/rename", func(c *gin.Context) {
		auth, membership, ok := mediaIdentity(c, deps)
		if !ok {
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		if !decodeAssetCommand(c, &body) {
			mediaLibraryError(c, deps, media.ErrAssetNameInvalid)
			return
		}
		asset, err := deps.Media.Repo.RenameAsset(c.Request.Context(), membership.Team.ID, c.Param("id"), body.Name, mediaAssetActor(auth, membership))
		if err != nil {
			mediaLibraryError(c, deps, err)
			return
		}
		asset.URL = deps.Media.Blobs.URL(membership.Team.ID, asset.ID)
		asset = deps.Media.Blobs.WithPreviewURL(membership.Team.ID, asset)
		c.JSON(200, okData(asset))
	})
	router.POST("/api/v1/media/assets/:id/deletion-requests", func(c *gin.Context) {
		auth, membership, ok := mediaIdentity(c, deps)
		if !ok {
			return
		}
		var body struct{}
		if !decodeAssetCommand(c, &body) {
			mediaLibraryError(c, deps, media.ErrInvalid)
			return
		}
		request, err := deps.Media.Repo.RequestAssetDeletion(c.Request.Context(), membership.Team.ID, c.Param("id"), mediaAssetActor(auth, membership))
		if err != nil {
			mediaLibraryError(c, deps, err)
			return
		}
		c.JSON(200, okData(request))
	})
	router.POST("/api/v1/media/asset-deletion-requests/:id/review", func(c *gin.Context) {
		auth, membership, ok := mediaIdentity(c, deps)
		if !ok {
			return
		}
		var body struct {
			Decision string `json:"decision"`
		}
		if !decodeAssetCommand(c, &body) {
			mediaLibraryError(c, deps, media.ErrInvalid)
			return
		}
		request, err := deps.Media.Repo.ReviewAssetDeletion(c.Request.Context(), membership.Team.ID, c.Param("id"), body.Decision, mediaAssetActor(auth, membership))
		if err != nil {
			mediaLibraryError(c, deps, err)
			return
		}
		c.JSON(200, okData(request))
	})
}
