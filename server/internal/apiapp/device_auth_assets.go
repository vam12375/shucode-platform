package apiapp

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// 设备登录网页（Signal 视觉）的静态资源，规范见 specs/device-login-pages.md。
//
// 页面不内联任何脚本，样式与脚本都作为同源外链提供，CSP 才能保持
// script-src 'self'。
//
// 背景信号源：目录里有 signal.mp4 时，WebGL 采样视频；没有视频，或视频加载失败时，
// 使用程序噪声。视频是可选文件，增删都不用改代码。
//
// 视频经媒体 CDN（PUBLIC_MEDIA_CDN_ORIGIN，空值为同源）分发：地址带内容哈希 ?v=，
// 源站下发一年 immutable + 内容 ETag，CDN 按「遵循源站」缓存一年；跨源时以
// CORS（Access-Control-Allow-Origin: *，公开内容、不带凭据）供 WebGL 读取帧。
//
//go:embed assets/device-auth
var deviceAuthFiles embed.FS

type deviceAuthAsset struct {
	contentType string
	data        []byte
	version     string
}

const (
	deviceAuthSignalVideo = "signal.mp4"
	deviceAuthAssetPath   = "/app/device-auth/"
	// 一年：内容哈希地址，内容变化即换 URL。
	deviceAuthImmutableCache = "public, max-age=31536000, s-maxage=31536000, immutable"
)

var deviceAuthAssets = mustLoadDeviceAuthAssets()

// deviceAuthAssetVersion 由 css+js 内容哈希得到，拼在页面引用的查询串上；
// 内容变化即换 URL，因此资源可以安全地使用 immutable 长缓存。
var deviceAuthAssetVersion = func() string {
	sum := sha256.New()
	sum.Write(deviceAuthAssets["auth.css"].data)
	sum.Write(deviceAuthAssets["auth.js"].data)
	return hex.EncodeToString(sum.Sum(nil))[:12]
}()

func mustLoadDeviceAuthAssets() map[string]deviceAuthAsset {
	files := []struct {
		name        string
		contentType string
		required    bool
	}{
		{"auth.css", "text/css; charset=utf-8", true},
		{"auth.js", "text/javascript; charset=utf-8", true},
		{deviceAuthSignalVideo, "video/mp4", false},
	}
	assets := make(map[string]deviceAuthAsset, len(files))
	for _, file := range files {
		data, err := deviceAuthFiles.ReadFile("assets/device-auth/" + file.name)
		if err != nil {
			if file.required {
				panic("设备登录页资源缺失: " + file.name)
			}
			continue
		}
		sum := sha256.Sum256(data)
		assets[file.name] = deviceAuthAsset{contentType: file.contentType, data: data, version: hex.EncodeToString(sum[:])[:12]}
	}
	return assets
}

// deviceAuthMediaOrigin 返回媒体 CDN 的 scheme://host；未配置或非法时为空（同源）。
func deviceAuthMediaOrigin(env *Env) string {
	if env == nil {
		return ""
	}
	raw := strings.TrimSpace(env.PublicMediaCDNOrigin)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

// deviceAuthPage 是设备登录页渲染所需的部署相关输入（由路由按当前 Env 构造）。
type deviceAuthPage struct {
	// VideoURL 为空表示没有内置视频，页面只用程序噪声。
	VideoURL string
}

func deviceAuthPageFor(deps *Deps) deviceAuthPage {
	asset, ok := deviceAuthAssets[deviceAuthSignalVideo]
	if !ok {
		return deviceAuthPage{}
	}
	var env *Env
	if deps != nil {
		env = deps.Env
	}
	return deviceAuthPage{VideoURL: deviceAuthMediaOrigin(env) + deviceAuthAssetPath + deviceAuthSignalVideo + "?v=" + asset.version}
}

// deviceAuthAssetStartedAt 作为 ServeContent 的 Last-Modified，进程内稳定即可。
var deviceAuthAssetStartedAt = time.Now().UTC().Truncate(time.Second)

func registerDeviceAuthAssetRoutes(router *gin.Engine) {
	handler := func(c *gin.Context) {
		// 默认 no-store：未知资源、版本不符、Range 越界等错误都不能被 CDN 缓存。
		c.Header("Cache-Control", "no-store")
		c.Writer = mediaCacheWriter{c.Writer}
		name := c.Param("name")
		asset, ok := deviceAuthAssets[name]
		if !ok {
			c.Status(http.StatusNotFound)
			return
		}
		version := c.Query("v")
		if name == deviceAuthSignalVideo {
			// 视频经 CDN 缓存一年：只接受当前内容哈希，避免旧/伪造版本号把错误内容写进 CDN 缓存键。
			if version != asset.version {
				c.Status(http.StatusNotFound)
				return
			}
			c.Header("Cache-Control", deviceAuthImmutableCache)
			c.Header("ETag", strconv.Quote(asset.version))
			c.Header("Content-Type", asset.contentType)
			c.Header("X-Content-Type-Options", "nosniff")
			// 页面与 CDN 不同源时，WebGL 需要 CORS 才能读取视频帧；公开内容、不带凭据。
			c.Header("Access-Control-Allow-Origin", "*")
			c.Header("Cross-Origin-Resource-Policy", "cross-origin")
			c.Header("Timing-Allow-Origin", "*")
			// ServeContent：支持 Range（Safari 播放 mp4 依赖 206）、HEAD 与 If-None-Match。
			http.ServeContent(c.Writer, c.Request, name, deviceAuthAssetStartedAt, bytes.NewReader(asset.data))
			return
		}
		// css/js 由页面以 ?v=<css+js 哈希> 引用；版本一致才给一年缓存，其余不缓存。
		if version == deviceAuthAssetVersion {
			c.Header("Cache-Control", deviceAuthImmutableCache)
			c.Header("ETag", strconv.Quote(deviceAuthAssetVersion))
		} else {
			c.Header("Cache-Control", "no-cache")
		}
		c.Header("Content-Type", asset.contentType)
		c.Header("X-Content-Type-Options", "nosniff")
		c.Data(http.StatusOK, asset.contentType, asset.data)
	}
	router.GET(deviceAuthAssetPath+":name", handler)
	router.HEAD(deviceAuthAssetPath+":name", handler)
}
