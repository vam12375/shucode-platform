import { loadEnvFilesUpwards } from "@shucode-platform/db";

export interface GatewayEnv {
  port: number;
  host: string;
  databaseUrl: string | null;
  memoryDb: boolean;
  upstreamBaseUrl: string;
  upstreamApiKey: string;
  anthropicVersion: string;
}

function isTruthy(value: string | undefined): boolean {
  const normalized = value?.trim().toLowerCase();
  return normalized === "1" || normalized === "true" || normalized === "yes";
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
    databaseUrl,
    memoryDb,
    upstreamBaseUrl: (env.UPSTREAM_BASE_URL?.trim() || "https://api.anthropic.com").replace(
      /\/+$/u,
      "",
    ),
    upstreamApiKey: env.UPSTREAM_API_KEY?.trim() || "",
    anthropicVersion: env.ANTHROPIC_VERSION?.trim() || "2023-06-01",
  };
}
