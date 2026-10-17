import { readFile } from "node:fs/promises";
import { extname, join, normalize, resolve } from "node:path";
import { cors } from "hono/cors";
import { Hono } from "hono";
import type { AppContext } from "./context.js";
import { registerClientConfigRoutes } from "./routes/client-configs.js";
import { registerClientEventRoutes } from "./routes/client-events.js";
import { registerConsoleRoutes } from "./routes/console.js";
import { registerPlatformAuthRoutes } from "./routes/platform-auth.js";
import { registerReleaseRoutes } from "./routes/releases.js";
import { registerUserConfigRoutes } from "./routes/user-config.js";

const STATIC_MIME: Record<string, string> = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".ico": "image/x-icon",
  ".woff2": "font/woff2",
  ".map": "application/json; charset=utf-8",
};

/** 控制台 SPA 静态托管（/console/*）；必须晚于 /console/api/* 注册。 */
function registerConsoleStatic(app: Hono, distDir: string): void {
  const root = resolve(distDir);
  app.get("/console", (c) => c.redirect("/console/", 302));
  app.get("/console/*", async (c) => {
    const rawPath = new URL(c.req.url).pathname.replace(/^\/console\/?/u, "");
    const relative = normalize(rawPath).replace(/^([./\\])+/u, "");
    if (relative.includes("..")) {
      return c.notFound();
    }
    const candidate = join(root, relative || "index.html");
    try {
      const content = await readFile(candidate);
      return c.body(content, 200, {
        "Content-Type": STATIC_MIME[extname(candidate)] ?? "application/octet-stream",
        "Cache-Control": candidate.endsWith("index.html")
          ? "no-cache"
          : "public, max-age=31536000, immutable",
      });
    } catch {
      // SPA fallback：非静态资源路径回落到 index.html。
      try {
        const index = await readFile(join(root, "index.html"), "utf8");
        return c.html(index);
      } catch {
        return c.notFound();
      }
    }
  });
}

export function createApp(context: AppContext): Hono {
  const app = new Hono();

  app.onError((error, c) => {
    context.logger.error("请求处理失败", { path: c.req.path, error });
    return c.json({ code: 500, msg: "服务器内部错误" }, 500);
  });

  app.get("/healthz", (c) => c.json({ ok: true, memoryDb: context.env.memoryDb }));

  // 独立部署 Console（console.mendao.chat）时允许携带凭据的跨源访问。
  if (context.env.publicConsoleOrigin) {
    app.use(
      "/console/api/*",
      cors({
        origin: context.env.publicConsoleOrigin,
        credentials: true,
      }),
    );
  }

  registerClientConfigRoutes(app, context);
  registerReleaseRoutes(app, context);
  registerPlatformAuthRoutes(app, context);
  registerUserConfigRoutes(app, context);
  registerClientEventRoutes(app, context);
  registerConsoleRoutes(app, context);

  if (context.env.consoleDistDir) {
    registerConsoleStatic(app, context.env.consoleDistDir);
  }

  return app;
}
