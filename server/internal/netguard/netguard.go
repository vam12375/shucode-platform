// Package netguard:出站连接的目标边界。
// 默认拒绝回环、私网、链路本地、组播、保留段与云元数据等地址，降低管理员配置
// 恶意上游 URL 或 DNS 重绑定导致的 SSRF/密钥外发风险。
package netguard

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrBlockedTarget 供上层区分「被边界策略拒绝」与「真的连不上」：
// 前者是配置问题（运维要改 baseUrl），后者才是网络/上游故障。
var ErrBlockedTarget = errors.New("目标地址属于禁止出站的网段")

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT
	netip.MustParsePrefix("169.254.0.0/16"),  // 含云元数据 169.254.169.254
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF 协议分配
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("198.18.0.0/15"),   // 基准测试
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("240.0.0.0/4"),     // 保留 + 255.255.255.255 广播
	netip.MustParsePrefix("2001:db8::/32"),   // IPv6 文档段
	netip.MustParsePrefix("::/128"),          // 未指定地址
}

// nat64Prefixes:RFC 6052 / RFC 8215 的知名前缀,把内嵌的 IPv4 直接映射成 v6 字面量。
// addr.Unmap() 只剥 ::ffff: 前缀,不处理这两类;若宿主机配有 NAT64/DNS64,
// https://[64:ff9b::a9fe:a9fe] 会绕过 v4 黑名单直达 169.254.169.254。
var nat64Prefixes = []netip.Prefix{
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

// embeddedV4 提取 NAT64 地址内嵌的 IPv4。命中前缀时低 32 位即目标 v4
// (RFC 6052 的 /96 与 /48 都把 v4 放在最低位,差异只在其后的填充字节)。
func embeddedV4(addr netip.Addr) (netip.Addr, bool) {
	if !addr.Is6() {
		return netip.Addr{}, false
	}
	for _, prefix := range nat64Prefixes {
		if !prefix.Contains(addr) {
			continue
		}
		raw := addr.As16()
		return netip.AddrFrom4([4]byte{raw[12], raw[13], raw[14], raw[15]}), true
	}
	return netip.Addr{}, false
}

func blockedAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	// NAT64 先解包成真正的 v4 再判:否则 v6 分支既看不出私网也看不出元数据。
	if v4, ok := embeddedV4(addr); ok {
		addr = v4
	}
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() {
		return true
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// NewTransport:allowPrivate 为 true 时允许私网/回环目标（本地开发/自建内网上游），
// 链路本地与组播始终拒绝。Control 在 DNS 解析后、真正连接前执行，可覆盖 DNS 重绑定。
func NewTransport(allowPrivate bool) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host := address
			if parsed, err := netip.ParseAddrPort(address); err == nil {
				host = parsed.Addr().Unmap().String()
			}
			addr, err := netip.ParseAddr(host)
			if err != nil {
				return ErrBlockedTarget
			}
			// 这里是「DNS 已解析、socket 尚未 connect」的最后一跳，
			// 与 blockedAddress 用同一套判定，避免两处规则漂移。
			if addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() ||
				addr.IsUnspecified() {
				return ErrBlockedTarget
			}
			if !allowPrivate && blockedAddress(addr) {
				return ErrBlockedTarget
			}
			return nil
		},
	}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}
