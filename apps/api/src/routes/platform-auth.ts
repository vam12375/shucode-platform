import {
  buildPlatformDeepLinkCallbackUrl,
  platformAuthInitRequestSchema,
} from "@shucode-platform/contracts/platform-auth";
import {
  deviceTokenHint,
  generateDeviceToken,
  hashToken,
  randomId,
  verifyPassword,
} from "@shucode-platform/db";
import type { Context, Hono } from "hono";
import type { AppContext } from "../context.js";
import { isoNow } from "../context.js";
import {
  renderAuthorizePage,
  renderAuthorizedPage,
  renderLoginPage,
  renderMessagePage,
} from "../html.js";
import { createRateLimiter } from "../services/rate-limit.js";
import { loginWithSession, readSessionUser } from "../services/sessions.js";

const FLOW_TTL_MS = 10 * 60 * 1000;
const POLL_INTERVAL_SEC = 3;

function readPollToken(c: Context): string | null {
  const header = c.req.header("authorization")?.trim();
  if (!header?.toLowerCase().startsWith("bearer ")) return null;
  const token = header.slice(7).trim();
  return token || null;
}

function resolveClientIp(c: Context): string {
  return (
    c.req.header("cf-connecting-ip")?.trim() ||
    c.req.header("x-forwarded-for")?.split(",")[0]?.trim() ||
    "unknown"
  );
}

function readFormString(form: Record<string, string | File>, key: string): string {
  const value = form[key];
  return typeof value === "string" ? value : "";
}

/**
 * 平台设备登录：
 * 客户端 POST init（Bearer 轮询凭据）→ 浏览器打开 authorize_url 完成登录与授权 →
 * 客户端轮询 poll 领取设备 token。浏览器回跳 zcode://platform/callback 只负责唤醒窗口，
 * 凭据不下发到 URL 中；并发轮询用 consumeLoginFlow 的条件消费保证只签发一次。
 */
