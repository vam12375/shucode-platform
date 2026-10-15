import { serve } from "@hono/node-server";
import { createMemoryStore, createPostgresStore, type PlatformStore } from "@shucode-platform/db";
import { createGatewayApp, type GatewayLogger } from "./app.js";
import { loadGatewayEnv } from "./env.js";

const logger: GatewayLogger = {
  info: (message, detail) => console.log("[gateway]", message, detail ?? ""),
  warn: (message, detail) => console.warn("[gateway]", message, detail ?? ""),
  error: (message, detail) => console.error("[gateway]", message, detail ?? ""),
};

async function main(): Promise<void> {
  const env = loadGatewayEnv();
  let store: PlatformStore;
  if (env.memoryDb) {
    logger.warn("MEMORY_DB 模式：无共享数据库，仅用于本地冒烟（无可用凭据）");
    store = await createMemoryStore();
  } else {
    store = createPostgresStore(env.databaseUrl as string);
  }
  if (!env.upstreamApiKey) {
    logger.warn("未配置 UPSTREAM_API_KEY，转发到上游会返回 401");
  }

  const app = createGatewayApp({
    store,
    env,
    now: Date.now,
    fetchUpstream: (input, init) => fetch(input, init),
    logger,
  });
  const server = serve({ fetch: app.fetch, hostname: env.host, port: env.port }, (info) => {
    logger.info("Mendao 模型网关已启动", {
      listen: `http://${env.host}:${info.port}`,
      upstream: env.upstreamBaseUrl,
      memoryDb: env.memoryDb,
    });
  });

  const shutdown = (signal: string): void => {
    logger.info("收到停止信号，正在退出", { signal });
    server.close(() => {
      void store.dispose().finally(() => process.exit(0));
    });
    setTimeout(() => process.exit(0), 3000).unref();
  };
  process.on("SIGINT", () => shutdown("SIGINT"));
  process.on("SIGTERM", () => shutdown("SIGTERM"));
}

main().catch((error) => {
  console.error("[gateway] 启动失败", error);
  process.exit(1);
});
