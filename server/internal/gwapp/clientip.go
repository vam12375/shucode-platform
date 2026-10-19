package gwapp

import (
	"github.com/vam12375/shucode-platform/server/internal/netutil"
)

// client-ip.ts 的镜像:解析规则统一放在 internal/netutil,网关不再单独维护一份。
// 仅信任显式配置的可信反代精确地址。

type trustedProxies = netutil.TrustedProxies

func NormalizeIP(value string) (string, bool) {
	return netutil.NormalizeIP(value)
}

// ParseTrustedProxyIPs:逗号分隔的精确 IP(不支持 CIDR/通配符),归一化并保序去重。
func ParseTrustedProxyIPs(value string) ([]string, error) {
	return netutil.ParseTrustedProxyIPsNamed(value, "GATEWAY_TRUSTED_PROXY_IPS")
}

func NewTrustedProxies(ips []string) trustedProxies {
	return netutil.NewTrustedProxies(ips)
}

// ResolveClientIP:镜像 TS resolveClientIp(含全部边界用例):
// - TCP 对端不可信/未知:忽略一切转发头;
// - XFF 从右往左取首个非可信地址;XFF 存在但含非法项时保留对端;
// - 仅当 XFF 头缺失时才看 X-Real-IP(仍需通过校验)。
func ResolveClientIP(remoteAddress string, xffValues []string, xffPresent bool, xRealIP string, trusted trustedProxies) *string {
	return netutil.ResolveClientIP(remoteAddress, xffValues, xffPresent, "", xRealIP, trusted)
}
