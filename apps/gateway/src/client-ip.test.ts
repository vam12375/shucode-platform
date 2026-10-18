import { describe, expect, it } from "vitest";
import { normalizeIp, parseTrustedProxyIps, resolveClientIp } from "./client-ip.js";

const TRUSTED_PROXY_IPS = new Set(["10.0.0.2", "10.0.0.3", "2001:db8::2"]);

describe("客户端 IP 地址校验", () => {
  it.each([
    [" 203.0.113.7 ", "203.0.113.7"],
    ["::ffff:203.0.113.7", "203.0.113.7"],
    ["[::FFFF:203.0.113.7]", "203.0.113.7"],
    ["0:0:0:0:0:ffff:cb00:7107", "203.0.113.7"],
    ["::ffff:0:203.0.113.7", "::ffff:0:cb00:7107"],
    ["::ffff:0:cb00:7107", "::ffff:0:cb00:7107"],
    ["::ffff:1:cb00:7107", "::ffff:1:cb00:7107"],
    ["2001:0DB8:0:0:0:0:0:7", "2001:db8::7"],
    ["[2001:DB8::7]", "2001:db8::7"],
    ["::1", "::1"],
    ["fd00::7", "fd00::7"],
  ])("%s 归一化为 %s", (input, expected) => {
    expect(normalizeIp(input)).toBe(expected);
    expect(normalizeIp(expected)).toBe(expected);
  });

  it.each([
    undefined, "", "unknown", "not-an-ip", "999.1.1.1", "010.0.0.1", "127.0.0.1.evil",
    "203.0.113.7:443", "[2001:db8::7]:443", "::ffff:not-an-ip", "fe80::1%eth0",
    "203.0.113.7, 203.0.113.8", '"203.0.113.7"',
  ])("拒绝非法地址 %s", (input) => {
    expect(normalizeIp(input)).toBeNull();
  });
});

describe("可信代理配置", () => {
  it.each([undefined, "", "  "])("未配置 %s 时不信任任何代理", (value) => {
    expect(parseTrustedProxyIps(value)).toEqual([]);
  });

  it("逗号分隔精确 IP，归一化并去重", () => {
    expect(parseTrustedProxyIps(" 10.0.0.2, ::ffff:10.0.0.2, [2001:0DB8::2] ")).toEqual([
      "10.0.0.2", "2001:db8::2",
    ]);
  });

  it("保留非 mapped 的 ::ffff: IPv6 代理地址", () => {
    const ips = parseTrustedProxyIps("::ffff:0:203.0.113.7, ::ffff:0:cb00:7107");
    expect(ips).toEqual(["::ffff:0:cb00:7107"]);
    const headers = new Headers({ "x-forwarded-for": "198.51.100.20" });
    expect(resolveClientIp("::ffff:0:203.0.113.7", headers, new Set(ips))).toBe("198.51.100.20");
  });

  it.each(["*", "10.0.0.0/8", "localhost", "10.0.0.2,", ",10.0.0.2", "10.0.0.2,invalid"])(
    "配置 %s 非法时明确失败",
    (value) => {
      expect(() => parseTrustedProxyIps(value)).toThrow("GATEWAY_TRUSTED_PROXY_IPS");
    },
  );
});

describe("客户端 IP 来源信任边界", () => {
  it.each(["198.51.100.20", "::1234", "10.0.0.4", "127.0.0.1", "fd00::1"])(
    "配置了其他可信代理也不采信非可信对端 %s 的头",
    (remote) => {
      const headers = new Headers({ "x-forwarded-for": "203.0.113.7", "x-real-ip": "203.0.113.8" });
      expect(resolveClientIp(remote, headers, TRUSTED_PROXY_IPS)).toBe(remote);
      expect(resolveClientIp(remote, new Headers({ "x-real-ip": "203.0.113.8" }), TRUSTED_PROXY_IPS)).toBe(remote);
    },
  );

  it.each([undefined, "", "invalid", "::ffff:invalid"])("未知或非法对端 %s 不采信头", (remote) => {
    const headers = new Headers({ "x-forwarded-for": "203.0.113.7", "x-real-ip": "203.0.113.8" });
    expect(resolveClientIp(remote, headers, TRUSTED_PROXY_IPS)).toBeNull();
  });

  it.each([
    ["10.0.0.2", "203.0.113.7", "203.0.113.7"],
    ["10.0.0.2", "203.0.113.7, 10.0.0.3", "203.0.113.7"],
    ["10.0.0.2", "192.0.2.99, 203.0.113.7", "203.0.113.7"],
    ["10.0.0.2", "192.0.2.99, 10.0.0.4, 10.0.0.3", "10.0.0.4"],
    ["::ffff:10.0.0.2", "[::ffff:203.0.113.7], ::ffff:10.0.0.3", "203.0.113.7"],
    ["2001:0DB8:0:0:0:0:0:2", "2001:DB8::7, 10.0.0.3", "2001:db8::7"],
    ["10.0.0.2", "10.0.0.3", "10.0.0.3"],
    ["10.0.0.2", "unknown, 203.0.113.7", "203.0.113.7"],
  ])("对端 %s 按代理链 %s 取首个非可信地址 %s", (remote, forwarded, expected) => {
    const headers = new Headers({ "x-forwarded-for": forwarded, "x-real-ip": "192.0.2.98" });
    expect(resolveClientIp(remote, headers, TRUSTED_PROXY_IPS)).toBe(expected);
  });

  it.each(["", "unknown", "999.1.1.1", "203.0.113.7:443", "[::1]:443", "fe80::1%eth0", "203.0.113.7,", "203.0.113.7, unknown, 10.0.0.3"])(
    "可信代理的 XFF %s 非法时保留 TCP 对端，不降级采信 X-Real-IP",
    (forwarded) => {
      const headers = new Headers({ "x-forwarded-for": forwarded, "x-real-ip": "203.0.113.8" });
      expect(resolveClientIp("10.0.0.2", headers, TRUSTED_PROXY_IPS)).toBe("10.0.0.2");
    },
  );

  it("可信代理没有 XFF 时使用经过校验的 X-Real-IP", () => {
    const headers = new Headers({ "x-real-ip": "[::ffff:203.0.113.7]" });
    expect(resolveClientIp("10.0.0.2", headers, TRUSTED_PROXY_IPS)).toBe("203.0.113.7");
  });

  it.each([{}, { "x-real-ip": "unknown" }, { "x-real-ip": "203.0.113.7, 203.0.113.8" }])(
    "可信代理没有有效转发头 %j 时保留对端",
    (headers) => {
      expect(resolveClientIp("10.0.0.2", new Headers(headers), TRUSTED_PROXY_IPS)).toBe("10.0.0.2");
    },
  );
});
