package crypto

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type vectorsFile struct {
	Key       string `json:"key"`
	Secretbox []struct {
		Plain   string `json:"plain"`
		Payload string `json:"payload"`
	} `json:"secretbox"`
	Passwords []struct {
		Password string `json:"password"`
		Stored   string `json:"stored"`
	} `json:"passwords"`
}

func loadVectors(t *testing.T) vectorsFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatalf("读取冻结的互操作向量 testdata/vectors.json(迁移期由 TS 实现生成,勿手改): %v", err)
	}
	var v vectorsFile
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("解析向量文件: %v", err)
	}
	return v
}

// TS 侧产生的密文必须能直接解;Go 再加密同一明文须可解回且格式一致。
func TestSecretBoxDecryptsTSVectors(t *testing.T) {
	v := loadVectors(t)
	box, err := NewSecretBox(v.Key)
	if err != nil {
		t.Fatalf("构造 SecretBox: %v", err)
	}
	for _, item := range v.Secretbox {
		if item.Plain == "" {
			// TS 的密文段为空串时是 falsy,解密必然抛「无法识别的凭据密文」;
			// Go 行为必须同构(实际业务不会写空明文凭据,仅为对齐语义)。
			if _, err := box.Decrypt(item.Payload); err == nil {
				t.Fatal("空明文密文应解密失败(与 TS 一致)")
			}
			continue
		}
		got, err := box.Decrypt(item.Payload)
		if err != nil {
			t.Fatalf("解 TS 密文失败(%q): %v", item.Plain, err)
		}
		if got != item.Plain {
			t.Fatalf("明文不一致: got %q want %q", got, item.Plain)
		}
		round, err := box.Encrypt(item.Plain)
		if err != nil {
			t.Fatalf("Go 加密失败(%q): %v", item.Plain, err)
		}
		if parts := strings.Split(round, "."); len(parts) != 4 || parts[0] != "v1" {
			t.Fatalf("Go 密文格式不符: %q", round)
		} else {
			back, err := box.Decrypt(round)
			if err != nil || back != item.Plain {
				t.Fatalf("Go 密文回读失败(%q): %v", item.Plain, err)
			}
		}
	}
}

func TestSecretBoxRejectsBadFormats(t *testing.T) {
	box, err := NewSecretBox("qv3YmS8XKg0SdoLJc1TzW7tQe4pXbUcHnMhR9IyAL2k=")
	if err != nil {
		t.Fatalf("构造 SecretBox: %v", err)
	}
	for _, bad := range []string{
		"",
		"v1",
		"v1.aaa",
		"v1.aGVsbG8",
		"v2.a.b.c",
		"v1.!!!.aGVsbG8.c2lnbg",
	} {
		if _, err := box.Decrypt(bad); err == nil {
			t.Fatalf("应拒绝非法密文: %q", bad)
		}
	}
	if _, err := NewSecretBox("short"); err == nil {
		t.Fatal("非 32 字节 key 应报错")
	}
}

func TestPasswordVectors(t *testing.T) {
	v := loadVectors(t)
	for _, item := range v.Passwords {
		if !VerifyPassword(item.Password, item.Stored) {
			t.Fatalf("TS 口令哈希验证失败: %q", item.Password)
		}
		if VerifyPassword(item.Password+"x", item.Stored) {
			t.Fatalf("错误口令不应通过: %q", item.Password)
		}
		made, err := HashPassword(item.Password)
		if err != nil {
			t.Fatalf("HashPassword: %v", err)
		}
		if !VerifyPassword(item.Password, made) {
			t.Fatalf("Go 生成口令回验失败: %q", item.Password)
		}
	}
	for _, bad := range []string{"", "md5$1$2$3$4$5", "scrypt$a$b$c$d$e", "scrypt$16384$8$1$!!!$!!!"} {
		if VerifyPassword("x", bad) {
			t.Fatalf("应拒绝非法哈希串: %q", bad)
		}
	}
}

func TestPasswordRehashDetection(t *testing.T) {
	made, err := HashPassword("rehash-password")
	if err != nil {
		t.Fatal(err)
	}
	if NeedsRehash(made) {
		t.Fatal("当前参数生成的哈希不应要求重哈希")
	}
	// 旧工作因子（N=16384/p=1）仍可验证，但应被标记为需要升级。
	legacy := "scrypt$16384$8$1$c2FsdHNhbHRzYWx0c2E=$" + strings.Repeat("A", 88)
	if !NeedsRehash(legacy) {
		t.Fatal("旧工作因子应要求重哈希")
	}
	if !NeedsRehash("not-a-hash") {
		t.Fatal("非法哈希应要求重哈希")
	}
}

func TestTokenFormats(t *testing.T) {
	token := GenerateDeviceToken()
	// mdp_ 前缀 + 24 字节 base64url(32 字符)。
	if !strings.HasPrefix(token, "mdp_") || len(token) != len("mdp_")+32 {
		t.Fatalf("设备 token 形态不符: %q", token)
	}
	if n := len(GenerateSessionToken()); n != 43 { // 32 字节 base64url → 43 字符
		t.Fatalf("会话 token 长度不符: %d", n)
	}
	poll := GeneratePollToken()
	if n := len(poll); n != 64 { // 32 字节 hex
		t.Fatalf("轮询凭据长度不符: %d", n)
	}
	if DeviceTokenHint("abc") != "abc" {
		t.Fatal("短 token hint 应原样返回")
	}
	if DeviceTokenHint("mdp_abcdefgh1234") != "1234" {
		t.Fatal("hint 应取末 4 位")
	}
}
