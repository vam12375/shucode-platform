import { PLATFORM_CONFIG_KEYS } from "@shucode-platform/contracts/client-configs";
import { parseBuiltinReleaseLoose } from "@shucode-platform/contracts/release";
import {
  deviceTokenHint,
  hashPassword,
  verifyPassword,
  type UpstreamProviderRecord,
  type UserRecord,
} from "@shucode-platform/db";
import type { Context, Hono } from "hono";
import { z } from "zod";
import type { AppContext } from "../context.js";
import { loadPricingDoc, loadPricingEntries, type ModelPricingEntry } from "../services/model-pricing.js";
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

const CURSOR_SEPARATOR = "~";

const UUID_SHAPE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/iu;

/** 翻页游标：上一页最后一条记录的 createdAt 与 id；对客户端保持不透明、原样回传。 */
function encodeUsageCursor(record: { createdAt: string; id: string }): string {
  return `${record.createdAt}${CURSOR_SEPARATOR}${record.id}`;
}

function decodeUsageCursor(cursor: string | undefined): { createdAt: string; id: string } | null {
  if (!cursor) return null;
  const separatorIndex = cursor.lastIndexOf(CURSOR_SEPARATOR);
  if (separatorIndex <= 0) return null;
  const createdAt = cursor.slice(0, separatorIndex);
  const id = cursor.slice(separatorIndex + 1);
  const isoShape = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/u;
  if (!isoShape.test(createdAt) || !UUID_SHAPE.test(id)) return null;
  return { createdAt, id };
}

/** 供应商 base_url 统一存「协议根」（去掉尾部斜杠与 /v1）：路径由调用方按协议拼接。 */
function normalizeUpstreamBaseUrl(raw: string): string {
  return raw.trim().replace(/\/+$/u, "").replace(/\/v1$/iu, "");
}

const UPSTREAM_PROVIDER_PROTOCOLS = [
  "openai-chat-completions",
  "openai-responses",
  "anthropic-messages",
] as const;
/** 默认 OpenAI chat 协议（决策：OpenAI 兼容端点为多上游的默认形态）。 */
const DEFAULT_UPSTREAM_PROTOCOL = "openai-chat-completions" as const;
const ANTHROPIC_VERSION_FALLBACK = "2023-06-01";
/** 控制台「获取模型列表」探测的超时。 */
const UPSTREAM_MODEL_LIST_TIMEOUT_MS = 15_000;

const providerInput = z.object({
  name: z.string().trim().min(1).max(80),
  baseUrl: z.string().trim().min(1).max(300),
  protocol: z.enum(UPSTREAM_PROVIDER_PROTOCOLS).optional(),
  apiKey: z.string().trim().max(400).nullable().optional(),
  anthropicVersion: z.string().trim().max(40).nullable().optional(),
  timeoutMs: z.number().int().min(1000).max(600_000).nullable().optional(),
  enabled: z.boolean().optional(),
});

/** 供应商探测超时上限：独立于模型列表探测；provider.timeout_ms 更短则尊重它。 */
const PROVIDER_TEST_TIMEOUT_MS = 30_000;

/**
 * 按协议构造最小推理探测请求。路径与网关同口径：base_url 是协议根，/v1 由平台拼接。
 * max_output_tokens 最小值 16 是 Responses API 的硬约束。
 */
function providerProbeRequest(
  protocol: (typeof UPSTREAM_PROVIDER_PROTOCOLS)[number],
  model: string,
): { path: string; body: unknown } {
  if (protocol === "openai-chat-completions") {
    return {
      path: "/v1/chat/completions",
      body: { model, messages: [{ role: "user", content: "ping" }], max_tokens: 1, stream: false },
    };
  }
  if (protocol === "openai-responses") {
    return { path: "/v1/responses", body: { model, input: "ping", max_output_tokens: 16 } };
  }
  return {
    path: "/v1/messages",
    body: { model, max_tokens: 1, messages: [{ role: "user", content: "ping" }] },
  };
}

// 模型定价库条目：每 1M token 价格（USD 口径，与平台 credit/1M token 同值）。
const pricingEntryInput = z.object({
  modelId: z.string().trim().min(1).max(120),
  displayName: z.string().trim().min(1).max(120),
  inputCostPerMillion: priceString,
  outputCostPerMillion: priceString,
  cacheReadCostPerMillion: priceString.optional(),
  cacheCreationCostPerMillion: priceString.optional(),
});
const pricingPayload = z.object({ entries: z.array(pricingEntryInput).min(1).max(2000) });

