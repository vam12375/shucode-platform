package apiapp

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// MFA 私有状态使用独立配置键，普通公开配置接口只返回固定白名单字段。
// secret 仅以 AES-GCM 密文保存；恢复码只保存 SHA-256 摘要。
type rootMFAState struct {
	SecurityVersion  string     `json:"securityVersion"`
	Version          int        `json:"version"`
	Enabled          bool       `json:"enabled"`
	SecretCiphertext string     `json:"secretCiphertext,omitempty"`
	PendingExpiresAt *time.Time `json:"pendingExpiresAt,omitempty"`
	RecoveryHashes   []string   `json:"recoveryHashes,omitempty"`
	LastCounter      int64      `json:"lastCounter"`
}

func rootMFAKey(userID string) string { return "_security.root-mfa." + userID }
func readRootMFAState(ctx context.Context, deps *Deps, userID string) (rootMFAState, []byte, error) {
	state := rootMFAState{Version: 1, LastCounter: -1}
	raw, err := deps.Store.GetConfigValue(ctx, rootMFAKey(userID))
	if err != nil {
		return state, nil, err
	}
	if raw == nil {
		return state, nil, nil
	}
	// 持久状态必须明确带版本，不能把损坏的 {} 当作「未启用」而绕过第二因素。
	var persisted rootMFAState
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return state, nil, err
	}
	state = persisted
	if state.Version != 1 || state.LastCounter < -1 || len(state.RecoveryHashes) > 10 || (state.Enabled && (state.SecretCiphertext == "" || state.PendingExpiresAt != nil || state.SecurityVersion == "")) {
		return state, nil, fmt.Errorf("MFA 状态无效")
	}
	for _, hash := range state.RecoveryHashes {
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != 32 {
			return state, nil, fmt.Errorf("MFA 恢复状态无效")
		}
	}
	return state, raw, nil
}
func saveRootMFAState(ctx context.Context, deps *Deps, userID string, expected []byte, state rootMFAState) (bool, error) {
	raw, err := jsonMarshalNoHTML(state)
	if err != nil {
		return false, err
	}
	return deps.Store.CompareAndSwapConfigValue(ctx, rootMFAKey(userID), expected, raw)
}
func rootMFAUnavailable(c *gin.Context) {
	c.JSON(503, fail(503, "安全验证暂不可用，请稍后重试"))
}
func allowRootMFAAttempt(ctx context.Context, deps *Deps, c *gin.Context, userID string) bool {
	if deps.ConsoleLoginLimiter == nil {
		rootMFAUnavailable(c)
		return false
	}
	allowed, err := deps.ConsoleLoginLimiter.Allow(ctx, "root-mfa:"+userID)
	if err != nil {
		rootMFAUnavailable(c)
		return false
	}
	if !allowed {
		c.JSON(429, fail(429, "安全验证尝试次数过多，请稍后重试"))
		return false
	}
	return true
}
func rootMFAPassword(ctx context.Context, deps *Deps, c *gin.Context, user *store.UserRecord, body map[string]any) bool {
	if !allowRootMFAAttempt(ctx, deps, c, user.ID) {
		return false
	}
	password, ok := body["currentPassword"].(string)
	if !ok || password == "" || len(password) > 200 || !crypto.VerifyPassword(password, user.PasswordHash) {
		c.JSON(403, fail(403, "当前密码不正确"))
		return false
	}
	return true
}
func normalizeRecoveryCode(code string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
}
func generateRootRecoveryCodes() ([]string, []string, error) {
	codes := make([]string, 0, 10)
	hashes := make([]string, 0, 10)
	seen := map[string]bool{}
	for len(codes) < 10 {
		raw := make([]byte, 16)
		if _, err := cryptorand.Read(raw); err != nil {
			return nil, nil, err
		}
		plain := strings.ToUpper(hex.EncodeToString(raw))
		if seen[plain] {
			continue
		}
		seen[plain] = true
		groups := make([]string, 0, 8)
		for i := 0; i < len(plain); i += 4 {
			groups = append(groups, plain[i:i+4])
		}
		codes = append(codes, strings.Join(groups, "-"))
		hashes = append(hashes, crypto.HashToken(plain))
	}
	return codes, hashes, nil
}
func consumeRootMFAFactor(deps *Deps, state rootMFAState, body map[string]any) (rootMFAState, bool, error) {
	code, _ := body["code"].(string)
	recovery, _ := body["recoveryCode"].(string)
	code = strings.TrimSpace(code)
	recovery = strings.TrimSpace(recovery)
	if (code == "") == (recovery == "") {
		return state, false, nil
	}
	if recovery != "" {
		if len(recovery) > 200 {
			return state, false, nil
		}
		hash := crypto.HashToken(normalizeRecoveryCode(recovery))
		for i, expected := range state.RecoveryHashes {
			if subtle.ConstantTimeCompare([]byte(hash), []byte(expected)) == 1 {
				next := append([]string{}, state.RecoveryHashes[:i]...)
				state.RecoveryHashes = append(next, state.RecoveryHashes[i+1:]...)
				return state, true, nil
			}
		}
		return state, false, nil
	}
	if deps.SecretBox == nil {
		return state, false, fmt.Errorf("MFA 加密服务不可用")
	}
	secret, err := deps.SecretBox.Decrypt(state.SecretCiphertext)
	if err != nil {
		return state, false, err
	}
	counter, ok := crypto.VerifyTOTP(secret, code, deps.Now(), state.LastCounter)
	if !ok {
		return state, false, nil
	}
	state.LastCounter = counter
	return state, true, nil
}
func rejectRootMFA(c *gin.Context) {
	c.JSON(403, fail(403, "动态验证码或恢复码无效、已使用，请重新验证"))
}

