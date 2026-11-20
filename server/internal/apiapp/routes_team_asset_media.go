package apiapp

import (
	"bytes"
	"errors"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/contracts"
	"github.com/vam12375/shucode-platform/server/internal/teamassets"
)

func teamAssetRaster(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/webp", "image/gif", "image/avif", "image/bmp":
		return true
	}
	return false
}
func teamAssetThumbnailSupported(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/gif":
		return true
	}
	return false
}
func teamAssetMediaVersion(asset contracts.CloudAsset) string {
	if asset.ContentHash != "" {
		return asset.ContentHash
	}
	return asset.ID
}

// 列表只投影签名地址，不解码原图；平台仍唯一持有团队、版本和权限事实。
func withTeamAssetMediaURLs(deps *Deps, team string, asset contracts.CloudAsset) contracts.CloudAsset {
	asset.PreviewURL, asset.ThumbnailURL = "", ""
	if asset.Kind != "image" || !teamAssetRaster(asset.Mime) {
		return asset
	}
	base := "/api/v1/team-asset-media/" + team + "/" + asset.ID + "/"
	version := teamAssetMediaVersion(asset)
	asset.PreviewURL = deps.Media.Blobs.DisplayPathURL(base+"original", version)
	if teamAssetThumbnailSupported(asset.Mime) {
		asset.ThumbnailURL = deps.Media.Blobs.DisplayPathURL(base+"thumbnail", version)
	}
	return asset
}

func teamAssetsWithMediaURLs(deps *Deps, team string, assets []contracts.CloudAsset) []contracts.CloudAsset {
	result := make([]contracts.CloudAsset, len(assets))
	for i, asset := range assets {
		result[i] = withTeamAssetMediaURLs(deps, team, asset)
	}
	return result
}

func registerTeamAssetMediaRoutes(router *gin.Engine, deps *Deps, service teamassets.Service) {
	handler := func(c *gin.Context) {
		prepareSignedMediaResponse(c)
		team, id, variant := c.Param("team"), c.Param("id"), c.Param("variant")
		params, valid := signedMediaQuery(c, true)
		teamID, teamErr := uuid.Parse(team)
		assetID, idErr := uuid.Parse(id)
		if !valid || teamErr != nil || idErr != nil || teamID.String() != team || assetID.String() != id ||
			(variant != "original" && variant != "thumbnail") ||
			!deps.Media.Blobs.VerifyDisplayPath(c.Request.URL.Path, params.Get("v"), params.Get("expires"), params.Get("signature")) {
			c.Status(http.StatusForbidden)
			return
		}
		asset, err := service.Get(c.Request.Context(), team, id)
		if err != nil {
			teamAssetRouteError(c, deps, err)
			return
		}
		if asset == nil {
			c.Status(http.StatusNotFound)
			return
		}
		if asset.Kind != "image" || !teamAssetRaster(asset.Mime) {
			c.Status(http.StatusUnsupportedMediaType)
			return
		}
		if params.Get("v") != teamAssetMediaVersion(*asset) {
			c.Status(http.StatusForbidden)
			return
		}
		if variant == "thumbnail" {
			raw, err := service.Thumbnail(c.Request.Context(), team, id)
			if err != nil {
				teamAssetRouteError(c, deps, err)
				return
			}
			if !setSignedMediaHeaders(c, "image/jpeg", "jpeg-256-v1-"+team+"-"+id+"-"+params.Get("v"), params.Get("expires")) {
				return
			}
			http.ServeContent(c.Writer, c.Request, id+".jpg", asset.CreatedAt, bytes.NewReader(raw))
			return
		}
		_, file, err := service.Open(c.Request.Context(), team, id)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				c.Status(http.StatusNotFound)
				return
			}
			teamAssetRouteError(c, deps, err)
			return
		}
		defer file.Close()
		serveSignedMediaContent(c, file, asset.Mime, "team-original-"+team+"-"+id+"-"+params.Get("v"), params.Get("expires"))
	}
	router.GET("/api/v1/team-asset-media/:team/:id/:variant", handler)
	router.HEAD("/api/v1/team-asset-media/:team/:id/:variant", handler)
}
