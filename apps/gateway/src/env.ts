import { loadEnvFilesUpwards } from "@shucode-platform/db";
import { parseTrustedProxyIps } from "./client-ip.js";

export interface GatewayEnv {
  port: number;
  host: string;
  /** 允许提供客户端 IP 的可信反代精确地址；省略时不信任任何转发头。 */
  trustedProxyIps?: string[];
  databaseUrl: string | null;
  memoryDb: boolean;
  upstreamBaseUrl: string;
  upstreamApiKey: string;
  anthropicVersion: string;
  /** 上游限时（毫秒）：非流式覆盖整个上游响应；流式覆盖到响应头返回。 */
  upstreamTimeoutMs: number;
  /** 流式闲置超时（毫秒）：连续无字节即中断上游并释放额度预留。 */
  upstreamIdleTimeoutMs: number;
  /**
   * 供应商自动熔断阈值：绑定供应商的请求连续失败 N 次后自动停用（New API 借鉴）；
   * 0 = 关闭自动熔断。人工停用的供应商不受影响。
   */
  providerAutoDisableThreshold: number;
  /** 与 api 共用的凭据密钥：解密 upstream_providers 密文；缺省时绑定供应商的前缀解析不可用。 */
  platformSecretKey: string | null;
}

function isTruthy(value: string | undefined): boolean {
  const normalized = value?.trim().toLowerCase();
  return normalized === "1" || normalized === "true" || normalized === "yes";
}

function readPositiveIntMs(env: NodeJS.ProcessEnv, name: string, fallback: number): number {
  const raw = env[name]?.trim();
  if (!raw) return fallback;
  const parsed = Number.parseInt(raw, 10);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    throw new Error(`${name} 非法: ${env[name]}`);
  }
  return parsed;
}

function readNonNegativeInt(env: NodeJS.ProcessEnv, name: string, fallback: number): number {
  const raw = env[name]?.trim();
  if (!raw) return fallback;
  const parsed = Number.parseInt(raw, 10);
  if (!Number.isFinite(parsed) || parsed < 0) {
    throw new Error(`${name} 非法: ${env[name]}`);
  }
  return parsed;
}

export function loadGatewayEnv(env: NodeJS.ProcessEnv = process.env): GatewayEnv {
  loadEnvFilesUpwards(process.cwd());
  const port = Number.parseInt(env.GATEWAY_PORT?.trim() || env.PORT?.trim() || "8788", 10);
  if (!Number.isFinite(port) || port <= 0 || port > 65535) {
    throw new Error(`GATEWAY_PORT 非法: ${env.GATEWAY_PORT ?? env.PORT}`);
  }
  const databaseUrl = env.DATABASE_URL?.trim() || null;
  const memoryDb = isTruthy(env.MEMORY_DB) || !databaseUrl;
  return {
    port,
    host: env.HOST?.trim() || "0.0.0.0",
    trustedProxyIps: parseTrustedProxyIps(env.GATEWAY_TRUSTED_PROXY_IPS),
    databaseUrl,
    memoryDb,
    upstreamBaseUrl: (env.UPSTREAM_BASE_URL?.trim() || "https://api.anthropic.com").replace(
      /\/+$/u,
      "",
    ),
    upstreamApiKey: env.UPSTREAM_API_KEY?.trim() || "",
    anthropicVersion: env.ANTHROPIC_VERSION?.trim() || "2023-06-01",
    // 超时保底：上游挂死不释放的预留会冻结团队额度，必须有时限兜底。
    upstreamTimeoutMs: readPositiveIntMs(env, "UPSTREAM_TIMEOUT_MS", 120_000),
    upstreamIdleTimeoutMs: readPositiveIntMs(env, "UPSTREAM_IDLE_TIMEOUT_MS", 60_000),
    providerAutoDisableThreshold: readNonNegativeInt(env, "PROVIDER_AUTO_DISABLE_THRESHOLD", 3),
    platformSecretKey: env.PLATFORM_SECRET_KEY?.trim() || null,
  };
}
