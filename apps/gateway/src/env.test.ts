import { describe, expect, it, vi } from "vitest";
import { loadGatewayEnv } from "./env.js";

vi.mock("@shucode-platform/db", () => ({ loadEnvFilesUpwards: () => {} }));

describe("网关可信代理环境配置", () => {
  it.each([undefined, "", "  "])("GATEWAY_TRUSTED_PROXY_IPS=%s 默认禁用转发头", (value) => {
    expect(loadGatewayEnv({ GATEWAY_TRUSTED_PROXY_IPS: value }).trustedProxyIps).toEqual([]);
  });

  it("读取可信代理并归一化 IPv4-mapped 和 IPv6", () => {
    const env = loadGatewayEnv({ GATEWAY_TRUSTED_PROXY_IPS: "::ffff:10.0.0.2, 2001:0DB8::2" });
    expect(env.trustedProxyIps).toEqual(["10.0.0.2", "2001:db8::2"]);
  });

  it("保留非 mapped 的 ::ffff: IPv6 代理配置", () => {
    const env = loadGatewayEnv({ GATEWAY_TRUSTED_PROXY_IPS: "::ffff:0:203.0.113.7" });
    expect(env.trustedProxyIps).toEqual(["::ffff:0:cb00:7107"]);
  });

  it("无效配置启动时明确报错，不扩大信任范围", () => {
    expect(() => loadGatewayEnv({ GATEWAY_TRUSTED_PROXY_IPS: "10.0.0.0/8" })).toThrow(
      "GATEWAY_TRUSTED_PROXY_IPS",
    );
  });
});
