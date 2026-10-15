import { PLATFORM_CONFIG_KEYS } from "@shucode-platform/contracts/client-configs";
import { parseBuiltinReleaseLoose } from "@shucode-platform/contracts/release";
import { hashPassword, verifyPassword, type UserRecord } from "@shucode-platform/db";
import type { Context, Hono } from "hono";
import { z } from "zod";
import type { AppContext } from "../context.js";
import { createRateLimiter } from "../services/rate-limit.js";
import { loginWithSession, logoutSession, readSessionUser } from "../services/sessions.js";

const creditString = z
  .union([z.number(), z.string()])
  .transform((value) => {
    const numeric = typeof value === "number" ? value : Number.parseFloat(value);
    if (!Number.isFinite(numeric) || numeric < 0) {
      throw new Error("额度必须是非负数值");
    }
    return numeric.toFixed(6);
  });

const priceString = z
  .union([z.number(), z.string()])
  .transform((value) => {
    const numeric = typeof value === "number" ? value : Number.parseFloat(value);
    if (!Number.isFinite(numeric) || numeric < 0) {
      throw new Error("价格必须是非负数值");
    }
    return numeric.toFixed(6);
  });

function publicUser(user: UserRecord) {
  return {
    id: user.id,
    email: user.email,
    displayName: user.displayName,
    role: user.role,
    disabledAt: user.disabledAt,
    createdAt: user.createdAt,
  };
}

async function readJsonBody(c: Context): Promise<unknown> {
  try {
    return await c.req.json();
  } catch {
    return null;
  }
}

