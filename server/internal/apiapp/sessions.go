package apiapp

import (
	"context"
	"crypto/rand"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// sessions.ts 的镜像:控制台与设备登录页共用 mendao_session cookie。

const sessionCookieName = "mendao_session"

const sessionTTL = 7 * 24 * time.Hour

func getCookieValue(c *gin.Context, name string) string {
	value, err := c.Cookie(name)
	if err != nil {
		return ""
	}
	return value
}

// readSessionUser:过期会话删除并返回 nil;touch last_seen;禁用账号返回 nil。
func readSessionUser(ctx context.Context, deps *Deps, c *gin.Context) (*store.UserRecord, error) {
	raw := getCookieValue(c, sessionCookieName)
	if raw == "" {
		return nil, nil
	}
	session, err := deps.Store.FindSession(ctx, crypto.HashToken(raw))
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, nil
	}
	if !session.ExpiresAt.After(deps.Now()) {
		if err := deps.Store.DeleteSession(ctx, session.TokenHash); err != nil {
			return nil, err
		}
		return nil, nil
	}
	user, err := deps.Store.FindUserByID(ctx, session.UserID)
	if err != nil {
		return nil, err
	}
	if user == nil || user.DisabledAt != nil {
		return nil, nil
	}
	if err := deps.Store.TouchSession(ctx, session.TokenHash, deps.Now()); err != nil {
		return nil, err
	}
	return user, nil
}

func loginWithSession(ctx context.Context, deps *Deps, c *gin.Context, userID string) error {
	// TS 生成 `${randomUUID()}.${Math.random().toString(36).slice(2)}` 形态;
	// token 是不透明凭据,这里保持同形态(uuid + 点 + 基 36 随机后缀)。
	suffixBytes := make([]byte, 8)
	if _, err := rand.Read(suffixBytes); err != nil {
		return err
	}
	token := uuid.NewString() + "." + base36(suffixBytes)
	now := deps.Now()
	if err := deps.Store.CreateSession(ctx, store.SessionRecord{
		TokenHash:  crypto.HashToken(token),
		UserID:     userID,
		CreatedAt:  now,
		ExpiresAt:  now.Add(sessionTTL),
		LastSeenAt: now,
	}); err != nil {
		return err
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(sessionCookieName, token, int(sessionTTL.Seconds()), "/", deps.Env.CookieDomain, deps.Env.SecureCookies, true)
	return nil
}

func logoutSession(ctx context.Context, deps *Deps, c *gin.Context) error {
	raw := getCookieValue(c, sessionCookieName)
	if raw != "" {
		if err := deps.Store.DeleteSession(ctx, crypto.HashToken(raw)); err != nil {
			return err
		}
	}
	c.SetSameSite(http.SameSiteLaxMode)
	// MaxAge<0 立即过期等价 deleteCookie。
	c.SetCookie(sessionCookieName, "", -1, "/", deps.Env.CookieDomain, deps.Env.SecureCookies, true)
	return nil
}

func readBearerToken(c *gin.Context) string {
	header := strings.TrimSpace(c.GetHeader("authorization"))
	if len(header) >= 7 && strings.EqualFold(header[:7], "bearer ") {
		if token := strings.TrimSpace(header[7:]); token != "" {
			return token
		}
	}
	return strings.TrimSpace(c.GetHeader("x-api-key"))
}

type AuthenticatedDevice struct {
	User           *store.UserRecord
	Device         *store.DeviceTokenRecord
	PlaintextToken string // secretBox 缺失/解密失败时为空
}

func authenticateDevice(ctx context.Context, deps *Deps, c *gin.Context) (*AuthenticatedDevice, error) {
	token := readBearerToken(c)
	if token == "" {
		return nil, nil
	}
	device, err := deps.Store.FindDeviceTokenByHash(ctx, crypto.HashToken(token))
	if err != nil {
		return nil, err
	}
	if device == nil || device.RevokedAt != nil {
		return nil, nil
	}
	user, err := deps.Store.FindUserByID(ctx, device.UserID)
	if err != nil {
		return nil, err
	}
	if user == nil || user.DisabledAt != nil {
		return nil, nil
	}
	if err := deps.Store.TouchDeviceToken(ctx, device.ID, deps.Now()); err != nil {
		return nil, err
	}
	auth := &AuthenticatedDevice{User: user, Device: device}
	if deps.SecretBox != nil {
		plaintext, decErr := deps.SecretBox.Decrypt(device.TokenCiphertext)
		if decErr != nil {
			deps.Logger.Warn("设备 token 解密失败，user-config 将不携带凭据", "error", decErr)
		} else {
			auth.PlaintextToken = plaintext
		}
	}
	return auth, nil
}

// base36:把随机字节编码为 [0-9a-z] 串(等价 Math.random().toString(36).slice(2) 的字符集与长度)。
func base36(buf []byte) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	// 逐字节映射为两字符基 36(高位在前),长度稳定且熵足够。
	out := make([]byte, 0, len(buf)*2)
	for _, b := range buf {
		out = append(out, alphabet[b/36], alphabet[b%36])
	}
	return string(out)
}
