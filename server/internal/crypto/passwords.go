package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/scrypt"
)

// scrypt 口令哈希:格式 scrypt$N$r$p$saltB64$hashB64,与 packages/db/src/passwords.ts
// 完全一致;salt 与 hash 都是带 padding 的标准 base64。
// 新哈希采用 OWASP 推荐的 N=2^14/r=8/p=5（16 MiB、5 轮，避免单次登录占用 128 MiB）；
// 存量哈希仍按自身参数验证，登录成功后渐进重哈希。
const (
	scryptDefaultN = 1 << 14
	scryptDefaultR = 8
	scryptDefaultP = 5
	scryptKeyLen   = 64
	saltLen        = 16
)

func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	derived, err := scrypt.Key([]byte(password), salt,
		scryptDefaultN, scryptDefaultR, scryptDefaultP, scryptKeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("scrypt$%d$%d$%d$%s$%s",
		scryptDefaultN, scryptDefaultR, scryptDefaultP,
		base64.StdEncoding.EncodeToString(salt),
		base64.StdEncoding.EncodeToString(derived),
	), nil
}

// NeedsRehash:旧工作因子或非默认长度的哈希应在登录成功后升级。
func NeedsRehash(stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 6 || parts[0] != "scrypt" {
		return true
	}
	n, errN := strconv.Atoi(parts[1])
	r, errR := strconv.Atoi(parts[2])
	p, errP := strconv.Atoi(parts[3])
	expected, errHash := base64.StdEncoding.DecodeString(parts[5])
	if errN != nil || errR != nil || errP != nil || errHash != nil {
		return true
	}
	return n != scryptDefaultN || r != scryptDefaultR || p != scryptDefaultP ||
		len(expected) != scryptKeyLen
}

func VerifyPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 6 || parts[0] != "scrypt" {
		return false
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n <= 0 {
		return false
	}
	r, err := strconv.Atoi(parts[2])
	if err != nil || r <= 0 {
		return false
	}
	p, err := strconv.Atoi(parts[3])
	if err != nil || p <= 0 {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	expected, err := base64.StdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) == 0 {
		return false
	}
	derived, err := scrypt.Key([]byte(password), salt, n, r, p, len(expected))
	if err != nil {
		return false
	}
	return len(derived) == len(expected) &&
		subtle.ConstantTimeCompare(derived, expected) == 1
}

// ---- 访问凭据形态(与 crypto.ts 一致) ----

// randomId 由调用方直接使用 google/uuid(规格:v4 小写),此处不包一层。
// 访问凭据(设备 token / 会话 token)一律只存 sha256 十六进制哈希。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum)
}

func GenerateDeviceToken() string {
	buf := make([]byte, 24)
	mustRandom(buf)
	return "mdp_" + base64.RawURLEncoding.EncodeToString(buf)
}

func GenerateSessionToken() string {
	buf := make([]byte, 32)
	mustRandom(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

func GeneratePollToken() string {
	buf := make([]byte, 32)
	mustRandom(buf)
	return fmt.Sprintf("%x", buf)
}

// DeviceTokenHint 展示位:TS 为 token.slice(-4),不足 4 位时原样返回。
func DeviceTokenHint(token string) string {
	if len(token) < 4 {
		return token
	}
	return token[len(token)-4:]
}

func mustRandom(buf []byte) {
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("crypto/rand 不可用: %v", err))
	}
}