/** 管理控制台 API：全部位于 /console/api/*，要求 admin 角色的会话。 */
export function registerConsoleRoutes(app: Hono, context: AppContext): void {
  const loginLimiter = createRateLimiter({ windowMs: 5 * 60 * 1000, max: 10 });

  async function requireAdmin(c: Context): Promise<UserRecord | null> {
    const user = await readSessionUser(context, c);
    return user && user.role === "admin" ? user : null;
  }

  // ---- auth ----

  app.post("/console/api/auth/login", async (c) => {
    const parsed = z
      .object({ email: z.string().trim().min(3), password: z.string().min(1) })
      .safeParse(await readJsonBody(c));
    if (!parsed.success) return c.json({ code: 400, msg: "请求参数不合法" }, 400);
    const { email, password } = parsed.data;
    const ip = c.req.header("x-forwarded-for")?.split(",")[0]?.trim() ?? "local";
    if (!loginLimiter(`${ip}:${email.toLowerCase()}`)) {
      return c.json({ code: 429, msg: "尝试次数过多，请稍后再试" }, 429);
    }
    const user = await context.store.findUserByEmail(email);
    if (!user || user.disabledAt || user.role !== "admin") {
      return c.json({ code: 401, msg: "邮箱或密码不正确" }, 401);
    }
    if (!(await verifyPassword(password, user.passwordHash))) {
      return c.json({ code: 401, msg: "邮箱或密码不正确" }, 401);
    }
    await loginWithSession(context, c, user.id);
    return c.json({ code: 0, data: { user: publicUser(user) } });
  });

  app.post("/console/api/auth/logout", async (c) => {
    await logoutSession(context, c);
    return c.json({ code: 0 });
  });

  app.get("/console/api/auth/me", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    return c.json({ code: 0, data: { user: publicUser(admin) } });
  });

  // ---- teams ----

  app.get("/console/api/teams", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const teams = await context.store.listTeams();
    return c.json({
      code: 0,
      data: {
        teams: teams.map((entry) => ({ ...entry.team, memberCount: entry.memberCount })),
      },
    });
  });

  app.post("/console/api/teams", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const parsed = z
      .object({
        name: z.string().trim().min(1).max(80),
        quotaCredits: creditString.optional(),
        unlimited: z.boolean().optional(),
      })
      .safeParse(await readJsonBody(c));
    if (!parsed.success) return c.json({ code: 400, msg: "请求参数不合法" }, 400);
    try {
      const team = await context.store.createTeam({
        name: parsed.data.name,
        quotaCredits: parsed.data.quotaCredits ?? "0.000000",
        unlimited: parsed.data.unlimited ?? false,
      });
      return c.json({ code: 0, data: { team } });
    } catch (error) {
      return c.json(
        { code: 409, msg: error instanceof Error ? error.message : "团队创建失败" },
        409,
      );
    }
  });

  app.get("/console/api/teams/:id", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const teamId = c.req.param("id");
    const team = await context.store.getTeam(teamId);
    if (!team) return c.json({ code: 404, msg: "团队不存在" }, 404);
    const [members, aggregates, recentUsage] = await Promise.all([
      context.store.listMembers(teamId),
      context.store.aggregateUsage({ teamId }),
      context.store.listUsage({ teamId, limit: 50 }),
    ]);
    return c.json({
      code: 0,
      data: {
        team,
        members: members.map((member) => ({
          user: publicUser(member.user),
          role: member.role,
          joinedAt: member.createdAt,
        })),
        aggregates,
        recentUsage,
      },
    });
  });

  app.patch("/console/api/teams/:id", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const parsed = z
      .object({
        name: z.string().trim().min(1).max(80).optional(),
        quotaCredits: creditString.optional(),
        unlimited: z.boolean().optional(),
      })
      .safeParse(await readJsonBody(c));
    if (!parsed.success) return c.json({ code: 400, msg: "请求参数不合法" }, 400);
    const team = await context.store.updateTeam(c.req.param("id"), {
      ...(parsed.data.name !== undefined ? { name: parsed.data.name } : {}),
      ...(parsed.data.quotaCredits !== undefined
        ? { quotaCredits: parsed.data.quotaCredits }
        : {}),
      ...(parsed.data.unlimited !== undefined ? { unlimited: parsed.data.unlimited } : {}),
    });
    if (!team) return c.json({ code: 404, msg: "团队不存在" }, 404);
    return c.json({ code: 0, data: { team } });
  });

  app.post("/console/api/teams/:id/members", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const parsed = z
      .object({
        email: z.string().trim().min(3),
        role: z.enum(["owner", "member"]).optional(),
      })
      .safeParse(await readJsonBody(c));
    if (!parsed.success) return c.json({ code: 400, msg: "请求参数不合法" }, 400);
    const teamId = c.req.param("id");
    const user = await context.store.findUserByEmail(parsed.data.email);
    if (!user) {
      return c.json({ code: 404, msg: `用户不存在：${parsed.data.email}（请先在用户页创建）` }, 404);
    }
    await context.store.addMember(teamId, user.id, parsed.data.role ?? "member");
    return c.json({ code: 0, data: { user: publicUser(user) } });
  });

  app.delete("/console/api/teams/:id/members/:userId", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const removed = await context.store.removeMember(c.req.param("id"), c.req.param("userId"));
    if (!removed) return c.json({ code: 404, msg: "成员不存在" }, 404);
    return c.json({ code: 0 });
  });

  // ---- users ----

  app.get("/console/api/users", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const users = await context.store.listUsers();
    return c.json({ code: 0, data: { users: users.map(publicUser) } });
  });

  app.post("/console/api/users", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const parsed = z
      .object({
        email: z.string().trim().email(),
        password: z.string().min(8).max(200),
        displayName: z.string().trim().min(1).max(80),
        role: z.enum(["admin", "member"]).optional(),
      })
      .safeParse(await readJsonBody(c));
    if (!parsed.success) return c.json({ code: 400, msg: "请求参数不合法（密码至少 8 位）" }, 400);
    try {
      const user = await context.store.createUser({
        email: parsed.data.email,
        passwordHash: await hashPassword(parsed.data.password),
        displayName: parsed.data.displayName,
        role: parsed.data.role ?? "member",
      });
      return c.json({ code: 0, data: { user: publicUser(user) } });
    } catch (error) {
      return c.json(
        { code: 409, msg: error instanceof Error ? error.message : "用户创建失败" },
        409,
      );
    }
  });

  // ---- releases ----

  app.get("/console/api/releases", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const releases = await context.store.listReleases();
    return c.json({
      code: 0,
      data: {
        releases: releases.map((release) => ({
          revision: release.revision,
          publishedAt: release.publishedAt,
          isActive: release.isActive,
        })),
      },
    });
  });

  app.get("/console/api/releases/:revision", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const revision = Number.parseInt(c.req.param("revision"), 10);
    const release = Number.isFinite(revision)
      ? await context.store.getRelease(revision)
      : null;
    if (!release) return c.json({ code: 404, msg: "Release 不存在" }, 404);
    return c.json({ code: 0, data: { revision: release.revision, content: release.content } });
  });

  app.post("/console/api/releases", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const parsed = z.object({ content: z.unknown() }).safeParse(await readJsonBody(c));
    if (!parsed.success) return c.json({ code: 400, msg: "请求参数不合法" }, 400);
    let candidate = parsed.data.content;
    if (typeof candidate === "string") {
      try {
        candidate = JSON.parse(candidate);
      } catch {
        return c.json({ code: 400, msg: "内容不是合法 JSON" }, 400);
      }
    }
    let release: { revision: number };
    try {
      const loose = parseBuiltinReleaseLoose(candidate);
      const max = await context.store.getMaxReleaseRevision();
      const revision = (max ?? 0) + 1;
      await context.store.insertRelease({
        revision,
        content: loose.content,
        publishedBy: admin.id,
      });
      await context.store.activateRelease(revision);
      release = { revision };
      context.logger.info("发布 Built-in Release", { revision, by: admin.email });
    } catch (error) {
      return c.json(
        { code: 400, msg: error instanceof Error ? error.message : "发布失败" },
        400,
      );
    }
    return c.json({ code: 0, data: release });
  });

  app.post("/console/api/releases/:revision/activate", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const revision = Number.parseInt(c.req.param("revision"), 10);
    const release = Number.isFinite(revision)
      ? await context.store.activateRelease(revision)
      : null;
    if (!release) return c.json({ code: 404, msg: "Release 不存在" }, 404);
    return c.json({ code: 0, data: { revision: release.revision } });
  });

  // ---- models ----

  app.get("/console/api/models", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const includeDisabled = c.req.query("includeDisabled") === "1";
    const models = await context.store.listModels({ includeDisabled });
    return c.json({ code: 0, data: { models } });
  });

  app.put("/console/api/models/:modelId", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const parsed = z
      .object({
        displayName: z.string().trim().min(1).max(120),
        upstreamModel: z.string().trim().max(200).nullable().optional(),
        inputPricePerMtok: priceString,
        outputPricePerMtok: priceString,
        cacheReadPricePerMtok: priceString.optional(),
        cacheWritePricePerMtok: priceString.optional(),
        enabled: z.boolean().optional(),
        sortOrder: z.number().int().min(-10000).max(10000).optional(),
      })
      .safeParse(await readJsonBody(c));
    if (!parsed.success) return c.json({ code: 400, msg: "请求参数不合法" }, 400);
    const model = await context.store.upsertModel({
      modelId: c.req.param("modelId"),
      displayName: parsed.data.displayName,
      upstreamModel: parsed.data.upstreamModel ?? null,
      inputPricePerMtok: parsed.data.inputPricePerMtok,
      outputPricePerMtok: parsed.data.outputPricePerMtok,
      cacheReadPricePerMtok: parsed.data.cacheReadPricePerMtok ?? "0.000000",
      cacheWritePricePerMtok: parsed.data.cacheWritePricePerMtok ?? "0.000000",
      enabled: parsed.data.enabled ?? true,
      sortOrder: parsed.data.sortOrder ?? 0,
    });
    return c.json({ code: 0, data: { model } });
  });

  app.delete("/console/api/models/:modelId", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const removed = await context.store.deleteModel(c.req.param("modelId"));
    if (!removed) return c.json({ code: 404, msg: "模型不存在" }, 404);
    return c.json({ code: 0 });
  });

  // ---- config ----

  app.get("/console/api/config", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const [help, forceUpdate] = await Promise.all([
      context.store.getConfigValue(PLATFORM_CONFIG_KEYS.help),
      context.store.getConfigValue(PLATFORM_CONFIG_KEYS.forceUpdate),
    ]);
    return c.json({ code: 0, data: { help, forceUpdate } });
  });

  app.put("/console/api/config", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const parsed = z
      .object({
        help: z.unknown().optional(),
        forceUpdate: z.unknown().optional(),
      })
      .safeParse(await readJsonBody(c));
    if (!parsed.success) return c.json({ code: 400, msg: "请求参数不合法" }, 400);
    if (parsed.data.help !== undefined) {
      await context.store.setConfigValue(PLATFORM_CONFIG_KEYS.help, parsed.data.help);
    }
    if (parsed.data.forceUpdate !== undefined) {
      await context.store.setConfigValue(PLATFORM_CONFIG_KEYS.forceUpdate, parsed.data.forceUpdate);
    }
    return c.json({ code: 0 });
  });

  // ---- usage ----

  app.get("/console/api/usage", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const filter = {
      ...(c.req.query("teamId") ? { teamId: c.req.query("teamId")! } : {}),
      ...(c.req.query("from") ? { from: c.req.query("from")! } : {}),
      ...(c.req.query("to") ? { to: c.req.query("to")! } : {}),
      limit: 100,
    };
    const [aggregates, records] = await Promise.all([
      context.store.aggregateUsage(filter),
      context.store.listUsage(filter),
    ]);
    return c.json({ code: 0, data: { aggregates, records } });
  });

  // ---- devices ----

  app.get("/console/api/devices", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const tokens = await context.store.listDeviceTokens();
    const users = new Map((await context.store.listUsers()).map((user) => [user.id, user]));
    return c.json({
      code: 0,
      data: {
        devices: tokens.map((token) => ({
          id: token.id,
          name: token.name,
          platform: token.platform,
          hint: token.tokenHint,
          userEmail: users.get(token.userId)?.email ?? "(已删除)",
          createdAt: token.createdAt,
          lastUsedAt: token.lastUsedAt,
          revokedAt: token.revokedAt,
        })),
      },
    });
  });

  app.post("/console/api/devices/:id/revoke", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const device = await context.store.findDeviceTokenById(c.req.param("id"));
    if (!device) return c.json({ code: 404, msg: "设备不存在" }, 404);
    await context.store.revokeDeviceToken(device.id, new Date(context.now()).toISOString());
    context.logger.info("吊销设备凭据", { deviceId: device.id, by: admin.email });
    return c.json({ code: 0 });
  });
}
