import { isIP, SocketAddress } from "node:net";

/** 只接受 IP 字面量，并统一 IPv6 与 IPv4-mapped IPv6 的文本表示。 */
export function normalizeIp(value: string | undefined): string | null {
  const ip = value?.trim().replace(/^\[(.+)\]$/u, "$1");
  if (!ip || ip.includes("%")) return null;
  const version = isIP(ip);
  if (version === 0) return null;
  if (version === 4) return ip;
  const canonical = new SocketAddress({ address: ip, family: "ipv6" }).address;
  // 普通 IPv6 也可能以 ::ffff: 开头，仅其余部分是完整 IPv4 时才转换 mapped 地址。
  const mapped = canonical.startsWith("::ffff:") ? canonical.slice(7) : "";
  return isIP(mapped) === 4 ? mapped : canonical;
}

export function parseTrustedProxyIps(value: string | undefined): string[] {
  if (!value?.trim()) return [];
  const ips = value.split(",").map((part) => {
    const ip = normalizeIp(part);
    if (!ip) {
      throw new Error("GATEWAY_TRUSTED_PROXY_IPS 仅支持逗号分隔的 IPv4/IPv6 地址，不支持 CIDR 或通配符");
    }
    return ip;
  });
  return [...new Set(ips)];
}

export function resolveClientIp(
  remoteAddress: string | undefined,
  headers: Headers,
  trustedProxyIps: ReadonlySet<string>,
): string | null {
  const remote = normalizeIp(remoteAddress);
  // 原私网正则的空分支匹配所有地址；且私网/未知对端不等于可信反代，必须显式授权。
  if (!remote || !trustedProxyIps.has(remote)) return remote;

  const forwarded = headers.get("x-forwarded-for");
  if (forwarded !== null) {
    let clientIp = remote;
    // 代理链从 TCP 对端向左检查，遇到首个非可信地址即停止，避免采信客户端预置的最左项。
    for (const part of forwarded.split(",").reverse()) {
      const ip = normalizeIp(part);
      if (!ip) return remote;
      clientIp = ip;
      if (!trustedProxyIps.has(ip)) break;
    }
    return clientIp;
  }
  // XFF 存在但非法时不再回退到另一个头；X-Real-IP 仅供可信反代在未提供 XFF 时使用。
  return normalizeIp(headers.get("x-real-ip") ?? undefined) ?? remote;
}
