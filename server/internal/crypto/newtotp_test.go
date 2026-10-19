package crypto

import (
	"encoding/base32"
	"testing"
	"time"
)

func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	for _, vector := range []struct {
		at   int64
		code string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"}} {
		got, err := TOTP(secret, vector.at/30)
		if err != nil || got != vector.code {
			t.Fatalf("RFC6238 at=%d got=%s err=%v want=%s", vector.at, got, err, vector.code)
		}
		counter, ok := VerifyTOTP(secret, vector.code, time.Unix(vector.at, 0), -1)
		if !ok || counter != vector.at/30 {
			t.Fatal("标准向量验证失败")
		}
		if _, ok := VerifyTOTP(secret, vector.code, time.Unix(vector.at, 0), counter); ok {
			t.Fatal("已使用counter不能重放")
		}
	}
}
func TestTOTPWindowAndMalformedInput(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil || len(raw) != 20 {
		t.Fatal("secret应为20字节高熵base32")
	}
	at := time.Unix(1234567890, 0)
	current := at.Unix() / 30
	for _, offset := range []int64{-2, -1, 0, 1, 2} {
		code, err := TOTP(secret, current+offset)
		if err != nil {
			t.Fatal(err)
		}
		_, ok := VerifyTOTP(secret, code, at, -1)
		if ok != (offset >= -1 && offset <= 1) {
			t.Fatalf("窗口判断失败offset=%d", offset)
		}
	}
	for _, code := range []string{"", "12345", "1234567", "a12345", "１２３４５６"} {
		if _, ok := VerifyTOTP(secret, code, at, -1); ok {
			t.Fatalf("非法验证码 %q 应拒绝", code)
		}
	}
	if _, err := TOTP("invalid!", current); err == nil {
		t.Fatal("非法secret应失败")
	}
}
