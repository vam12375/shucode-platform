package apiapp

import (
	"net"
	"net/url"
	"strconv"
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
	// HSTS 值在启动时算死：includeSubDomains / preload 必须与 nginx 的
	// shucode-hsts.conf 完全一致，否则 nginx 与应用会下发两个不同的 HSTS 头。
	hstsValue := ""
	if deps.Env.HSTSMaxAgeSeconds > 0 && len(httpsOrigins) > 0 {
		hstsValue = "max-age=" + strconv.Itoa(deps.Env.HSTSMaxAgeSeconds)
		if deps.Env.HSTSIncludeSubdomains {
			hstsValue += "; includeSubDomains"
		}
		if deps.Env.HSTSPreload {
			hstsValue += "; preload"
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
		// 设备授权页不再内联任何 JS/事件处理器（改用 /app/redirect.js + data 属性），
		// 因此 script-src 不需要 'unsafe-inline'——内联脚本里只要有用户可控字符串
		// 就是存储型 XSS，这里直接把它从策略里拿掉。
		scriptSrc := "'self'" + turnstileHosts
		csp := "default-src 'self'; script-src " + scriptSrc + "; style-src 'self' 'unsafe-inline'; " +
			"img-src 'self' data:; font-src 'self'; connect-src 'self'" + turnstileHosts + "; " +
			"frame-src 'self'" + turnstileHosts + "; " +
			"object-src 'none'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
		// 有 https 公开地址才要求升级混合内容；纯 http 本地开发加了会打断 vite。
		if len(httpsOrigins) > 0 {
			csp += "; upgrade-insecure-requests"
		}
		c.Header("Content-Security-Policy", csp)
		if hstsValue != "" {
			c.Header("Strict-Transport-Security", hstsValue)
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

// csrfOriginGuard:带会话 Cookie 的写操作必须证明来源。
//
// 旧实现「没有 Origin 就放行」，把安全完全押在 SameSite=Lax 上。但 SameSite 是
// **site** 作用域：api / gw / console 同属一个注册域，任一子域被拿下或被错误接管，
// 攻击者就能构造出「同站」请求，而服务端构造的请求连 Sec-Fetch-Site 都没有。
// 收紧规则：
//   - 用 Bearer / x-api-key 认证的调用方自带凭据，不存在环境凭据，CSRF 不适用；
//   - 没有会话 Cookie 的请求没有可被借用的凭据（登录/初始化靠口令校验）；
//   - 有会话 Cookie 的写操作：Sec-Fetch-Site 必须为 same-origin，或 Origin 命中白名单；
//     其余（含两个头都缺失）一律拒绝。
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
		if readBearerToken(c) != "" {
			c.Next()
			return
		}
		// 浏览器自动附加的 Sec-Fetch-Site 最可信，优先判它：
		//   cross-site / same-site —— 无论有没有 Cookie 一律拒绝（same-site 也要拒，
		//   子域互打正是这一层要挡的场景）；
		//   same-origin —— 浏览器已确认同源，放行（含 Vite dev 代理，
		//   此时 Origin 是 :5173 而不在白名单里）。
		switch strings.ToLower(strings.TrimSpace(c.GetHeader("Sec-Fetch-Site"))) {
		case "cross-site", "same-site":
			c.AbortWithStatusJSON(403, fail(403, "请求来源不合法"))
			return
		case "same-origin":
			c.Next()
			return
		}
		if origin := c.GetHeader("Origin"); origin != "" {
			if _, ok := allowed[origin]; ok {
				c.Next()
				return
			}
			// 有 Origin 但不在白名单：可能是浏览器伪造尝试，直接拒。
			c.AbortWithStatusJSON(403, fail(403, "请求来源不合法"))
			return
		}
		// 两个头都没有：非浏览器客户端。没有会话 Cookie 就没有可被借用的环境凭据，
		// 放行（登录/初始化接口本身靠口令校验）；带 Cookie 的必须证明来源。
		if getCookieValue(c, sessionCookieName) == "" {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(403, fail(403, "请求来源不合法"))
	}
}
