package apiapp

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// app.ts 的镜像:路由装配 + 控制台静态托管 + 独立域名 CORS + 统一错误体。

const staticMimeDefault = "application/octet-stream"

// Vite 产物使用 `name-<hash>.ext`；只有带内容哈希的文件才能安全 immutable，
// 否则 index.html 引用的固定文件名会被旧缓存长期钉死。
var hashedStaticAsset = regexp.MustCompile(`-[0-9A-Za-z_-]{8,}\.[0-9A-Za-z]+$`)

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

	// 设备授权页的跳转脚本作为独立静态资源提供，页面本身不内联任何 JS，
	// 这样 /app/ 的 CSP 才能保持 script-src 'self'（见 securityHeaders）。
	router.GET("/app/redirect.js", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
		c.Data(http.StatusOK, "text/javascript; charset=utf-8", []byte(deviceRedirectScript))
	})

	// RFC 9116 漏洞上报渠道。Contact 是必填项，未配置时不提供该文件，
	// 而不是编造一个收不到信的地址。
	securityTxt := func(c *gin.Context) {
		if deps.Env.SecurityContact == "" {
			c.Status(http.StatusNotFound)
			return
		}
		c.Header("Cache-Control", "public, max-age=86400")
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(renderSecurityTxt(deps.Env)))
	}
	router.GET("/.well-known/security.txt", securityTxt)
	router.GET("/security.txt", securityTxt)

	registerPublicRoutes(router, deps)
	registerMediaRoutes(router, deps)
	registerTeamAssetRoutes(router, deps)
	registerConsoleRoutes(router, deps)
	registerTeamCreditRoutes(router, deps)
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
	// index.html 在同一个镜像里不会变，启动时读一次即可。
	// 旧实现每次 SPA 兜底都 os.ReadFile 一遍；容器是 read_only + overlayfs，
	// 叠加并发请求后正是静态页出现秒级抖动的原因之一。
	indexHTML, indexErr := os.ReadFile(filepath.Join(root, "index.html"))

	serveIndex := func(c *gin.Context) bool {
		if indexErr != nil {
			c.Status(http.StatusNotFound)
			c.Abort()
			return false
		}
		// 控制台入口 HTML 不缓存：共享终端退出后不能从缓存里翻出上一会话页面。
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
		c.Abort()
		return true
	}

	return func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.Next()
			return
		}
		requestPath := c.Request.URL.Path
		// 入口跳转由应用自己出，而不是交给 nginx 的 return：`return` 会短路
		// proxy_pass，securityHeaders 中间件不会执行，3xx 响应会丢掉全部安全头
		// （线上 / 与 /console 曾实测为 0 个安全头）。这里统一出口后，
		// nginx 只负责它自己生成的响应（444/ACME/错误页），头由 snippet 补齐。
		if requestPath == "/" {
			c.Redirect(http.StatusFound, "/console/")
			c.Abort()
			return
		}
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
			serveIndex(c)
			return
		}
		// 用 ServeContent 流式输出，而不是 os.ReadFile 全量读进内存：
		// 900KB 的 bundle × 并发请求会直接顶到容器 512MB 上限，且旧实现
		// 拿不到 Range / If-Modified-Since，浏览器每次都要整包重下。
		file, openErr := os.Open(candidate)
		if openErr != nil {
			serveIndex(c)
			return
		}
		defer file.Close()
		info, statErr := file.Stat()
		if statErr != nil || info.IsDir() {
			serveIndex(c)
			return
		}
		contentType := staticMime[strings.ToLower(filepath.Ext(candidate))]
		if contentType == "" {
			contentType = staticMimeDefault
		}
		// 带内容哈希的构建产物可以永久缓存；固定文件名只做短缓存，
		// 避免发版后浏览器继续使用旧 index.html 里的入口脚本。
		cacheControl := "public, max-age=3600"
		if hashedStaticAsset.MatchString(filepath.Base(candidate)) {
			cacheControl = "public, max-age=31536000, immutable"
		}
		c.Header("Cache-Control", cacheControl)
		c.Header("Content-Type", contentType)
		http.ServeContent(c.Writer, c.Request, filepath.Base(candidate), info.ModTime(), file)
		c.Abort()
	}
}

// deviceRedirectScript 读取 <body data-redirect> 后短暂停留再跳转。
// 少了内联脚本，CSP 就不需要为设备授权页开 'unsafe-inline'。
const deviceRedirectScript = `(function () {
  var target = document.body && document.body.getAttribute("data-redirect");
  if (!target) return;
  window.setTimeout(function () { window.location.href = target; }, 300);
})();
`

// renderSecurityTxt 生成 RFC 9116 文件。Expires 必须小于一年，这里取 180 天，
// 由部署侧的定时任务或每次发布自然刷新。
func renderSecurityTxt(env *Env) string {
	lines := []string{
		"Contact: " + env.SecurityContact,
		"Expires: " + time.Now().UTC().Add(180*24*time.Hour).Format(time.RFC3339),
		"Preferred-Languages: zh-CN, en",
	}
	if env.PublicConsoleOrigin != "" {
		lines = append(lines, "Canonical: "+env.PublicConsoleOrigin+"/.well-known/security.txt")
	}
	return strings.Join(lines, "\n") + "\n"
}
