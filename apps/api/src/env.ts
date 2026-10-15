import { loadEnvFilesUpwards } from "@shucode-platform/db";

export interface PlatformEnv {
  port: number;
  host: string;
  databaseUrl: string | null;
  memoryDb: boolean;
  /** 客户端 SHUCODE_BASE_URL 的目标。 */
  publicApiOrigin: string;
  /** 登录页/中转页所在 origin；默认与 API 同源。 */
  publicAppOrigin: string;
  platformSecretKey: string | null;
  cookieDomain: string | null;
  secureCookies: boolean;
  gatewayBaseUrl: string;
  /** Release JSON 下载基址；客户端要求 https。 */
  releaseBaseUrl: string;
  consoleDistDir: string | null;
  publicConsoleOrigin: string | null;
}

function isTruthy(value: string | undefined): boolean {
  const normalized = value?.trim().toLowerCase();
  return normalized === "1" || normalized === "true" || normalized === "yes";
}

function normalizeOrigin(value: string): string {
  return new URL(value).origin;
}

export function loadPlatformEnv(env: NodeJS.ProcessEnv = process.env): PlatformEnv {
  loadEnvFilesUpwards(process.cwd());

  const port = Number.parseInt(env.PORT?.trim() || "8787", 10);
  if (!Number.isFinite(port) || port <= 0 || port > 65535) {
    throw new Error(`PORT 非法: ${env.PORT}`);
  }
  const memoryDb = isTruthy(env.MEMORY_DB);
  const databaseUrl = env.DATABASE_URL?.trim() || null;
  if (!memoryDb && !databaseUrl) {
    throw new Error("必须提供 DATABASE_URL，或设置 MEMORY_DB=1 进入本地体验模式");
  }

  const publicApiOrigin = normalizeOrigin(
    env.PUBLIC_API_ORIGIN?.trim() || `http://localhost:${port}`,
  );
  const publicAppOrigin = normalizeOrigin(env.PUBLIC_APP_ORIGIN?.trim() || publicApiOrigin);

  return {
    port,
    host: env.HOST?.trim() || "0.0.0.0",
    databaseUrl,
    memoryDb: memoryDb || !databaseUrl,
    publicApiOrigin,
    publicAppOrigin,
    platformSecretKey: env.PLATFORM_SECRET_KEY?.trim() || null,
    cookieDomain: env.COOKIE_DOMAIN?.trim() || null,
    secureCookies: publicApiOrigin.startsWith("https:"),
    gatewayBaseUrl: (env.GATEWAY_BASE_URL?.trim() || "https://gw.mendao.chat").replace(/\/+$/u, ""),
    releaseBaseUrl: (env.RELEASE_BASE_URL?.trim() || publicApiOrigin).replace(/\/+$/u, ""),
    consoleDistDir: env.CONSOLE_DIST_DIR?.trim() || null,
    publicConsoleOrigin: env.PUBLIC_CONSOLE_ORIGIN?.trim() || null,
  };
}
