// Package netutil: 客户端 IP 归一与可信反代解析。
// 控制面与网关共用同一套边界：只有 TCP 对端命中精确 IP 白名单时，
// 才接受转发头；否则一律使用 TCP 对端地址。
package netutil

import (
	"errors"
	"net"
	"net/netip"
	"strings"
)

// NormalizeIP:仅接受 IP 字面量;IPv4-mapped IPv6 归一为 IPv4;其余用规范压缩形式。
func NormalizeIP(value string) (string, bool) {
	ip := strings.TrimSpace(value)
	if len(ip) >= 2 && ip[0] == '[' && ip[len(ip)-1] == ']' {
		ip = ip[1 : len(ip)-1]
	}
	if ip == "" || strings.Contains(ip, "%") {
		return "", false
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return "", false
	}
	if addr.Is4In6() {
		return addr.Unmap().String(), true
	}
	return addr.String(), true
}

// RemoteHost:从 RemoteAddr(host:port)提取主机部分;提取失败时原样返回。
func RemoteHost(remoteAddress string) string {
	if host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddress)); err == nil {
		return host
	}
	return strings.TrimSpace(remoteAddress)
}

// ParseTrustedProxyIPsNamed:逗号分隔的精确 IP(不支持 CIDR/通配符),归一化并保序去重。
// envName 只用于错误文案;任何非法项都报错。
func ParseTrustedProxyIPsNamed(value, envName string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, 2)
	for _, part := range strings.Split(value, ",") {
		ip, ok := NormalizeIP(part)
		if !ok {
			return nil, errors.New(envName + " 仅支持逗号分隔的 IPv4/IPv6 地址，不支持 CIDR 或通配符")
		}
		if _, exists := seen[ip]; exists {
			continue
		}
		seen[ip] = struct{}{}
		out = append(out, ip)
	}
	return out, nil
}

type TrustedProxies map[string]struct{}

func NewTrustedProxies(ips []string) TrustedProxies {
	set := make(TrustedProxies, len(ips))
	for _, ip := range ips {
		set[ip] = struct{}{}
	}
	return set
}

// ResolveClientIP:
// - TCP 对端不可信/未知:忽略一切转发头;
// - 对端可信时优先 CF-Connecting-IP(由可信代理清洗),其次 XFF 从右往左取首个非可信地址;
// - XFF 存在但含非法项时保留对端;仅当 XFF 头缺失时才看 X-Real-IP。
func ResolveClientIP(remoteAddress string, xffValues []string, xffPresent bool, cfConnectingIP, xRealIP string, trusted TrustedProxies) *string {
	remote, ok := NormalizeIP(RemoteHost(remoteAddress))
	if !ok {
		return nil
	}
	if _, isTrusted := trusted[remote]; !isTrusted {
		return &remote
	}
	if ip, valid := NormalizeIP(cfConnectingIP); valid {
		return &ip
	}
	if xffPresent {
		forwarded := strings.Join(xffValues, ", ")
		clientIP := remote
		parts := strings.Split(forwarded, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip, valid := NormalizeIP(parts[i])
			if !valid {
				return &remote
			}
			clientIP = ip
			if _, stillTrusted := trusted[ip]; !stillTrusted {
				break
			}
		}
		return &clientIP
	}
	if ip, valid := NormalizeIP(xRealIP); valid {
		return &ip
	}
	return &remote
}
