// Package netguard:出站连接的目标边界。
// 默认拒绝回环、私网、链路本地、组播与云元数据等地址，降低管理员配置恶意
// 上游 URL 或 DNS 重绑定导致的 SSRF/密钥外发风险。
package netguard

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

var (
	errBlockedTarget = errors.New("目标地址属于禁止出站的网段")
	blockedPrefixes  = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),  // CGNAT
		netip.MustParsePrefix("169.254.0.0/16"), // 含云元数据 169.254.169.254
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("::/128"),
	}
)

func blockedAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
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
				return errBlockedTarget
			}
			if addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() ||
				addr.IsUnspecified() {
				return errBlockedTarget
			}
			if !allowPrivate && blockedAddress(addr) {
				return errBlockedTarget
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
