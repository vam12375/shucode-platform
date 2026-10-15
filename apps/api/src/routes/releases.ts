import type { Hono } from "hono";
import type { AppContext } from "../context.js";

/**
 * Release 与更新相关端点：
 * - /api/v1/releases/builtin/<rev>.json：内置配置 JSON（客户端要求 https、禁重定向）。
 * - /api/v1/releases/electron/manifest：桌面自动更新清单（YAML）。
 *   当前平台未托管安装包，返回 version: 0.0.0 表示“无需更新”；
 *   任何低于本地版本的 manifest 都会被 electron-updater 判定为无更新。
 */
export function registerReleaseRoutes(app: Hono, context: AppContext): void {
  app.get("/api/v1/releases/builtin/latest.json", async (c) => {
    const active = await context.store.getActiveRelease();
    if (!active) {
      return c.json({ error: "no active release" }, 404);
    }
    c.header("Cache-Control", "public, max-age=60");
    return c.json(active.content);
  });

  app.get("/api/v1/releases/builtin/:revision", async (c) => {
    // Hono 不会按后缀拆分路径段，param 里包含 ".json"，这里剥掉再解析。
    const raw = (c.req.param("revision") ?? "").replace(/\.json$/u, "");
    const revision = Number.parseInt(raw, 10);
    if (!Number.isFinite(revision)) {
      return c.json({ error: "invalid revision" }, 400);
    }
    const release = await context.store.getRelease(revision);
    if (!release) {
      return c.json({ error: "release not found" }, 404);
    }
    c.header("Cache-Control", "public, max-age=300");
    return c.json(release.content);
  });

  app.get("/api/v1/releases/electron/manifest", (c) => {
    // electron-updater 会按 YAML 解析；缺少 files 的 0.0.0 清单等价于“没有可用更新”。
    c.header("Cache-Control", "no-store");
    return c.text("version: 0.0.0\n", 200, { "Content-Type": "text/yaml; charset=utf-8" });
  });
}
