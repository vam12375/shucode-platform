package apiapp

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// app.ts 的镜像:路由装配 + 控制台静态托管 + 独立域名 CORS + 统一错误体。

func New(deps *Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	// 与 TS 路由逐位兼容:Gin 默认会把未知路径的尾斜杠 301 到去斜杠版本,TS 直接 404。
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false
	router.Use(recoveryMiddleware(deps), securityHeaders(deps), consoleHostGuard(deps), setupHostGuard(deps), csrfOriginGuard(deps), paymentConsoleCookieOnly(), consoleAccessGuard(deps))

	// 独立部署 Console 时允许携带凭据的跨源访问(仅 /console/api/*)。
	if deps.Env.PublicConsoleOrigin != "" {
		router.Use(func(c *gin.Context) {
			if !strings.HasPrefix(c.Request.URL.Path, "/console/api/") {
				c.Next()
				return
			}
			c.Header("Access-Control-Allow-Origin", deps.Env.PublicConsoleOrigin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Expose-Headers", "ETag")
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
		router.Use(consoleStaticMiddleware(deps))
	}

	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	// 设备登录网页（Signal 视觉）的样式、脚本与媒体作为同源静态资源提供，
	// 页面本身不内联任何 JS，/app/ 的 CSP 才能保持 script-src 'self'（见 securityHeaders）。
	registerDeviceAuthAssetRoutes(router)

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
	registerTemplateLibraryRoutes(router, deps)
	registerTeamAssetRoutes(router, deps)
	registerConsoleRoutes(router, deps)
	registerTeamCreditRoutes(router, deps)
	registerConsoleOpsRoutes(router, deps)
	registerDeepSeekWebToolsConfigRoutes(router, deps)
	registerConsoleOpsExtRoutes(router, deps)
	registerConsoleMediaFailureRoutes(router, deps)
	registerTenantRoutes(router, deps)
	registerQuotaRequestRoutes(router, deps)
	registerRootMFARoutes(router, deps)
	registerPaymentConfigRoutes(router, deps)
	registerPaymentRoutes(router, deps)
	registerPaymentPackageRoutes(router, deps)
	registerClientPaymentRoutes(router, deps)
	registerPaymentPublicRoutes(router, deps)
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
