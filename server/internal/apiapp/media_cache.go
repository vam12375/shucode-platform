package apiapp

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// ServeContent 的 Range/文件错误不能沿用此前设置的成功缓存头。
type mediaCacheWriter struct{ gin.ResponseWriter }

func (w mediaCacheWriter) WriteHeader(code int) {
	if code >= 400 {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Del("ETag")
		w.Header().Del("Expires")
	}
	w.ResponseWriter.WriteHeader(code)
}

func prepareSignedMediaResponse(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Writer = mediaCacheWriter{c.Writer}
}

func signedMediaQuery(c *gin.Context, requireVersion bool) (url.Values, bool) {
	params, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return nil, false
	}
	for _, key := range []string{"expires", "signature"} {
		if len(params[key]) != 1 || params.Get(key) == "" {
			return nil, false
		}
	}
	if requireVersion || params.Has("v") {
		if len(params["v"]) != 1 || params.Get("v") == "" {
			return nil, false
		}
	}
	return params, true
}

func setSignedMediaHeaders(c *gin.Context, mime, etag, expires string) bool {
	expiration, err := strconv.ParseInt(expires, 10, 64)
	remaining := expiration - time.Now().Unix()
	if err != nil || remaining <= 0 {
		c.Status(http.StatusForbidden)
		return false
	}
	ttl := min(int64(300), remaining)
	c.Header("Cache-Control", fmt.Sprintf("public, max-age=%d, s-maxage=%d, must-revalidate", ttl, ttl))
	c.Header("ETag", strconv.Quote(etag))
	c.Header("Cross-Origin-Resource-Policy", "cross-origin")
	c.Header("Content-Disposition", "inline")
	c.Header("Content-Type", mime)
	c.Header("X-Content-Type-Options", "nosniff")
	return true
}

func serveSignedMediaFile(c *gin.Context, path, mime, etag, expires string) {
	// 先确认真实文件，再发布可缓存响应；缺失原件不能因元数据或派生缓存存在而交付。
	file, err := os.Open(path)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	defer file.Close()
	serveSignedMediaContent(c, file, mime, etag, expires)
}

func serveSignedMediaContent(c *gin.Context, file *os.File, mime, etag, expires string) {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		c.Status(http.StatusNotFound)
		return
	}
	if !setSignedMediaHeaders(c, mime, etag, expires) {
		return
	}
	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
}
