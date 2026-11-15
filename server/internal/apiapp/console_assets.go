package apiapp

import (
	"encoding/json"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

type consoleAssetManifest struct {
	Version int               `json:"version"`
	Files   map[string]string `json:"files"`
}

func consoleStaticMiddleware(deps *Deps) gin.HandlerFunc {
	rootPath, err := filepath.Abs(deps.Env.ConsoleDistDir)
	if err != nil {
		rootPath = deps.Env.ConsoleDistDir
	}
	manifest := readConsoleAssetManifest(rootPath)
	if manifest.Version != 1 {
		deps.Logger.Error("控制台资源清单无效，已停止交付静态资源")
	}
	return func(c *gin.Context) {
		requestPath := c.Request.URL.Path
		if (requestPath == "/" || requestPath == "/console") && (c.Request.Method == "GET" || c.Request.Method == "HEAD") {
			c.Redirect(http.StatusFound, "/console/")
			c.Abort()
			return
		}
		if !strings.HasPrefix(requestPath, "/console/") || strings.HasPrefix(requestPath, "/console/api/") {
			c.Next()
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Header("X-Robots-Tag", "noindex, nofollow, noarchive")
		c.Abort()
		reject := func() { c.Status(http.StatusNotFound) }
		if manifest.Version != 1 || (c.Request.Method != "GET" && c.Request.Method != "HEAD") {
			reject()
			return
		}
		relative := strings.TrimPrefix(requestPath, "/console/")
		if strings.ContainsAny(relative, "\\\x00") || strings.Contains(relative, "..") || strings.HasPrefix(relative, ".") || (relative != "" && path.Clean(relative) != relative) {
			reject()
			return
		}
		access, exists := manifest.Files[relative]
		isIndex := relative == "" || relative == "index.html"
		// 只有无扩展名的页面路径可回落；缺失资源、源映射、目录和清单一律 404。
		if !exists && !isIndex && path.Ext(relative) == "" && relative != "api" && relative != "assets" && !strings.HasPrefix(relative, "assets/") {
			isIndex = true
		}
		if isIndex {
			relative, access = "index.html", "public"
		} else if !exists || !allowConsoleAsset(c, deps, access) {
			reject()
			return
		}
		// os.Root 防止符号链接和路径规范化绕过静态目录边界。
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			reject()
			return
		}
		defer root.Close()
		file, err := root.Open(relative)
		if err != nil {
			reject()
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			reject()
			return
		}
		mime := map[string]string{".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8", ".svg": "image/svg+xml", ".png": "image/png", ".ico": "image/x-icon", ".woff2": "font/woff2"}
		c.Header("Content-Type", mime[strings.ToLower(filepath.Ext(relative))])
		if access == "public" && !isIndex {
			c.Header("Cache-Control", "public, max-age=3600")
			if strings.HasPrefix(relative, "assets/") {
				c.Header("Cache-Control", "public, max-age=31536000, immutable")
			}
		}
		// 私有资源不提供修改时间，条件请求也必须重新鉴权，禁止缓存跨会话复用。
		http.ServeContent(c.Writer, c.Request, relative, time.Time{}, file)
	}
}

// 原先整个 dist 可匿名读取，隐藏菜单无法阻止下载管理代码。
// 仅交付本次构建列出的文件；清单缺失、损坏、未知权限均拒绝，不能退回公开托管。
func readConsoleAssetManifest(root string) consoleAssetManifest {
	var manifest consoleAssetManifest
	data, err := os.ReadFile(filepath.Join(root, "asset-access.json"))
	if err != nil || json.Unmarshal(data, &manifest) != nil || manifest.Version != 1 {
		return consoleAssetManifest{}
	}
	for name, access := range manifest.Files {
		if !validConsoleAssetPath(name) || (access != "public" && access != "session" && access != "platform" && access != "setup") {
			return consoleAssetManifest{}
		}
	}
	return manifest
}

func validConsoleAssetPath(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") {
			return false
		}
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".js", ".css", ".svg", ".png", ".ico", ".woff2":
		return true
	}
	return false
}

func allowConsoleAsset(c *gin.Context, deps *Deps, access string) bool {
	if access == "public" {
		return true
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("Vary", "Cookie")
	if access == "setup" {
		return setupAccessAllowed(c, deps, false)
	}
	if access != "session" && access != "platform" {
		return false
	}
	user, err := readSessionUser(c.Request.Context(), deps, c)
	if err != nil || user == nil {
		return false
	}
	if access == "platform" {
		return user.Role == store.RoleRoot && !rootMFAEnrollmentRequired(c.Request.Context(), deps, c, user)
	}
	return true
}

const setupAccessCookie = "mendao_setup_access"
const setupAccessTTL = 5 * time.Minute

type setupProof struct {
	Purpose string `json:"purpose"`
	Expires int64  `json:"expires"`
	Binding string `json:"binding"`
}

func setupCredentialBinding(deps *Deps) string {
	return crypto.HashToken("console-setup:" + deps.Env.BootstrapSecret)
}

// 短时加密凭据只用于初始化资源，不能代替账号会话；创建完成后立即失效。
func setupAccessAllowed(c *gin.Context, deps *Deps, allowHeader bool) bool {
	allowed := false
	if allowHeader {
		allowed = (deps.Env.MemoryDB && deps.Env.BootstrapSecret == "") ||
			(deps.Env.BootstrapSecret != "" && secretsEqual(c.GetHeader("x-bootstrap-secret"), deps.Env.BootstrapSecret))
	}
	if !allowed && deps.SecretBox != nil {
		var proof setupProof
		if value, err := deps.SecretBox.Decrypt(getCookieValue(c, setupAccessCookie)); err == nil && json.Unmarshal([]byte(value), &proof) == nil {
			allowed = proof.Purpose == "console-setup" && proof.Expires > deps.Now().Unix() &&
				proof.Expires <= deps.Now().Add(setupAccessTTL).Unix() && secretsEqual(proof.Binding, setupCredentialBinding(deps))
		}
	}
	if !allowed {
		return false
	}
	user, err := deps.Store.FindRootUser(c.Request.Context())
	return err == nil && user == nil
}

func registerSetupAccess(group *gin.RouterGroup, deps *Deps) {
	group.POST("/setup/access", func(c *gin.Context) {
		if !allowPublicRead(c, deps, "setup-access") {
			return
		}
		if !setupAccessAllowed(c, deps, true) || deps.SecretBox == nil {
			c.Status(http.StatusNotFound)
			return
		}
		payload, _ := json.Marshal(setupProof{Purpose: "console-setup", Expires: deps.Now().Add(setupAccessTTL).Unix(), Binding: setupCredentialBinding(deps)})
		value, err := deps.SecretBox.Encrypt(string(payload))
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.SetSameSite(http.SameSiteStrictMode)
		c.SetCookie(setupAccessCookie, value, int(setupAccessTTL.Seconds()), "/console", "", deps.Env.SecureCookies, true)
		c.JSON(200, okData(gin.H{}))
	})
}
