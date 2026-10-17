import { PLATFORM_CONFIG_KEYS } from "@shucode-platform/contracts/client-configs";
import { parseBuiltinReleaseLoose } from "@shucode-platform/contracts/release";
import {
  deviceTokenHint,
  hashPassword,
  verifyPassword,
  type UserRecord,
} from "@shucode-platform/db";
import type { Context, Hono } from "hono";
import { z } from "zod";
import type { AppContext } from "../context.js";
import { loadPricingEntries, MODEL_PRICING_KV_KEY, type ModelPricingEntry } from "../services/model-pricing.js";
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
  const uuidShape = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/iu;
  if (!isoShape.test(createdAt) || !uuidShape.test(id)) return null;
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
    const [members, aggregates, usagePage] = await Promise.all([
      context.store.listMembers(teamId),
      context.store.aggregateUsage({ teamId }),
      context.store.listUsage({
        teamId,
        limit: 51,
        ...(decodedUsageCursor
          ? { cursorCreatedAt: decodedUsageCursor.createdAt, cursorId: decodedUsageCursor.id }
          : {}),
      }),
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
    return c.json({ code: 0, data: { created, priced, skipped } });
  });

  app.delete("/console/api/models/:modelId", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const removed = await context.store.deleteModel(c.req.param("modelId"));
    if (!removed) return c.json({ code: 404, msg: "模型不存在" }, 404);
    context.configEvents.emit("models");
    return c.json({ code: 0 });
  });

  // ---- upstream providers（多上游，按需配置） ----

  function publicProvider(record: {
    id: string;
    name: string;
    baseUrl: string;
    protocol: string;
    enabled: boolean;
    apiKeyHint: string | null;
    apiKeyCiphertext: string | null;
    anthropicVersion: string | null;
    timeoutMs: number | null;
    createdAt: string;
  }) {
    return {
      id: record.id,
      name: record.name,
      baseUrl: record.baseUrl,
      protocol: record.protocol,
      enabled: record.enabled,
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
    context.configEvents.emit("providers");
    return c.json({ code: 0, data: { provider: publicProvider(provider) } });
  });

  app.delete("/console/api/providers/:id", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    // 绑定该供应商的模型会自动解绑定并回落 env 单上游（FK ON DELETE SET NULL）。
    const removed = await context.store.deleteUpstreamProvider(c.req.param("id"));
    if (!removed) return c.json({ code: 404, msg: "供应商不存在" }, 404);
    context.logger.info("删除上游供应商", { providerId: c.req.param("id"), by: admin.email });
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

  // ---- model pricing（模型定价库：cc-switch / models.dev 口径） ----

  app.get("/console/api/model-pricing", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const entries = await loadPricingEntries(context.store);
    return c.json({ code: 0, data: { entries } });
  });

  // 全量替换保存（前端编辑后整表提交）；modelId 重复时以后一条为准。
  app.put("/console/api/model-pricing", async (c) => {
    const admin = await requireAdmin(c);
    if (!admin) return c.json({ code: 401, msg: "需要管理员登录" }, 401);
    const parsed = pricingPayload.safeParse(await readJsonBody(c));
    if (!parsed.success) {
      return c.json({ code: 400, msg: "请求参数不合法（条目需含模型 ID、显示名与非负价格）" }, 400);
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
    await context.store.setConfigValue(MODEL_PRICING_KV_KEY, { entries });
    context.logger.info("更新模型定价库", { entries: entries.length, by: admin.email });
    return c.json({ code: 0, data: { entries } });
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
    const baseFilter = {
      ...(c.req.query("teamId") ? { teamId: c.req.query("teamId")! } : {}),
      ...(c.req.query("from") ? { from: c.req.query("from")! } : {}),
      ...(c.req.query("to") ? { to: c.req.query("to")! } : {}),
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
    context.configEvents.emit("devices");
    return c.json({ code: 0 });
  });
}
