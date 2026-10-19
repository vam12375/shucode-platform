package apiapp

import (
	"net"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// 安全响应头与浏览器侧请求边界。
// CSP 对控制台静态站点使用严格策略；设备登录页存在一段内联跳转脚本，
// 仅对这些路径放宽 script-src（页面内容全部转义，不含用户可控 HTML）。

func securityHeaders(deps *Deps) gin.HandlerFunc {
	httpsOrigins := map[string]string{}
	for _, origin := range []string{deps.Env.PublicAPIOrigin, deps.Env.PublicAppOrigin, deps.Env.PublicConsoleOrigin} {
		parsed, err := url.Parse(strings.TrimSpace(origin))
		if err == nil && parsed.Scheme == "https" && parsed.Host != "" {
			httpsOrigins[strings.ToLower(parsed.Host)] = "https://" + parsed.Host
		}
	}
	return func(c *gin.Context) {
		// 已配置 https 公开地址时，Host 必须命中已配置域名（避免开放重定向），
		// 且请求不得走明文：反代明确报告 http，或直连且没有 TLS 都视为明文。
		if c.Request.URL.Path != "/healthz" {
			if target, ok := httpsOrigins[strings.ToLower(c.Request.Host)]; ok {
				proto := strings.ToLower(c.GetHeader("x-forwarded-proto"))
				insecure := proto == "http" || (proto == "" && c.Request.TLS == nil)
				if insecure {
					if c.Request.Method == "GET" || c.Request.Method == "HEAD" {
						c.Redirect(308, target+c.Request.URL.RequestURI())
					} else {
						c.AbortWithStatusJSON(403, fail(403, "管理/公开接口必须使用 HTTPS"))
					}
					return
				}
			}
		}
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		// Turnstile 脚本与挑战 iframe 都在 challenges.cloudflare.com；只在启用时放宽。
		turnstileHosts := ""
		if deps.Env.TurnstileSiteKey != "" {
			turnstileHosts = " https://challenges.cloudflare.com"
		}
		scriptSrc := "'self'" + turnstileHosts
		if strings.HasPrefix(c.Request.URL.Path, "/app/") {
			scriptSrc += " 'unsafe-inline'"
		}
		csp := "default-src 'self'; script-src " + scriptSrc + "; style-src 'self' 'unsafe-inline'; " +
			"img-src 'self' data:; font-src 'self'; connect-src 'self'" + turnstileHosts + "; " +
			"frame-src 'self'" + turnstileHosts + "; " +
			"frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
		c.Header("Content-Security-Policy", csp)
		if len(httpsOrigins) > 0 {
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		if strings.HasPrefix(c.Request.URL.Path, "/console/api/auth") ||
			strings.HasPrefix(c.Request.URL.Path, "/console/api/setup") ||
			strings.HasPrefix(c.Request.URL.Path, "/app/platform/") ||
			strings.HasPrefix(c.Request.URL.Path, "/api/v1/platform/auth/") {
			c.Header("Cache-Control", "no-store")
		}
		c.Next()
	}
}

// consoleHostGuard:配置 CONSOLE_ALLOWED_HOSTS 后，管理面只在指定 Host 暴露。
// 配合边缘 Access 按域名保护管理入口，公开 API 域名无法从同一进程绕进 /console。
func consoleHostGuard(deps *Deps) gin.HandlerFunc {
	allowed := map[string]struct{}{}
	for _, host := range deps.Env.ConsoleAllowedHosts {
		allowed[strings.ToLower(strings.TrimSuffix(host, "."))] = struct{}{}
	}
	return func(c *gin.Context) {
		if len(allowed) == 0 || !strings.HasPrefix(c.Request.URL.Path, "/console") {
			c.Next()
			return
		}
		host := strings.ToLower(strings.TrimSuffix(netutilHost(c.Request.Host), "."))
		if _, ok := allowed[host]; !ok {
			c.Status(404)
			c.Abort()
			return
		}
		c.Next()
	}
}

// setupHostGuard:首次初始化接口（/console/api/setup/*）只在专用初始化 Host 暴露。
// 未配置时保持与 /console 相同的可见性；生产建议指向仅经 Access/VPN 可达的域名。
func setupHostGuard(deps *Deps) gin.HandlerFunc {
	allowed := map[string]struct{}{}
	for _, host := range deps.Env.SetupAllowedHosts {
		allowed[strings.ToLower(strings.TrimSuffix(host, "."))] = struct{}{}
	}
	return func(c *gin.Context) {
		if len(allowed) == 0 || !strings.HasPrefix(c.Request.URL.Path, "/console/api/setup/") {
			c.Next()
			return
		}
		host := strings.ToLower(strings.TrimSuffix(netutilHost(c.Request.Host), "."))
		if _, ok := allowed[host]; !ok {
			c.Status(404)
			c.Abort()
			return
		}
		c.Next()
	}
}

func netutilHost(hostPort string) string {
	if host, _, err := net.SplitHostPort(hostPort); err == nil {
		return host
	}
	return hostPort
}

// csrfOriginGuard:对带 cookie 的写操作校验 Origin / Sec-Fetch-Site。
// 缺失 Origin 的非浏览器请求（curl、服务间调用、测试）不受影响。
func csrfOriginGuard(deps *Deps) gin.HandlerFunc {
	allowed := map[string]struct{}{}
	for _, origin := range []string{deps.Env.PublicAPIOrigin, deps.Env.PublicAppOrigin, deps.Env.PublicConsoleOrigin} {
		if origin = strings.TrimSpace(origin); origin != "" {
			allowed[origin] = struct{}{}
		}
	}
	return func(c *gin.Context) {
		switch c.Request.Method {
		case "GET", "HEAD", "OPTIONS":
			c.Next()
			return
		}
		path := c.Request.URL.Path
		if !strings.HasPrefix(path, "/console/api/") && !strings.HasPrefix(path, "/app/platform/") {
			c.Next()
			return
		}
		if c.GetHeader("Sec-Fetch-Site") == "cross-site" || c.GetHeader("Sec-Fetch-Site") == "same-site" {
			c.AbortWithStatusJSON(403, fail(403, "请求来源不合法"))
			return
		}
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}
		if _, ok := allowed[origin]; ok {
			c.Next()
			return
		}
		// 同源请求（含 Vite dev 代理）由浏览器标记 same-origin；不再要求 Origin 与
		// 公开配置完全一致，避免本地开发被误伤。跨站请求在上面的分支已拒绝。
		if c.GetHeader("Sec-Fetch-Site") == "same-origin" {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(403, fail(403, "请求来源不合法"))
	}
}
