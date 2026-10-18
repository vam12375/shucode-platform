package gwapp

import (
	"errors"
	"net/netip"
	"strings"
)

// client-ip.ts 的镜像:仅接受 IP 字面量;IPv4-mapped IPv6 归一为 IPv4;
// 其余 IPv6 用规范压缩形式。只信任显式配置的可信反代精确地址。

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

// ParseTrustedProxyIPs:逗号分隔的精确 IP(不支持 CIDR/通配符),归一化并保序去重;
// 任何非法项都报错(错误文案与 TS 一致)。
func ParseTrustedProxyIPs(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, 2)
	for _, part := range strings.Split(value, ",") {
		ip, ok := NormalizeIP(part)
		if !ok {
			return nil, errors.New("GATEWAY_TRUSTED_PROXY_IPS 仅支持逗号分隔的 IPv4/IPv6 地址，不支持 CIDR 或通配符")
		}
		if _, exists := seen[ip]; exists {
			continue
		}
		seen[ip] = struct{}{}
		out = append(out, ip)
	}
	return out, nil
}

type trustedProxies map[string]struct{}

func NewTrustedProxies(ips []string) trustedProxies {
	set := make(trustedProxies, len(ips))
	for _, ip := range ips {
		set[ip] = struct{}{}
	}
	return set
}

// ResolveClientIP:镜像 TS resolveClientIp(含全部边界用例):
// - TCP 对端不可信/未知:忽略一切转发头;
// - XFF 从右往左取首个非可信地址;XFF 存在但含非法项时保留对端;
// - 仅当 XFF 头缺失时才看 X-Real-IP(仍需通过校验)。
func ResolveClientIP(remoteAddress string, xffValues []string, xffPresent bool, xRealIP string, trusted trustedProxies) *string {
	remote, ok := NormalizeIP(remoteAddress)
	if !ok {
		return nil
	}
	if _, isTrusted := trusted[remote]; !isTrusted {
		return &remote
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
