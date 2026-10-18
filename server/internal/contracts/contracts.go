// Package contracts:packages/contracts/src/*.ts(zod)的平台侧 Go 镜像。
// 跨端契约以客户端仓库为权威,本包只镜像「服务端必须解析/构造」的部分;
// 一致性由 testdata/fixtures.json(与 TS 用例共享)的对齐测试防漂移。
package contracts

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// ---- 与 platform-auth.ts / release.ts / client-configs.ts 一致的常量 ----

const (
	PlatformDeviceTokenPrefix      = "mdp_"
	PlatformDeepLinkCallbackPrefix = "zcode://platform/callback"
	UserConfigAPIPath              = "/api/v1/client/user-config"
	BuiltinReleaseJSONPathPrefix   = "/api/v1/releases/builtin"
	// PLATFORM_CONFIG_KEYS(client-configs.ts):/console/api/config 的 KV 键,
	// 同时是 /api/v1/client/configs 公开字段的来源。
	PlatformConfigKeyHelp             = "help"
	PlatformConfigKeyForceUpdate      = "forceUpdate"
	PlatformConfigKeyPluginStoreOrder = "pluginStoreOrder"
)

var platformDeviceTokenRe = regexp.MustCompile(`^mdp_[A-Za-z0-9_-]{22,}$`)

func ValidatePlatformDeviceToken(token string) bool {
	return platformDeviceTokenRe.MatchString(token)
}

// Flow ID:trim 后 16..64 字符(platformAuthFlowIdSchema)。
func ValidatePlatformAuthFlowID(raw string) (string, bool) {
	id := strings.TrimSpace(raw)
	if len([]rune(id)) < 16 || len([]rune(id)) > 64 {
		return "", false
	}
	return id, true
}

// buildPlatformDeepLinkCallbackUrl:encodeURIComponent 与 Go 的 QueryEscape
// 对保留字符的处理不同(JS 保留 !~*'()、空格转 %20 而非 +),这里按 JS 语义实现,
// 保证 deep link 逐字节一致。
func BuildPlatformDeepLinkCallbackURL(flowID string) string {
	return PlatformDeepLinkCallbackPrefix + "?flow_id=" + encodeURIComponent(flowID)
}

const unreservedAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~!*'()"

func encodeURIComponent(s string) string {
	var out strings.Builder
	for _, r := range s {
		if r < 128 && strings.ContainsRune(unreservedAlphabet, r) {
			out.WriteRune(r)
			continue
		}
		// UTF-8 逐字节百分号编码(JS encodeURIComponent 的实际行为)。
		for i := 0; i < len(string(r)); i++ {
			out.WriteString(fmt.Sprintf("%%%02X", string(r)[i]))
		}
	}
	return out.String()
}

// RandomUUIDv4:与 TS randomUUID 同规格(v4 小写 连字符)。存储层也用它。
func RandomUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]), nil
}
