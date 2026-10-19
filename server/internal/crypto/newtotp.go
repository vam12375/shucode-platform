package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

func GenerateTOTPSecret() (string, error) {
	secret := make([]byte, 20)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret), nil
}

// TOTP 按 RFC 6238 的 SHA-1 HOTP 生成六位验证码，counter 为 Unix 秒 / 30。
func TOTP(secret string, counter int64) (string, error) {
	if counter < 0 {
		return "", fmt.Errorf("TOTP counter 无效")
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil || len(key) == 0 {
		return "", fmt.Errorf("TOTP secret 无效")
	}
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], uint64(counter))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(message[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 0x0f
	value := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1_000_000), nil
}

// 已消费 counter 必须严格递增，避免同一验证码被多会话重放。
func VerifyTOTP(secret, code string, at time.Time, lastCounter int64) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	for _, char := range code {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	current := at.Unix() / 30
	for _, offset := range []int64{0, -1, 1} {
		counter := current + offset
		if counter < 0 || counter <= lastCounter {
			continue
		}
		expected, err := TOTP(secret, counter)
		if err == nil && subtle.ConstantTimeCompare([]byte(code), []byte(expected)) == 1 {
			return counter, true
		}
	}
	return 0, false
}