export function registerPlatformAuthRoutes(app: Hono, context: AppContext): void {
  const loginLimiter = createRateLimiter({ windowMs: 5 * 60 * 1000, max: 10 });

  // ---- 客户端 API ----

  app.post("/api/v1/platform/auth/init", async (c) => {
    const pollToken = readPollToken(c);
    if (!pollToken || pollToken.length < 32) {
      return c.json({ code: 400, msg: "缺少轮询凭据" }, 400);
    }
    let body: unknown = {};
    try {
      body = await c.req.json();
    } catch {
      body = {};
    }
    const parsed = platformAuthInitRequestSchema.safeParse(body ?? {});
    if (!parsed.success) {
      return c.json({ code: 400, msg: "请求参数不合法" }, 400);
    }
    const now = context.now();
    const id = randomId();
    await context.store.createLoginFlow({
      id,
      pollTokenHash: hashToken(pollToken),
      status: "pending",
      userId: null,
      deviceName: parsed.data.device_name ?? "未知设备",
      devicePlatform: parsed.data.platform ?? null,
      createdAt: new Date(now).toISOString(),
      expiresAt: new Date(now + FLOW_TTL_MS).toISOString(),
      authorizedAt: null,
      consumedAt: null,
      tokenId: null,
      lastPolledAt: null,
    });
    const authorizeUrl = `${context.env.publicAppOrigin}/app/platform/login?flow_id=${encodeURIComponent(id)}`;
    return c.json({
      code: 0,
      data: {
        flow_id: id,
        authorize_url: authorizeUrl,
        expires_at: Math.floor((now + FLOW_TTL_MS) / 1000),
        poll_interval_sec: POLL_INTERVAL_SEC,
      },
    });
  });

  app.get("/api/v1/platform/auth/poll/:flowId", async (c) => {
    const pollToken = readPollToken(c);
    if (!pollToken) {
      return c.json({ code: 400, msg: "缺少轮询凭据" }, 400);
    }
    const flowId = c.req.param("flowId");
    const flow = await context.store.getLoginFlow(flowId);
    if (!flow) {
      return c.json({ code: 404, msg: "登录流程不存在" }, 404);
    }
    if (hashToken(pollToken) !== flow.pollTokenHash) {
      return c.json({ code: 403, msg: "轮询凭据不匹配" }, 403);
    }
    const now = context.now();
    if (
      (flow.status === "pending" || flow.status === "authorized") &&
      Date.parse(flow.expiresAt) <= now
    ) {
      await context.store.updateLoginFlow(flowId, { status: "expired" });
      return c.json({ code: 0, data: { status: "expired" } });
    }
    await context.store.updateLoginFlow(flowId, { lastPolledAt: isoNow(context) });

    if (flow.status === "pending") {
      return c.json({ code: 0, data: { status: "pending" } });
    }
    if (flow.status === "denied") {
      return c.json({ code: 0, data: { status: "denied" } });
    }
    if (flow.status === "expired") {
      return c.json({ code: 0, data: { status: "expired" } });
    }
    if (flow.status !== "authorized" || !flow.userId) {
      return c.json({ code: 0, data: { status: "consumed" } });
    }
    if (!context.secretBox) {
      return c.json({ code: 503, msg: "平台未配置 PLATFORM_SECRET_KEY，无法签发凭据" }, 503);
    }

    const user = await context.store.findUserById(flow.userId);
    if (!user || user.disabledAt) {
      return c.json({ code: 403, msg: "账号不可用" }, 403);
    }

    // 条件消费：并发轮询只有一个赢家签发 token，其余返回 consumed。
    const claimed = await context.store.consumeLoginFlow(flowId, isoNow(context));
    if (!claimed) {
      return c.json({ code: 0, data: { status: "consumed" } });
    }
    const token = generateDeviceToken();
    const tokenId = randomId();
    await context.store.createDeviceToken({
      id: tokenId,
      userId: user.id,
      name: flow.deviceName,
      platform: flow.devicePlatform,
      tokenHash: hashToken(token),
      tokenCiphertext: context.secretBox.encrypt(token),
      tokenHint: deviceTokenHint(token),
      createdAt: isoNow(context),
      lastUsedAt: null,
      revokedAt: null,
    });
    await context.store.updateLoginFlow(flowId, { tokenId });
    const teams = await context.store.listTeamsForUser(user.id);
    return c.json({
      code: 0,
      data: {
        status: "authorized",
        credentials: { token, token_id: tokenId, token_hint: deviceTokenHint(token) },
        user: { id: user.id, email: user.email, display_name: user.displayName },
        teams: teams.map((entry) => ({
          id: entry.team.id,
          name: entry.team.name,
          role: entry.role,
        })),
      },
    });
  });

  // ---- 浏览器页面 ----

  app.get("/app/platform/login", async (c) => {
    const flowId = c.req.query("flow_id")?.trim() ?? "";
    const flow = flowId ? await context.store.getLoginFlow(flowId) : null;
    if (!flow) {
      return c.html(
        renderMessagePage({
          title: "链接无效",
          message: "登录链接无效，请回到应用重新发起登录。",
          tone: "error",
        }),
        400,
      );
    }
    if (flow.status === "denied") {
      return c.html(
        renderMessagePage({ title: "已取消", message: "本次登录已取消，可关闭本页面。" }),
      );
    }
    if (flow.status === "consumed" || flow.status === "authorized") {
      return c.redirect(`/app/platform/authorized?flow_id=${encodeURIComponent(flowId)}`, 303);
    }
    if (Date.parse(flow.expiresAt) <= context.now()) {
      if (flow.status === "pending") {
        await context.store.updateLoginFlow(flowId, { status: "expired" });
      }
      return c.html(
        renderMessagePage({
          title: "链接已过期",
          message: "登录链接已过期，请回到应用重新发起登录。",
          tone: "error",
        }),
        410,
      );
    }
    const user = await readSessionUser(context, c);
    if (!user) {
      return c.html(renderLoginPage({ flowId, deviceName: flow.deviceName }));
    }
    return c.html(
      renderAuthorizePage({
        flowId,
        deviceName: flow.deviceName,
        devicePlatform: flow.devicePlatform,
        email: user.email,
      }),
    );
  });

  app.post("/app/platform/login", async (c) => {
    const form = await c.req.parseBody();
    const flowId = readFormString(form, "flow_id");
    const email = readFormString(form, "email").trim();
    const password = readFormString(form, "password");
    const flow = flowId ? await context.store.getLoginFlow(flowId) : null;
    if (!flow || flow.status !== "pending" || Date.parse(flow.expiresAt) <= context.now()) {
      return c.html(
        renderMessagePage({
          title: "链接无效",
          message: "登录链接无效或已过期，请回到应用重新发起登录。",
          tone: "error",
        }),
        400,
      );
    }
    if (!loginLimiter(`${resolveClientIp(c)}:${email.toLowerCase()}`)) {
      return c.html(
        renderLoginPage({
          flowId,
          deviceName: flow.deviceName,
          error: "尝试次数过多，请稍后再试。",
        }),
        429,
      );
    }
    const user = email ? await context.store.findUserByEmail(email) : null;
    const passwordOk = user && !user.disabledAt
      ? await verifyPassword(password, user.passwordHash)
      : false;
    if (!user || !passwordOk) {
      // 统一文案，不区分「账号不存在」与「密码错误」，避免账号枚举。
      return c.html(
        renderLoginPage({
          flowId,
          deviceName: flow.deviceName,
          error: "邮箱或密码不正确。",
          email,
        }),
        401,
      );
    }
    await loginWithSession(context, c, user.id);
    return c.redirect(`/app/platform/login?flow_id=${encodeURIComponent(flowId)}`, 303);
  });

  app.post("/app/platform/authorize", async (c) => {
    const form = await c.req.parseBody();
    const flowId = readFormString(form, "flow_id");
    const decision = readFormString(form, "decision");
    const user = await readSessionUser(context, c);
    if (!user) {
      return c.redirect(
        `/app/platform/login?flow_id=${encodeURIComponent(flowId)}`,
        303,
      );
    }
    const flow = flowId ? await context.store.getLoginFlow(flowId) : null;
    if (!flow) {
      return c.html(
        renderMessagePage({
          title: "链接无效",
          message: "登录链接无效，请回到应用重新发起登录。",
          tone: "error",
        }),
        400,
      );
    }
    if (flow.status === "authorized" || flow.status === "consumed") {
      return c.redirect(`/app/platform/authorized?flow_id=${encodeURIComponent(flowId)}`, 303);
    }
    if (flow.status !== "pending" || Date.parse(flow.expiresAt) <= context.now()) {
      return c.html(
        renderMessagePage({
          title: "链接已过期",
          message: "登录链接已过期，请回到应用重新发起登录。",
          tone: "error",
        }),
        410,
      );
    }
    if (decision === "approve") {
      await context.store.updateLoginFlow(flowId, {
        status: "authorized",
        userId: user.id,
        authorizedAt: isoNow(context),
      });
      return c.redirect(`/app/platform/authorized?flow_id=${encodeURIComponent(flowId)}`, 303);
    }
    await context.store.updateLoginFlow(flowId, { status: "denied" });
    return c.html(renderMessagePage({ title: "已取消", message: "已取消本次授权，可关闭本页面。" }));
  });

  app.get("/app/platform/authorized", async (c) => {
    const flowId = c.req.query("flow_id")?.trim() ?? "";
    const flow = flowId ? await context.store.getLoginFlow(flowId) : null;
    if (!flow || (flow.status !== "authorized" && flow.status !== "consumed")) {
      return c.html(
        renderMessagePage({
          title: "状态未知",
          message: "授权状态未知，请回到应用查看登录结果。",
        }),
      );
    }
    return c.html(
      renderAuthorizedPage({ flowId, deepLink: buildPlatformDeepLinkCallbackUrl(flowId) }),
    );
  });
}
