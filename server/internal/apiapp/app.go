package apiapp

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// app.ts 的镜像:路由装配 + 控制台静态托管 + 独立域名 CORS + 统一错误体。

const staticMimeDefault = "application/octet-stream"

var staticMime = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".ico":   "image/x-icon",
	".woff2": "font/woff2",
	".map":   "application/json; charset=utf-8",
}

func New(deps *Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	// 与 TS 路由逐位兼容:Gin 默认会把未知路径的尾斜杠 301 到去斜杠版本,TS 直接 404。
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false
	router.Use(recoveryMiddleware(deps), securityHeaders(deps), consoleHostGuard(deps), setupHostGuard(deps), csrfOriginGuard(deps), consoleAccessGuard(deps))

	// 独立部署 Console 时允许携带凭据的跨源访问(仅 /console/api/*)。
	if deps.Env.PublicConsoleOrigin != "" {
		router.Use(func(c *gin.Context) {
			if !strings.HasPrefix(c.Request.URL.Path, "/console/api/") {
				c.Next()
				return
			}
			c.Header("Access-Control-Allow-Origin", deps.Env.PublicConsoleOrigin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Vary", "Origin")
			if c.Request.Method == http.MethodOptions {
				c.Header("Access-Control-Allow-Methods", "GET,HEAD,PUT,POST,DELETE,PATCH")
				if requestHeaders := c.GetHeader("access-control-request-headers"); requestHeaders != "" {
					c.Header("Access-Control-Allow-Headers", requestHeaders)
				}
				c.Status(http.StatusNoContent)
				c.Abort()
				return
			}
			c.Next()
		})
	}

	// 控制台静态托管(/console/*,必须绕过 /console/api/*)。
	if deps.Env.ConsoleDistDir != "" {
		router.Use(consoleStaticMiddleware(deps.Env.ConsoleDistDir))
	}

	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true, "memoryDb": deps.Env.MemoryDB})
	})

	registerPublicRoutes(router, deps)
	registerMediaRoutes(router, deps)
	registerConsoleRoutes(router, deps)
	registerConsoleOpsRoutes(router, deps)
	registerConsoleOpsExtRoutes(router, deps)
	registerTenantRoutes(router, deps)
	registerQuotaRequestRoutes(router, deps)
	registerRootMFARoutes(router, deps)
	return router
}

// recoveryMiddleware:替代 gin.CustomRecovery。后者在 broken-pipe 分支会 dump
// 整个请求（仅屏蔽 Authorization），可能把 Cookie / x-api-key 写进日志。
func recoveryMiddleware(deps *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				deps.Logger.Error("请求处理失败", "path", c.Request.URL.Path, "error", recovered)
				if !c.Writer.Written() {
					c.JSON(500, fail(500, "服务器内部错误"))
				}
				c.Abort()
			}
		}()
		c.Next()
	}
}

// consoleStaticMiddleware:GET/HEAD 的 /console/* 直接由静态文件响应;
// 非静态资源路径回落 index.html(SPA fallback);`..` 一律 404。
func consoleStaticMiddleware(distDir string) gin.HandlerFunc {
	root, err := filepath.Abs(distDir)
	if err != nil {
		root = distDir
	}
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.Next()
			return
		}
		requestPath := c.Request.URL.Path
		if requestPath == "/console" {
			c.Redirect(http.StatusFound, "/console/")
			c.Abort()
			return
		}
		if !strings.HasPrefix(requestPath, "/console/") || strings.HasPrefix(requestPath, "/console/api/") {
			c.Next()
			return
		}
		rawRelative := strings.TrimPrefix(requestPath, "/console/")
		relative := strings.TrimLeft(path.Clean("/"+rawRelative), "/.")
		if strings.Contains(rawRelative, "..") {
			c.Status(http.StatusNotFound)
			c.Abort()
			return
		}
		candidate := filepath.Join(root, relative)
		if relative == "" {
			candidate = filepath.Join(root, "index.html")
		}
		content, readErr := os.ReadFile(candidate)
		if readErr != nil {
			index, indexErr := os.ReadFile(filepath.Join(root, "index.html"))
			if indexErr != nil {
				c.Status(http.StatusNotFound)
				c.Abort()
				return
			}
			c.Data(200, "text/html; charset=utf-8", index)
			c.Abort()
			return
		}
		contentType := staticMime[strings.ToLower(filepath.Ext(candidate))]
		if contentType == "" {
			contentType = staticMimeDefault
		}
		cacheControl := "public, max-age=31536000, immutable"
		if strings.HasSuffix(candidate, "index.html") {
			cacheControl = "no-cache"
		}
		c.Header("Cache-Control", cacheControl)
		c.Data(200, contentType, content)
		c.Abort()
	}
}
