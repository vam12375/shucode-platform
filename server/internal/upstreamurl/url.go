// Package upstreamurl 统一控制面与网关的上游 API 地址拼接。
package upstreamurl

import (
	"net/url"
	"regexp"
	"strings"
)

var versionSuffix = regexp.MustCompile(`(?i)/v[0-9]+$`)

// Join 将网关的标准 /v1/... 路径拼到供应商的服务根或版本化 API 前缀。
func Join(baseURL, path string) string {
	base := strings.TrimRight(baseURL, "/")
	parsed, err := url.Parse(base)
	// 原先固定追加 /v1，使 Ark 的 /api/v3 变成 /api/v3/v1 并返回 404。
	// 显式版本前缀应保留上游版本；未带版本的旧地址仍追加标准 /v1。
	if err == nil && versionSuffix.MatchString(parsed.Path) && strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1")
	}
	return base + path
}
