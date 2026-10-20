// Package crypto:与 packages/db/src/crypto.ts、passwords.ts 逐位兼容的凭据加密、
// 口令哈希与 token 生成(specs/go-rewrite.md §2.2)。存量库里的密文/口令必须可读,
// 新写入必须保持同格式,任何字段顺序/编码差异视为事故。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const secretBoxVersion = "v1"

// SecretBox:AES-256-GCM。密文格式 v1.<iv>.<ciphertext>.<tag>,三段均为
// 不带 padding 的 base64url;tag 为 GCM auth tag(16 字节)单独成段——
// 与 TS 实现(cipher.getAuthTag)完全一致,而不能用 Go Seal 的 ct||tag 直出。
type SecretBox struct {
	key []byte
}

func NewSecretBox(secretKeyBase64 string) (*SecretBox, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(secretKeyBase64))
	if err != nil {
		return nil, fmt.Errorf("PLATFORM_SECRET_KEY 必须是 32 字节 base64: %w", err)
	}
	if len(key) != 32 {
		return nil, errors.New("PLATFORM_SECRET_KEY 必须是 32 字节 base64")
	}
	return &SecretBox{key: key}, nil
}

var b64url = base64.RawURLEncoding

func (b *SecretBox) Encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, iv, []byte(plaintext), nil)
	tag, ct := sealed[len(sealed)-16:], sealed[:len(sealed)-16]
	return strings.Join([]string{
		secretBoxVersion,
		b64url.EncodeToString(iv),
		b64url.EncodeToString(ct),
		b64url.EncodeToString(tag),
	}, "."), nil
}

func (b *SecretBox) Decrypt(payload string) (string, error) {
	parts := strings.Split(payload, ".")
	if len(parts) != 4 || parts[0] != secretBoxVersion ||
		parts[1] == "" || parts[2] == "" || parts[3] == "" {
		// 与 TS 实现同文案:控制台网关错误信息会原样透出。
		return "", errors.New("无法识别的凭据密文")
	}
	iv, err := b64url.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("无法识别的凭据密文")
	}
	ct, err := b64url.DecodeString(parts[2])
	if err != nil {
		return "", errors.New("无法识别的凭据密文")
	}
	tag, err := b64url.DecodeString(parts[3])
	if err != nil {
		return "", errors.New("无法识别的凭据密文")
	}
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	// GCM.Open 遇到错误 nonce 长度会 panic，先校验密文结构再认证，篡改一律拒绝。
	if len(iv) != gcm.NonceSize() || len(tag) != gcm.Overhead() {
		return "", errors.New("无法识别的凭据密文")
	}
	out, err := gcm.Open(nil, iv, append(ct, tag...), nil)
	if err != nil {
		return "", errors.New("无法识别的凭据密文")
	}
	return string(out), nil
}
