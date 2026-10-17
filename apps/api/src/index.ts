import { randomBytes } from "node:crypto";
import { serve } from "@hono/node-server";
import {
  createMemoryStore,
  createPostgresStore,
  createSecretBox,
  runMigrations,
  type PlatformStore,
  type SecretBox,
} from "@shucode-platform/db";
import { createApp } from "./app.js";
import { createConsoleLogger, type AppContext } from "./context.js";
import { loadPlatformEnv } from "./env.js";
import { createConfigEventBus } from "./services/config-events.js";
import { ensureDefaultModels, ensureSeedRelease } from "./services/release-bootstrap.js";

async function main(): Promise<void> {
  const env = loadPlatformEnv();
  const logger = createConsoleLogger("api");

  let store: PlatformStore;
  if (env.memoryDb) {
    logger.warn("MEMORY_DB 模式：数据仅存于进程内，重启丢失（仅用于本地体验）");
    store = await createMemoryStore({ demoSeed: true });
  } else {
    await runMigrations(env.databaseUrl as string, { log: (message) => logger.info(message) });
    store = createPostgresStore(env.databaseUrl as string);
  }

  let secretBox: SecretBox | null = env.platformSecretKey
    ? createSecretBox(env.platformSecretKey)
    : null;
  if (!secretBox) {
    if (!env.memoryDb) {
      throw new Error(
        "生产模式必须配置 PLATFORM_SECRET_KEY（32 字节 base64，生成方式见 .env.example）",
      );
    }
    secretBox = createSecretBox(randomBytes(32).toString("base64"));
    logger.warn("未配置 PLATFORM_SECRET_KEY，已生成临时密钥（重启后旧设备 token 无法恢复）");
  }

  const context: AppContext = {
    store,
    env,
    now: Date.now,
    secretBox,
    configEvents: createConfigEventBus(),
    logger,
  };
  await ensureSeedRelease(context);
  await ensureDefaultModels(context);

  const app = createApp(context);
  const server = serve({ fetch: app.fetch, hostname: env.host, port: env.port }, (info) => {
    logger.info("Mendao 平台 API 已启动", {
      listen: `http://${env.host}:${info.port}`,
      publicApiOrigin: env.publicApiOrigin,
      gatewayBaseUrl: env.gatewayBaseUrl,
      memoryDb: env.memoryDb,
    });
  });

  const shutdown = (signal: string): void => {
    logger.info("收到停止信号，正在退出", { signal });
    server.close(() => {
      void store.dispose().finally(() => process.exit(0));
    });
    // 兜底：连接迟迟不释放时强制退出。
    setTimeout(() => process.exit(0), 3000).unref();
  };
  process.on("SIGINT", () => shutdown("SIGINT"));
  process.on("SIGTERM", () => shutdown("SIGTERM"));
}

main().catch((error) => {
  console.error("[api] 启动失败", error);
  process.exit(1);
});