/** 管理控制台 API：全部位于 /console/api/*，要求 admin 角色的会话。 */
export function registerConsoleRoutes(app: Hono, context: AppContext): void {
  const loginLimiter = createRateLimiter({ windowMs: 5 * 60 * 1000, max: 10 });

  async function requireAdmin(c: Context): Promise<UserRecord | null> {
    const user = await readSessionUser(context, c);
    return user && user.role === "admin" ? user : null;
  }

  /**
   * 管理动作审计（New API 借鉴）：谁、对谁、做了什么。
   * detail 只放元数据（字段名/数量/ID），凭据、密码、请求正文与上游错误原文永不入审计；
   * 写失败只告警不阻塞主操作（与网关失败兜底路径一致）。
   */
  async function audit(
    actor: UserRecord,
    action: string,
    targetType: string | null,
    targetId: string | null,
    detail: Record<string, unknown> = {},
  ): Promise<void> {
    try {
      await context.store.insertAuditLog({
        actorUserId: actor.id,
        action,
        targetType,
        targetId,
        detail,
      });
    } catch (error) {
      context.logger.warn("审计写入失败", { action, error });
    }
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
    const usageCursor = c.req.query("usageCursor");
    const decodedUsageCursor = decodeUsageCursor(usageCursor);
    if (usageCursor && !decodedUsageCursor) {
      return c.json({ code: 400, msg: "分页游标不合法" }, 400);
    }
    // 多取一条探测下一页，records 只交付 limit 条。
    const [members, aggregates, usagePage, quotaAdjustments] = await Promise.all([
      context.store.listMembers(teamId),
      context.store.aggregateUsage({ teamId }),
      context.store.listUsage({
        teamId,
        limit: 51,
        ...(decodedUsageCursor
          ? { cursorCreatedAt: decodedUsageCursor.createdAt, cursorId: decodedUsageCursor.id }
          : {}),
      }),
      context.store.listQuotaAdjustments(teamId, 10),
    ]);
    const hasMore = usagePage.length > 50;
    const recentUsage = hasMore ? usagePage.slice(0, 50) : usagePage;
    const last = recentUsage[recentUsage.length - 1];
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
        recentUsageNextCursor: hasMore && last ? encodeUsageCursor(last) : null,
        quotaAdjustments,
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
    const teamId = c.req.param("id");
    // 额度调整流水（New API 借鉴：人工调额有前后快照可对账）：旧值先读、之后与新旧入账。
    // 注意 memory 实现返回原地引用，必须在 update 前把标量快照取走，不能持有记录引用。
    const previousTeam = await context.store.getTeam(teamId);
    if (!previousTeam) return c.json({ code: 404, msg: "团队不存在" }, 404);
    const previousQuota = previousTeam.quotaCredits;
    const previousUnlimited = previousTeam.unlimited;
    const previousName = previousTeam.name;
    const team = await context.store.updateTeam(teamId, {
      ...(parsed.data.name !== undefined ? { name: parsed.data.name } : {}),
      ...(parsed.data.quotaCredits !== undefined
        ? { quotaCredits: parsed.data.quotaCredits }
        : {}),
      ...(parsed.data.unlimited !== undefined ? { unlimited: parsed.data.unlimited } : {}),
    });
    if (!team) return c.json({ code: 404, msg: "团队不存在" }, 404);
    if (parsed.data.quotaCredits !== undefined || parsed.data.unlimited !== undefined) {
      await context.store.insertQuotaAdjustment({
        teamId,
        actorUserId: admin.id,
        action: "set_quota",
        oldQuotaCredits: previousQuota,
        newQuotaCredits: team.quotaCredits,
        oldUnlimited: previousUnlimited,
        newUnlimited: team.unlimited,
      });
    }
    if (parsed.data.name !== undefined && parsed.data.name !== previousName) {
      await audit(admin, "team.update", "team", team.id, { fields: ["name"] });
    }
    context.configEvents.emit("teams");
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
    context.configEvents.emit("teams");
    return c.json({ code: 0, data: { user: publicUser(user) } });
  });

  app.delete("/console/api/teams/:id/members/:userId", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const removed = await context.store.removeMember(c.req.param("id"), c.req.param("userId"));
    if (!removed) return c.json({ code: 404, msg: "成员不存在" }, 404);
    context.configEvents.emit("teams");
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
      await audit(admin, "user.create", "user", user.id, { role: user.role });
      return c.json({ code: 0, data: { user: publicUser(user) } });
    } catch (error) {
      return c.json(
        { code: 409, msg: error instanceof Error ? error.message : "用户创建失败" },
        409,
      );
    }
  });

  app.patch("/console/api/users/:id", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const parsed = z
      .object({
        disabled: z.boolean().optional(),
        password: z.string().min(8).max(200).optional(),
        displayName: z.string().trim().min(1).max(80).optional(),
        role: z.enum(["admin", "member"]).optional(),
      })
      .safeParse(await readJsonBody(c));
    if (!parsed.success) return c.json({ code: 400, msg: "请求参数不合法（密码至少 8 位）" }, 400);
    const patch = parsed.data;
    const targetId = c.req.param("id");
    // 自我保护：避免管理员禁用/降级自己导致无人可管理控制台。
    if (targetId === admin.id && (patch.disabled === true || patch.role !== undefined)) {
      return c.json({ code: 400, msg: "不能禁用自己的账号或变更自己的角色" }, 400);
    }
    const user = await context.store.updateUser(targetId, {
      ...(patch.disabled === undefined
        ? {}
        : { disabledAt: patch.disabled ? new Date(context.now()).toISOString() : null }),
      ...(patch.displayName !== undefined ? { displayName: patch.displayName } : {}),
      ...(patch.password !== undefined ? { passwordHash: await hashPassword(patch.password) } : {}),
      ...(patch.role !== undefined ? { role: patch.role } : {}),
    });
    if (!user) return c.json({ code: 404, msg: "用户不存在" }, 404);
    // 重置密码 / 禁用 / 降级后必须吊销该用户全部登录态：旧后台会话与设备凭据
    // 即时失效（只读路径虽实时校验 disabledAt/role，但密码重置后旧会话无任何
    // 校验会继续有效，属安全缺口）。启用（disabled=false）不触发回补。
    if (patch.password !== undefined || patch.disabled === true || patch.role !== undefined) {
      await context.store.deleteSessionsForUser(user.id);
      const revokedDevices = await context.store.revokeDeviceTokensForUser(
        user.id,
        new Date(context.now()).toISOString(),
      );
      context.logger.info("吊销用户登录态", { userId: user.id, by: admin.email, revokedDevices });
    }
    context.logger.info("更新用户", { userId: user.id, by: admin.email });
    await audit(admin, "user.update", "user", user.id, { fields: Object.keys(patch) });
    return c.json({ code: 0, data: { user: publicUser(user) } });
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
      await audit(admin, "release.publish", "release", String(revision));
      context.configEvents.emit("releases");
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
    context.configEvents.emit("releases");
    await audit(admin, "release.activate", "release", String(release.revision));
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
        upstreamProviderId: z.string().uuid().nullable().optional(),
        inputPricePerMtok: priceString,
        outputPricePerMtok: priceString,
        cacheReadPricePerMtok: priceString.optional(),
        cacheWritePricePerMtok: priceString.optional(),
        enabled: z.boolean().optional(),
        sortOrder: z.number().int().min(-10000).max(10000).optional(),
      })
      .safeParse(await readJsonBody(c));
    if (!parsed.success) return c.json({ code: 400, msg: "请求参数不合法" }, 400);
    // upstreamProviderId 恒随请求下发：null = 解除绑定回落 env 单上游。
    const model = await context.store.upsertModel({
      modelId: c.req.param("modelId"),
      displayName: parsed.data.displayName,
      upstreamModel: parsed.data.upstreamModel ?? null,
      upstreamProviderId: parsed.data.upstreamProviderId ?? null,
      inputPricePerMtok: parsed.data.inputPricePerMtok,
      outputPricePerMtok: parsed.data.outputPricePerMtok,
      cacheReadPricePerMtok: parsed.data.cacheReadPricePerMtok ?? "0.000000",
      cacheWritePricePerMtok: parsed.data.cacheWritePricePerMtok ?? "0.000000",
      enabled: parsed.data.enabled ?? true,
      sortOrder: parsed.data.sortOrder ?? 0,
    });
    context.configEvents.emit("models");
    await audit(admin, "model.upsert", "model", model.modelId, { displayName: model.displayName });
    return c.json({ code: 0, data: { model } });
  });

  // 从上游批量导入模型：以拉取到的上游模型名为准建档（模型 ID = 上游模型名，
  // 绑定所选供应商，默认停用 + 0 价格——价格未填前不得启用；已存在的跳过不覆盖）。
  app.post("/console/api/models/import", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const parsed = z
      .object({
        providerId: z.string().uuid(),
        modelIds: z.array(z.string().trim().min(1).max(200)).min(1).max(500),
      })
      .safeParse(await readJsonBody(c));
    if (!parsed.success) return c.json({ code: 400, msg: "请求参数不合法" }, 400);
    const provider = await context.store.getUpstreamProvider(parsed.data.providerId);
    if (!provider) return c.json({ code: 404, msg: "供应商不存在" }, 404);

    const requested = [...new Set(parsed.data.modelIds.map((id) => id.trim()).filter(Boolean))];
    // 命中定价库的模型自动带价（模型 ID 精确匹配；不命中保持 0 价格待人工填写）。
    const pricingMap = new Map(
      (await loadPricingEntries(context.store)).map((entry) => [entry.modelId, entry]),
    );
    const created: string[] = [];
    const priced: string[] = [];
    const skipped: string[] = [];
    for (const modelId of requested) {
      const existing = await context.store.getModel(modelId);
      if (existing) {
        skipped.push(modelId);
        continue;
      }
      const hit = pricingMap.get(modelId);
      if (hit) priced.push(modelId);
      await context.store.upsertModel({
        modelId,
        displayName: hit?.displayName ?? modelId,
        upstreamModel: modelId,
        upstreamProviderId: provider.id,
        inputPricePerMtok: hit?.inputCostPerMillion ?? "0.000000",
        outputPricePerMtok: hit?.outputCostPerMillion ?? "0.000000",
        cacheReadPricePerMtok: hit?.cacheReadCostPerMillion ?? "0.000000",
        cacheWritePricePerMtok: hit?.cacheCreationCostPerMillion ?? "0.000000",
        enabled: false,
        sortOrder: 0,
      });
      created.push(modelId);
    }
    context.logger.info("从上游导入模型", {
      providerId: provider.id,
      created: created.length,
      priced: priced.length,
      skipped: skipped.length,
      by: admin.email,
    });
    if (created.length > 0) context.configEvents.emit("models");
    await audit(admin, "model.import", "provider", provider.id, {
      created: created.length,
      priced: priced.length,
      skipped: skipped.length,
    });
    return c.json({ code: 0, data: { created, priced, skipped } });
  });

  app.delete("/console/api/models/:modelId", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const removed = await context.store.deleteModel(c.req.param("modelId"));
    if (!removed) return c.json({ code: 404, msg: "模型不存在" }, 404);
    context.configEvents.emit("models");
    await audit(admin, "model.delete", "model", c.req.param("modelId"));
    return c.json({ code: 0 });
  });

  // ---- upstream providers（多上游，按需配置） ----

  function publicProvider(record: UpstreamProviderRecord) {
    return {
      id: record.id,
      name: record.name,
      baseUrl: record.baseUrl,
      protocol: record.protocol,
      enabled: record.enabled,
      disabledMode: record.disabledMode,
      autoDisabledAt: record.autoDisabledAt,
      autoDisabledReason: record.autoDisabledReason,
      consecutiveFailures: record.consecutiveFailures,
      lastTestAt: record.lastTestAt,
      lastTestOk: record.lastTestOk,
      lastTestKind: record.lastTestKind,
      lastTestStatus: record.lastTestStatus,
      lastTestLatencyMs: record.lastTestLatencyMs,
      lastTestModel: record.lastTestModel,
      lastTestDetail: record.lastTestDetail,
      apiKeyHint: record.apiKeyHint,
      hasKey: record.apiKeyCiphertext !== null,
      anthropicVersion: record.anthropicVersion,
      timeoutMs: record.timeoutMs,
      createdAt: record.createdAt,
    };
  }

  function providerKeyPair(
    apiKey: string | undefined | null,
  ): { ciphertext: string | null; hint: string | null } | undefined {
    if (apiKey === undefined) return undefined;
    if (apiKey === null || apiKey === "") return { ciphertext: null, hint: null };
    if (!context.secretBox) return undefined; // 由调用方转 400
    return {
      ciphertext: context.secretBox.encrypt(apiKey),
      hint: deviceTokenHint(apiKey),
    };
  }

  app.get("/console/api/providers", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const providers = await context.store.listUpstreamProviders({ includeDisabled: true });
    return c.json({ code: 0, data: { providers: providers.map(publicProvider) } });
  });

  app.post("/console/api/providers", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const parsed = providerInput.safeParse(await readJsonBody(c));
    if (!parsed.success) return c.json({ code: 400, msg: "请求参数不合法" }, 400);
    const data = parsed.data;
    const keyPair = providerKeyPair(data.apiKey);
    if (data.apiKey && data.apiKey !== "" && !keyPair) {
      return c.json({ code: 400, msg: "未配置 PLATFORM_SECRET_KEY，无法安全存储上游密钥" }, 400);
    }
    try {
      const provider = await context.store.createUpstreamProvider({
        name: data.name,
        baseUrl: normalizeUpstreamBaseUrl(data.baseUrl),
        protocol: data.protocol ?? DEFAULT_UPSTREAM_PROTOCOL,
        apiKeyCiphertext: keyPair?.ciphertext ?? null,
        apiKeyHint: keyPair?.hint ?? null,
        anthropicVersion: data.anthropicVersion ?? null,
        timeoutMs: data.timeoutMs ?? null,
        enabled: data.enabled ?? true,
      });
      context.logger.info("创建上游供应商", { providerId: provider.id, by: admin.email });
      await audit(admin, "provider.create", "provider", provider.id, {
        name: provider.name,
        protocol: provider.protocol,
      });
      context.configEvents.emit("providers");
      return c.json({ code: 0, data: { provider: publicProvider(provider) } });
    } catch (error) {
      return c.json(
        { code: 409, msg: error instanceof Error ? error.message : "供应商创建失败" },
        409,
      );
    }
  });

  app.patch("/console/api/providers/:id", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const parsed = providerInput.partial().safeParse(await readJsonBody(c));
    if (!parsed.success) return c.json({ code: 400, msg: "请求参数不合法" }, 400);
    const data = parsed.data;
    const keyPair = providerKeyPair(data.apiKey);
    if (data.apiKey && data.apiKey !== "" && !keyPair) {
      return c.json({ code: 400, msg: "未配置 PLATFORM_SECRET_KEY，无法安全存储上游密钥" }, 400);
    }
    const provider = await context.store.updateUpstreamProvider(c.req.param("id"), {
      ...(data.name !== undefined ? { name: data.name } : {}),
      ...(data.baseUrl !== undefined ? { baseUrl: normalizeUpstreamBaseUrl(data.baseUrl) } : {}),
      ...(data.protocol !== undefined ? { protocol: data.protocol } : {}),
      ...(keyPair
        ? { apiKeyCiphertext: keyPair.ciphertext, apiKeyHint: keyPair.hint }
        : {}),
      ...(data.anthropicVersion !== undefined
        ? { anthropicVersion: data.anthropicVersion }
        : {}),
      ...(data.timeoutMs !== undefined ? { timeoutMs: data.timeoutMs } : {}),
      ...(data.enabled !== undefined ? { enabled: data.enabled } : {}),
    });
    if (!provider) return c.json({ code: 404, msg: "供应商不存在" }, 404);
    context.logger.info("更新上游供应商", { providerId: provider.id, by: admin.email });
    await audit(admin, "provider.update", "provider", provider.id, { fields: Object.keys(data) });
    context.configEvents.emit("providers");
    return c.json({ code: 0, data: { provider: publicProvider(provider) } });
  });

  app.delete("/console/api/providers/:id", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    // 绑定该供应商的模型会自动解绑定并回落 env 单上游（FK ON DELETE SET NULL）。
    const existing = await context.store.getUpstreamProvider(c.req.param("id"));
    const removed = await context.store.deleteUpstreamProvider(c.req.param("id"));
    if (!removed) return c.json({ code: 404, msg: "供应商不存在" }, 404);
    context.logger.info("删除上游供应商", { providerId: c.req.param("id"), by: admin.email });
    await audit(admin, "provider.delete", "provider", c.req.param("id"), {
      name: existing?.name ?? null,
    });
    context.configEvents.emit("providers");
    return c.json({ code: 0 });
  });

  // 获取上游模型列表：服务端代理探测（key 不出后端）。默认 OpenAI 兼容 /v1/models，
  // anthropic 协议供应商用同名端点 + x-api-key 鉴权，供控制台对照填写 upstream_model。
  app.get("/console/api/providers/:id/models", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const provider = await context.store.getUpstreamProvider(c.req.param("id"));
    if (!provider) return c.json({ code: 404, msg: "供应商不存在" }, 404);

    let apiKey: string | null = null;
    if (provider.apiKeyCiphertext) {
      if (!context.secretBox) {
        return c.json({ code: 500, msg: "平台密钥未配置，无法解密该供应商的 API Key" }, 500);
      }
      try {
        apiKey = context.secretBox.decrypt(provider.apiKeyCiphertext);
      } catch {
        return c.json({ code: 500, msg: "供应商 API Key 解密失败（PLATFORM_SECRET_KEY 是否更换过？）" }, 500);
      }
    }

    const headers: Record<string, string> = {};
    if (provider.protocol === "anthropic-messages") {
      if (apiKey) headers["x-api-key"] = apiKey;
      headers["anthropic-version"] = provider.anthropicVersion ?? ANTHROPIC_VERSION_FALLBACK;
    } else if (apiKey) {
      headers["authorization"] = `Bearer ${apiKey}`;
    }

    let upstream: Response;
    try {
      upstream = await fetch(`${provider.baseUrl}/v1/models`, {
        headers,
        signal: AbortSignal.timeout(UPSTREAM_MODEL_LIST_TIMEOUT_MS),
      });
    } catch (error) {
      context.logger.warn("获取上游模型列表失败", { providerId: provider.id, error });
      return c.json({ code: 504, msg: "上游不可达或响应超时" }, 504);
    }
    if (!upstream.ok) {
      const detail = (await upstream.text().catch(() => "")).slice(0, 300);
      return c.json(
        { code: 502, msg: `上游返回 HTTP ${upstream.status}${detail ? `: ${detail}` : ""}` },
        502,
      );
    }
    const body: unknown = await upstream.json().catch(() => null);
    // OpenAI/Anthropic list 均为 { data: [{ id }] }；宽容兼容直接返回数组的服务实现。
    const rows = Array.isArray(body)
      ? body
      : ((body as { data?: unknown } | null)?.data ?? []);
    const models = (Array.isArray(rows) ? rows : [])
      .map((row) =>
        row && typeof row === "object" && "id" in row
          ? String((row as { id: unknown }).id)
          : null,
      )
      .filter((id): id is string => Boolean(id))
      .sort((a, b) => a.localeCompare(b));
    return c.json({ code: 0, data: { models } });
  });

  // 供应商连通/推理测试：真实打一次上游（key 只在后端解密使用，不落日志、不出接口）。
  // 不带 model：GET /v1/models，只验证可达性与鉴权；带 model：按协议发一次最小推理，
  // 验证「模型真的可调用」——获取列表成功不等于推理可用。
  app.post("/console/api/providers/:id/test", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const provider = await context.store.getUpstreamProvider(c.req.param("id"));
    if (!provider) return c.json({ code: 404, msg: "供应商不存在" }, 404);
    const parsed = z
      .object({ model: z.string().trim().min(1).max(200).optional() })
      .safeParse(await readJsonBody(c));
    if (!parsed.success) return c.json({ code: 400, msg: "请求参数不合法" }, 400);
    const probeModel = parsed.data.model ?? null;

    let apiKey: string | null = null;
    if (provider.apiKeyCiphertext) {
      if (!context.secretBox) {
        return c.json({ code: 500, msg: "平台密钥未配置，无法解密该供应商的 API Key" }, 500);
      }
      try {
        apiKey = context.secretBox.decrypt(provider.apiKeyCiphertext);
      } catch {
        return c.json({ code: 500, msg: "供应商 API Key 解密失败（PLATFORM_SECRET_KEY 是否更换过？）" }, 500);
      }
    }

    // 鉴权头与网关 buildUpstreamRequest 同口径：openai 家族 Bearer；anthropic x-api-key + anthropic-version。
    const headers: Record<string, string> = { "content-type": "application/json" };
    if (provider.protocol === "anthropic-messages") {
      if (apiKey) headers["x-api-key"] = apiKey;
      headers["anthropic-version"] = provider.anthropicVersion ?? ANTHROPIC_VERSION_FALLBACK;
    } else if (apiKey) {
      headers["authorization"] = `Bearer ${apiKey}`;
    }

    let url: string;
    let body: string | undefined;
    if (probeModel) {
      const probe = providerProbeRequest(provider.protocol, probeModel);
      url = `${provider.baseUrl}${probe.path}`;
      body = JSON.stringify(probe.body);
    } else {
      url = `${provider.baseUrl}/v1/models`;
    }

    const startedAt = Date.now();
    // 测试结果落库（0006 迁移的 last_test_* 列）：控制台刷新即可看到最近一次可用性，
    // 不再只依赖会话内状态。detail 是上游错误原文截断，不含任何凭据。
    const persistOutcome = async (outcome: {
      ok: boolean;
      kind: string;
      status: number | null;
      latencyMs: number;
      detail: string | null;
    }): Promise<void> => {
      try {
        await context.store.recordProviderTestResult(provider.id, {
          ok: outcome.ok,
          kind: outcome.kind,
          status: outcome.status,
          latencyMs: outcome.latencyMs,
          model: probeModel,
          detail: outcome.detail,
        });
      } catch (error) {
        context.logger.warn("供应商测试结果落库失败", { providerId: provider.id, error });
      }
      await audit(admin, "provider.test", "provider", provider.id, {
        ok: outcome.ok,
        kind: outcome.kind,
      });
    };

    let upstream: Response;
    try {
      upstream = await fetch(url, {
        method: body ? "POST" : "GET",
        headers,
        ...(body ? { body } : {}),
        signal: AbortSignal.timeout(
          Math.min(provider.timeoutMs ?? PROVIDER_TEST_TIMEOUT_MS, PROVIDER_TEST_TIMEOUT_MS),
        ),
      });
    } catch (error) {
      // AbortSignal.timeout 的到期错误 name 为 TimeoutError，与其余网络故障分开归类。
      const timedOut = error instanceof Error && error.name === "TimeoutError";
      const latencyMs = Date.now() - startedAt;
      context.logger.warn("供应商测试失败", {
        providerId: provider.id,
        withModel: probeModel !== null,
        timedOut,
        latencyMs,
        by: admin.email,
      });
      const outcome = { ok: false, kind: timedOut ? "timeout" : "network", status: null, latencyMs, detail: null };
      await persistOutcome(outcome);
      return c.json({ code: 0, data: outcome });
    }
    const latencyMs = Date.now() - startedAt;
    if (!upstream.ok) {
      const detail = (await upstream.text().catch(() => "")).slice(0, 300);
      const kind =
        upstream.status === 401 || upstream.status === 403
          ? "auth"
          : upstream.status === 404
            ? "not_found"
            : upstream.status === 429
              ? "rate_limited"
              : "upstream_error";
      context.logger.info("供应商测试未通过", {
        providerId: provider.id,
        withModel: probeModel !== null,
        kind,
        status: upstream.status,
        by: admin.email,
      });
      const outcome = { ok: false, kind, status: upstream.status, latencyMs, detail };
      await persistOutcome(outcome);
      return c.json({ code: 0, data: outcome });
    }
    context.logger.info("供应商测试通过", {
      providerId: provider.id,
      withModel: probeModel !== null,
      latencyMs,
      by: admin.email,
    });
    const outcome = { ok: true, kind: "ok", status: upstream.status, latencyMs, detail: null };
    await persistOutcome(outcome);
    return c.json({ code: 0, data: outcome });
  });

  // ---- model pricing（模型定价库：cc-switch / models.dev 口径） ----

  app.get("/console/api/model-pricing", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const doc = await loadPricingDoc(context.store);
    return c.json({ code: 0, data: doc });
  });

  // 全量替换保存（前端整表提交 + expectedVersion 乐观锁）；modelId 重复时以后一条为准。
  // 版本冲突返回 409：另一个管理员改过定价，前端提示刷新，不静默覆盖。
  app.put("/console/api/model-pricing", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const parsed = z
      .object({
        entries: pricingPayload.shape.entries,
        expectedVersion: z.string().trim().min(1).max(64),
      })
      .safeParse(await readJsonBody(c));
    if (!parsed.success) {
      return c.json(
        { code: 400, msg: "请求参数不合法（条目需含模型 ID、显示名与非负价格，并携带 expectedVersion）" },
        400,
      );
    }
    const byId = new Map<string, ModelPricingEntry>();
    for (const raw of parsed.data.entries) {
      byId.set(raw.modelId, {
        modelId: raw.modelId,
        displayName: raw.displayName,
        inputCostPerMillion: raw.inputCostPerMillion,
        outputCostPerMillion: raw.outputCostPerMillion,
        cacheReadCostPerMillion: raw.cacheReadCostPerMillion ?? "0.000000",
        cacheCreationCostPerMillion: raw.cacheCreationCostPerMillion ?? "0.000000",
      });
    }
    const entries = [...byId.values()];
    const saved = await context.store.saveModelPricing(entries, parsed.data.expectedVersion);
    if (!saved) {
      return c.json({ code: 409, msg: "定价库已被他人修改，请刷新页面后重试" }, 409);
    }
    context.logger.info("更新模型定价库", { entries: entries.length, by: admin.email });
    await audit(admin, "pricing.update", null, null, { entries: entries.length });
    return c.json({ code: 0, data: { entries: saved.entries, version: saved.version } });
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
    context.configEvents.emit("platform-config");
    return c.json({ code: 0 });
  });

  // ---- usage ----

  app.get("/console/api/usage", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const limitRaw = Number.parseInt(c.req.query("limit") ?? "", 10);
    const limit = Number.isFinite(limitRaw) && limitRaw > 0 ? Math.min(limitRaw, 500) : 100;
    const cursor = c.req.query("cursor");
    const decodedCursor = decodeUsageCursor(cursor);
    if (cursor && !decodedCursor) {
      return c.json({ code: 400, msg: "分页游标不合法" }, 400);
    }
    // 诊断筛选：状态白名单 + 供应商 ID 必须是 UUID，避免把脏参数透传进存储层。
    const statusQuery = c.req.query("status");
    if (statusQuery && statusQuery !== "ok" && statusQuery !== "error" && statusQuery !== "aborted") {
      return c.json({ code: 400, msg: "status 只支持 ok / error / aborted" }, 400);
    }
    const providerIdQuery = c.req.query("providerId");
    if (providerIdQuery && !UUID_SHAPE.test(providerIdQuery)) {
      return c.json({ code: 400, msg: "providerId 不合法" }, 400);
    }
    const baseFilter = {
      ...(c.req.query("teamId") ? { teamId: c.req.query("teamId")! } : {}),
      ...(c.req.query("from") ? { from: c.req.query("from")! } : {}),
      ...(c.req.query("to") ? { to: c.req.query("to")! } : {}),
      ...(c.req.query("userId") ? { userId: c.req.query("userId")! } : {}),
      ...(c.req.query("model") ? { model: c.req.query("model")! } : {}),
      ...(providerIdQuery ? { providerId: providerIdQuery } : {}),
      ...(statusQuery ? { status: statusQuery as "ok" | "error" | "aborted" } : {}),
    };
    const pageFilter = {
      ...baseFilter,
      limit: limit + 1,
      ...(decodedCursor
        ? { cursorCreatedAt: decodedCursor.createdAt, cursorId: decodedCursor.id }
        : {}),
    };
    const [aggregates, allRecords] = await Promise.all([
      context.store.aggregateUsage(baseFilter),
      context.store.listUsage(pageFilter),
    ]);
    const hasMore = allRecords.length > limit;
    const records = hasMore ? allRecords.slice(0, limit) : allRecords;
    const last = records[records.length - 1];
    return c.json({
      code: 0,
      data: {
        aggregates,
        records,
        nextCursor: hasMore && last ? encodeUsageCursor(last) : null,
      },
    });
  });

  // 趋势图口径的时间桶聚合：粒度白名单 + 小时粒度限制范围 ≤ 31 天，防止桶数失控。
  app.get("/console/api/usage/series", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const granularity = c.req.query("granularity") === "hour" ? "hour" : "day";
    const fromQuery = c.req.query("from");
    const toQuery = c.req.query("to");
    if (granularity === "hour" && fromQuery && toQuery) {
      // from/to 是本地时间的 naive 串，此处只用于估算范围长度，统一按 UTC 解析即可。
      const spanMs =
        Date.parse(`${toQuery.replace(" ", "T")}Z`) - Date.parse(`${fromQuery.replace(" ", "T")}Z`);
      if (Number.isFinite(spanMs) && spanMs > 31 * 86_400_000) {
        return c.json({ code: 400, msg: "小时粒度最多查询 31 天范围" }, 400);
      }
    }
    const statusQuery = c.req.query("status");
    if (statusQuery && statusQuery !== "ok" && statusQuery !== "error" && statusQuery !== "aborted") {
      return c.json({ code: 400, msg: "status 只支持 ok / error / aborted" }, 400);
    }
    const providerIdQuery = c.req.query("providerId");
    if (providerIdQuery && !UUID_SHAPE.test(providerIdQuery)) {
      return c.json({ code: 400, msg: "providerId 不合法" }, 400);
    }
    const filter = {
      ...(c.req.query("teamId") ? { teamId: c.req.query("teamId")! } : {}),
      ...(fromQuery ? { from: fromQuery } : {}),
      ...(toQuery ? { to: toQuery } : {}),
      ...(c.req.query("userId") ? { userId: c.req.query("userId")! } : {}),
      ...(c.req.query("model") ? { model: c.req.query("model")! } : {}),
      ...(providerIdQuery ? { providerId: providerIdQuery } : {}),
      ...(statusQuery ? { status: statusQuery as "ok" | "error" | "aborted" } : {}),
    };
    const series = await context.store.aggregateUsageSeries(filter, granularity);
    return c.json({ code: 0, data: { series } });
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
    await audit(admin, "device.revoke", "device", device.id);
    context.configEvents.emit("devices");
    return c.json({ code: 0 });
  });

  // ---- audit ----

  // 管理动作审计查询：与 usage 相同的 (created_at, id) 游标翻页；复用其编解码。
  app.get("/console/api/audit", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const limitRaw = Number.parseInt(c.req.query("limit") ?? "", 10);
    const limit = Number.isFinite(limitRaw) && limitRaw > 0 ? Math.min(limitRaw, 500) : 50;
    const cursor = c.req.query("cursor");
    const decodedCursor = decodeUsageCursor(cursor);
    if (cursor && !decodedCursor) {
      return c.json({ code: 400, msg: "分页游标不合法" }, 400);
    }
    const logs = await context.store.listAuditLogs({
      limit: limit + 1,
      ...(c.req.query("actorUserId") ? { actorUserId: c.req.query("actorUserId")! } : {}),
      ...(decodedCursor
        ? { cursorCreatedAt: decodedCursor.createdAt, cursorId: decodedCursor.id }
        : {}),
    });
    const hasMore = logs.length > limit;
    const page = hasMore ? logs.slice(0, limit) : logs;
    const last = page[page.length - 1];
    return c.json({
      code: 0,
      data: {
        logs: page,
        nextCursor: hasMore && last ? encodeUsageCursor(last) : null,
      },
    });
  });
}