// 密码已验证后才调用此 helper；CAS 同时消费 OTP counter/恢复码，跨实例重放也不能登录。
func verifyRootMFA(ctx context.Context, deps *Deps, c *gin.Context, user *store.UserRecord, body map[string]any) bool {
	if user.Role != store.RoleRoot {
		return true
	}
	state, raw, err := readRootMFAState(ctx, deps, user.ID)
	if err != nil {
		rootMFAUnavailable(c)
		return false
	}
	if !state.Enabled {
		cacheSessionSecurityProof(c, user.ID, state.SecurityVersion)
		return true
	}
	if !allowRootMFAAttempt(ctx, deps, c, user.ID) {
		return false
	}
	next, valid, err := consumeRootMFAFactor(deps, state, body)
	if err != nil {
		rootMFAUnavailable(c)
		return false
	}
	if !valid {
		rejectRootMFA(c)
		return false
	}
	swapped, err := saveRootMFAState(ctx, deps, user.ID, raw, next)
	if err != nil {
		rootMFAUnavailable(c)
		return false
	}
	if !swapped {
		rejectRootMFA(c)
		return false
	}
	cacheSessionSecurityProof(c, user.ID, next.SecurityVersion)
	return true
}
func readRootMFABody(c *gin.Context, fields ...string) (map[string]any, bool) {
	body, ok := readObjectBody(c)
	if !ok {
		c.JSON(400, fail(400, "请求参数不合法"))
		return nil, false
	}
	known := map[string]bool{}
	for _, field := range fields {
		known[field] = true
	}
	for key := range body {
		if !known[key] {
			c.JSON(400, fail(400, "不支持的安全验证字段"))
			return nil, false
		}
	}
	return body, true
}
func renewRootMFASession(ctx context.Context, deps *Deps, c *gin.Context, userID, securityVersion string) bool {
	cacheSessionSecurityProof(c, userID, securityVersion)
	if err := deps.Store.DeleteSessionsForUser(ctx, userID); err != nil {
		// 版本已原子轮换，旧会话即使清理失败也已失效；清理只是回收存储。
		deps.Logger.Warn("旧安全会话清理失败", "error", err)
	}
	user, err := deps.Store.FindUserByID(ctx, userID)
	if err != nil || user == nil {
		deps.Logger.Error("MFA 会话续签读取用户失败", "error", err)
		rootMFAUnavailable(c)
		return false
	}
	if err := loginWithSession(ctx, deps, c, user); err != nil {
		rootMFAUnavailable(c)
		return false
	}
	return true
}
func registerRootMFARoutes(router *gin.Engine, deps *Deps) {
	group := router.Group("/console/api/security/mfa")
	group.GET("", func(c *gin.Context) {
		ctx := c.Request.Context()
		user, ok := requireRoot(ctx, deps, c)
		if !ok {
			return
		}
		state, _, err := readRootMFAState(ctx, deps, user.ID)
		if err != nil {
			rootMFAUnavailable(c)
			return
		}
		c.JSON(200, okData(gin.H{"enabled": state.Enabled, "recoveryCodesRemaining": len(state.RecoveryHashes)}))
	})
	group.POST("/setup", func(c *gin.Context) {
		ctx := c.Request.Context()
		user, ok := requireRoot(ctx, deps, c)
		if !ok {
			return
		}
		body, ok := readRootMFABody(c, "currentPassword")
		if !ok {
			return
		}
		if !rootMFAPassword(ctx, deps, c, user, body) {
			return
		}
		state, raw, err := readRootMFAState(ctx, deps, user.ID)
		if err != nil {
			rootMFAUnavailable(c)
			return
		}
		if state.Enabled {
			c.JSON(409, fail(409, "MFA 已启用，请先验证并停用后重新设置"))
			return
		}
		if deps.SecretBox == nil {
			c.JSON(503, fail(503, "未配置凭据加密服务，不能设置 MFA"))
			return
		}
		secret, err := crypto.GenerateTOTPSecret()
		if err != nil {
			rootMFAUnavailable(c)
			return
		}
		ciphertext, err := deps.SecretBox.Encrypt(secret)
		if err != nil {
			rootMFAUnavailable(c)
			return
		}
		expires := deps.Now().Add(10 * time.Minute)
		next := rootMFAState{Version: 1, SecurityVersion: state.SecurityVersion, SecretCiphertext: ciphertext, PendingExpiresAt: &expires, LastCounter: -1}
		swapped, err := saveRootMFAState(ctx, deps, user.ID, raw, next)
		if err != nil {
			rootMFAUnavailable(c)
			return
		}
		if !swapped {
			c.JSON(409, fail(409, "安全设置已变化，请重新设置"))
			return
		}
		otpURL := url.URL{Scheme: "otpauth", Host: "totp", Path: "/Mendao Platform:" + user.Email}
		query := url.Values{"secret": {secret}, "issuer": {"Mendao Platform"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
		otpURL.RawQuery = query.Encode()
		c.JSON(200, okData(gin.H{"secret": secret, "otpauthUrl": otpURL.String()}))
	})
	group.POST("/enable", func(c *gin.Context) {
		ctx := c.Request.Context()
		user, ok := requireRoot(ctx, deps, c)
		if !ok {
			return
		}
		body, ok := readRootMFABody(c, "code")
		if !ok {
			return
		}
		if !allowRootMFAAttempt(ctx, deps, c, user.ID) {
			return
		}
		state, raw, err := readRootMFAState(ctx, deps, user.ID)
		if err != nil {
			rootMFAUnavailable(c)
			return
		}
		if state.Enabled {
			c.JSON(409, fail(409, "MFA 已启用"))
			return
		}
		if state.PendingExpiresAt == nil || !state.PendingExpiresAt.After(deps.Now()) {
			c.JSON(410, fail(410, "MFA 设置已过期，请重新设置"))
			return
		}
		next, valid, err := consumeRootMFAFactor(deps, state, body)
		if err != nil {
			rootMFAUnavailable(c)
			return
		}
		if !valid {
			rejectRootMFA(c)
			return
		}
		codes, hashes, err := generateRootRecoveryCodes()
		if err != nil {
			rootMFAUnavailable(c)
			return
		}
		next.Enabled = true
		next.PendingExpiresAt = nil
		next.RecoveryHashes = hashes
		next.SecurityVersion = crypto.GenerateSessionToken()
		swapped, err := saveRootMFAState(ctx, deps, user.ID, raw, next)
		if err != nil {
			rootMFAUnavailable(c)
			return
		}
		if !swapped {
			c.JSON(409, fail(409, "安全设置已变化，请重新验证"))
			return
		}
		if !renewRootMFASession(ctx, deps, c, user.ID, next.SecurityVersion) {
			return
		}
		writeAudit(ctx, deps, user, "security.mfa.enabled", "user", user.ID, map[string]any{"recoveryCodeCount": len(codes)})
		c.JSON(200, okData(gin.H{"recoveryCodes": codes}))
	})
	for _, action := range []string{"disable", "recovery-codes"} {
		group.POST("/"+action, func(c *gin.Context) {
			ctx := c.Request.Context()
			user, ok := requireRoot(ctx, deps, c)
			if !ok {
				return
			}
			body, ok := readRootMFABody(c, "currentPassword", "code", "recoveryCode")
			if !ok {
				return
			}
			if !rootMFAPassword(ctx, deps, c, user, body) {
				return
			}
			state, raw, err := readRootMFAState(ctx, deps, user.ID)
			if err != nil {
				rootMFAUnavailable(c)
				return
			}
			if !state.Enabled {
				c.JSON(409, fail(409, "MFA 尚未启用"))
				return
			}
			next, valid, err := consumeRootMFAFactor(deps, state, body)
			if err != nil {
				rootMFAUnavailable(c)
				return
			}
			if !valid {
				rejectRootMFA(c)
				return
			}
			codes := []string{}
			auditAction := "security.mfa.disabled"
			if action == "disable" {
				next = rootMFAState{Version: 1, LastCounter: -1}
			} else {
				var hashes []string
				codes, hashes, err = generateRootRecoveryCodes()
				if err != nil {
					rootMFAUnavailable(c)
					return
				}
				next.RecoveryHashes = hashes
				auditAction = "security.mfa.recovery_rotated"
			}
			next.SecurityVersion = crypto.GenerateSessionToken()
			swapped, err := saveRootMFAState(ctx, deps, user.ID, raw, next)
			if err != nil {
				rootMFAUnavailable(c)
				return
			}
			if !swapped {
				rejectRootMFA(c)
				return
			}
			if !renewRootMFASession(ctx, deps, c, user.ID, next.SecurityVersion) {
				return
			}
			writeAudit(ctx, deps, user, auditAction, "user", user.ID, map[string]any{"recoveryCodeCount": len(codes)})
			if action == "disable" {
				c.JSON(200, okData(gin.H{"enabled": false}))
			} else {
				c.JSON(200, okData(gin.H{"recoveryCodes": codes}))
			}
		})
	}
}

// 设备授权表单同样会签发控制台会话；已启用 MFA 的 root 必须先完成控制台双因素登录。
func allowRootMFAPlatformPasswordLogin(ctx context.Context, deps *Deps, c *gin.Context, user *store.UserRecord) bool {
	if user.Role != store.RoleRoot {
		return true
	}
	state, _, err := readRootMFAState(ctx, deps, user.ID)
	if err != nil {
		rootMFAUnavailable(c)
		return false
	}
	if !state.Enabled {
		cacheSessionSecurityProof(c, user.ID, state.SecurityVersion)
		return true
	}
	origin := strings.TrimRight(deps.Env.PublicConsoleOrigin, "/")
	target := origin + "/console/#/login"
	body := `<p class="brand">Mendao</p><h1>需要双因素验证</h1><p>请先在控制台完成登录，再返回本页面继续设备授权。</p><p><a href="` + escapeHTML(target) + `" target="_blank" rel="noopener">打开控制台登录</a></p>`
	c.Data(403, "text/html; charset=utf-8", []byte(htmlLayout("需要双因素验证 - Mendao", body, "")))
	return false
}
