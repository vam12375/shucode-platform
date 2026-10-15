import {
  createPostgresStore,
  hashPassword,
  runMigrations,
  type PlatformStore,
} from "@shucode-platform/db";
import { createConsoleLogger, type AppContext } from "./context.js";
import { loadPlatformEnv } from "./env.js";
import { ensureDefaultModels, ensureSeedRelease } from "./services/release-bootstrap.js";

/**
 * 种子脚本：创建管理员（SEED_ADMIN_EMAIL/SEED_ADMIN_PASSWORD）、
 * 载入种子内置配置与默认模型目录。可重复执行（幂等）。
 */
async function main(): Promise<void> {
  const env = loadPlatformEnv();
  if (env.memoryDb) {
    console.log("MEMORY_DB=1 无需种子：本地体验模式已内置演示数据（admin@mendao.chat / mendao-admin）");
    return;
  }
  await runMigrations(env.databaseUrl as string, { log: (message) => console.log(message) });
  const store: PlatformStore = createPostgresStore(env.databaseUrl as string);
  const logger = createConsoleLogger("seed");
  const context: AppContext = { store, env, now: Date.now, secretBox: null, logger };

  const adminEmail = process.env.SEED_ADMIN_EMAIL?.trim() || "admin@mendao.chat";
  const adminPassword = process.env.SEED_ADMIN_PASSWORD?.trim() || "";
  const existing = await store.findUserByEmail(adminEmail);
  if (existing) {
    console.log(`管理员已存在: ${adminEmail}`);
  } else if (!adminPassword) {
    console.log(
      `未创建管理员：请设置 SEED_ADMIN_PASSWORD 后重试（目标邮箱 ${adminEmail}）`,
    );
  } else {
    await store.createUser({
      email: adminEmail,
      passwordHash: await hashPassword(adminPassword),
      displayName: "平台管理员",
      role: "admin",
    });
    console.log(`管理员已创建: ${adminEmail}`);
  }

  await ensureSeedRelease(context);
  await ensureDefaultModels(context);
  await store.dispose();
  console.log("seed done");
}

main().catch((error) => {
  console.error("[seed] 失败", error);
  process.exit(1);
});
