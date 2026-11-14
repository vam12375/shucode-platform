package apiapp

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"github.com/vam12375/shucode-platform/server/internal/teamassets"
	"io"
	"net/http"
)

var teamAssetCapabilities = gin.H{"management": true, "thumbnail": true, "contentHash": true}

func registerTeamAssetManagementRoutes(routes *gin.RouterGroup, deps *Deps, service teamassets.Service, actor func(*gin.Context) (*AuthenticatedDevice, *store.TeamMembership, bool)) {
	routes.GET("/:id/thumbnail", func(c *gin.Context) {
		_, membership, ok := actor(c)
		if !ok {
			return
		}
		bytes, err := service.Thumbnail(c.Request.Context(), membership.Team.ID, c.Param("id"))
		if err != nil {
			teamAssetRouteError(c, deps, err)
			return
		}
		c.Header("Cache-Control", "private, no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Data(200, "image/jpeg", bytes)
	})
	routes.GET("/capabilities", func(c *gin.Context) {
		if _, _, ok := actor(c); ok {
			c.JSON(http.StatusOK, okData(teamAssetCapabilities))
		}
	})
	routes.PUT("/:id/favorite", func(c *gin.Context) {
		auth, membership, ok := actor(c)
		if !ok {
			return
		}
		var input struct {
			Favorite *bool `json:"favorite"`
		}
		if err := decodeTeamAssetManagement(c, &input); err != nil || input.Favorite == nil {
			c.JSON(400, fail(400, "team_asset_invalid"))
			return
		}
		if err := deps.Store.SetTeamAssetFavorite(c.Request.Context(), membership.Team.ID, auth.User.ID, c.Param("id"), *input.Favorite); err != nil {
			teamAssetManagementError(c, deps, err)
			return
		}
		c.JSON(200, okData(gin.H{"ok": true}))
	})
	routes.PUT("/:id/tags", func(c *gin.Context) {
		auth, membership, ok := actor(c)
		if !ok {
			return
		}
		var input struct {
			Tags *[]string `json:"tags"`
		}
		if err := decodeTeamAssetManagement(c, &input); err != nil || input.Tags == nil {
			c.JSON(400, fail(400, "team_asset_invalid"))
			return
		}
		if err := deps.Store.SetTeamAssetTags(c.Request.Context(), membership.Team.ID, auth.User.ID, c.Param("id"), *input.Tags); err != nil {
			teamAssetManagementError(c, deps, err)
			return
		}
		c.JSON(200, okData(gin.H{"ok": true}))
	})
}

func teamAssetManagementError(c *gin.Context, deps *Deps, err error) {
	switch err {
	case store.ErrInvalidInput:
		err = teamassets.ErrInvalid
	case store.ErrTeamAssetForbidden:
		err = teamassets.ErrForbidden
	case store.ErrTeamAssetNotFound:
		err = teamassets.ErrNotFound
	}
	teamAssetRouteError(c, deps, err)
}

func serveManagedTeamAssets(c *gin.Context, deps *Deps, service teamassets.Service, auth *AuthenticatedDevice, membership *store.TeamMembership) {
	favorite := c.Query("favoritesOnly")
	if favorite != "" && favorite != "true" && favorite != "false" {
		teamAssetRouteError(c, deps, teamassets.ErrInvalid)
		return
	}
	page, err := service.Query(c.Request.Context(), membership.Team.ID, auth.User.ID, c.Query("cursor"), store.TeamAssetQuery{Q: c.Query("q"), Kind: c.Query("kind"), UploadedBy: c.Query("uploadedBy"), Tag: c.Query("tag"), Sort: c.Query("sort"), FavoritesOnly: favorite == "true", KnownRevision: c.Query("knownRevision")})
	if err != nil {
		teamAssetRouteError(c, deps, err)
		return
	}
	c.JSON(200, okData(gin.H{"team": gin.H{"id": membership.Team.ID, "name": membership.Team.Name, "storage": page.Storage}, "assets": page.Assets, "nextCursor": page.NextCursor, "revision": page.Revision, "notModified": page.NotModified, "facets": page.Facets, "capabilities": teamAssetCapabilities}))
}

func decodeTeamAssetManagement(c *gin.Context, target any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("extra json")
	}
	return nil
}
