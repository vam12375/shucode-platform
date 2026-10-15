import { formatMicroCredits, parseCreditsToMicro } from "@shucode-platform/contracts/credits";
import { randomId } from "./crypto.js";
import { hashPassword } from "./passwords.js";
import type {
  DeviceTokenRecord,
  LoginFlowRecord,
  PlatformModelRecord,
  PlatformStore,
  ReleaseRecord,
  SessionRecord,
  TeamMemberView,
  TeamRecord,
  TeamWithUsage,
  UsageAggregateRow,
  UsageFilter,
  UsageRecord,
  UserRecord,
} from "./ports.js";

export interface MemoryStoreOptions {
  /** 无 Postgres 的本地体验模式：预置管理员/成员/演示团队与两个模型。 */
  demoSeed?: boolean;
}

function nowIso(): string {
  return new Date().toISOString();
}

/** 进程内实现：语义与 Postgres 实现一致，用于本地体验与测试。 */
export async function createMemoryStore(
  options: MemoryStoreOptions = {},
): Promise<PlatformStore> {
  const users = new Map<string, UserRecord>();
  const sessions = new Map<string, SessionRecord>();
  const teams = new Map<string, TeamRecord>();
  const members = new Map<string, Set<string>>(); // teamId -> userIds
  const memberRoles = new Map<string, "owner" | "member">(); // `${teamId}:${userId}`
  const memberJoinedAt = new Map<string, string>();
  const deviceTokens = new Map<string, DeviceTokenRecord>();
  const flows = new Map<string, LoginFlowRecord>();
  const releases = new Map<number, ReleaseRecord>();
  const models = new Map<string, PlatformModelRecord>();
  const config = new Map<string, unknown>();
  const usage: UsageRecord[] = [];

  function memberKey(teamId: string, userId: string): string {
    return `${teamId}:${userId}`;
  }

  function readTeam(id: string): TeamRecord | null {
    return teams.get(id) ?? null;
  }

  function teamMemberCount(teamId: string): number {
    return members.get(teamId)?.size ?? 0;
  }

  const store: PlatformStore = {
    // ---- users ----
    async findUserByEmail(email) {
      const normalized = email.trim().toLowerCase();
      for (const user of users.values()) {
        if (user.email.toLowerCase() === normalized) return user;
      }
      return null;
    },
    async findUserById(id) {
      return users.get(id) ?? null;
    },
    async createUser(input) {
      const email = input.email.trim().toLowerCase();
      for (const user of users.values()) {
        if (user.email.toLowerCase() === email) {
          throw new Error(`邮箱已存在: ${email}`);
        }
      }
      const record: UserRecord = {
        id: randomId(),
        email,
        passwordHash: input.passwordHash,
        displayName: input.displayName,
        role: input.role,
        disabledAt: null,
        createdAt: nowIso(),
      };
      users.set(record.id, record);
      return record;
    },
    async listUsers() {
      return [...users.values()].sort((a, b) => a.createdAt.localeCompare(b.createdAt));
    },

    // ---- sessions ----
    async createSession(record) {
      sessions.set(record.tokenHash, record);
    },
    async findSession(tokenHash) {
      return sessions.get(tokenHash) ?? null;
    },
    async touchSession(tokenHash, at) {
      const session = sessions.get(tokenHash);
      if (session) session.lastSeenAt = at;
    },
    async deleteSession(tokenHash) {
      sessions.delete(tokenHash);
    },

    // ---- teams ----
    async createTeam(input) {
      const name = input.name.trim();
      for (const team of teams.values()) {
        if (team.name === name) throw new Error(`团队名已存在: ${name}`);
      }
      const record: TeamRecord = {
        id: randomId(),
        name,
        quotaCredits: input.quotaCredits,
        usedCredits: "0.000000",
        reservedCredits: "0.000000",
        unlimited: input.unlimited,
        createdAt: nowIso(),
        disabledAt: null,
      };
      teams.set(record.id, record);
      members.set(record.id, new Set());
      return record;
    },
    async getTeam(id) {
      return readTeam(id);
    },
    async listTeams(): Promise<TeamWithUsage[]> {
      return [...teams.values()]
        .sort((a, b) => a.createdAt.localeCompare(b.createdAt))
        .map((team) => ({ team, memberCount: teamMemberCount(team.id) }));
    },
    async updateTeam(id, patch) {
      const team = teams.get(id);
      if (!team) return null;
      if (patch.name !== undefined) team.name = patch.name;
      if (patch.quotaCredits !== undefined) team.quotaCredits = patch.quotaCredits;
      if (patch.unlimited !== undefined) team.unlimited = patch.unlimited;
      if (patch.disabledAt !== undefined) team.disabledAt = patch.disabledAt;
      return team;
    },
    async listMembers(teamId): Promise<TeamMemberView[]> {
      const ids = members.get(teamId);
      if (!ids) return [];
      const views: TeamMemberView[] = [];
      for (const userId of ids) {
        const user = users.get(userId);
        if (!user) continue;
        views.push({
          user,
          role: memberRoles.get(memberKey(teamId, userId)) ?? "member",
          createdAt: memberJoinedAt.get(memberKey(teamId, userId)) ?? nowIso(),
        });
      }
      return views.sort((a, b) => a.createdAt.localeCompare(b.createdAt));
    },
    async addMember(teamId, userId, role) {
      if (!teams.has(teamId)) throw new Error(`团队不存在: ${teamId}`);
      if (!users.has(userId)) throw new Error(`用户不存在: ${userId}`);
      const set = members.get(teamId) ?? new Set<string>();
      if (!set.has(userId)) memberJoinedAt.set(memberKey(teamId, userId), nowIso());
      set.add(userId);
      members.set(teamId, set);
      memberRoles.set(memberKey(teamId, userId), role);
    },
    async removeMember(teamId, userId) {
      const set = members.get(teamId);
      if (!set || !set.delete(userId)) return false;
      memberRoles.delete(memberKey(teamId, userId));
      memberJoinedAt.delete(memberKey(teamId, userId));
      return true;
    },
    async listTeamsForUser(userId) {
      const result: Array<{ team: TeamRecord; role: "owner" | "member" }> = [];
      for (const [teamId, ids] of members) {
        if (!ids.has(userId)) continue;
        const team = teams.get(teamId);
        if (!team) continue;
        result.push({
          team,
          role: memberRoles.get(memberKey(teamId, userId)) ?? "member",
        });
      }
      return result.sort((a, b) => a.team.createdAt.localeCompare(b.team.createdAt));
    },

    // ---- device tokens ----
    async createDeviceToken(record) {
      deviceTokens.set(record.id, record);
    },
    async findDeviceTokenByHash(tokenHash) {
      for (const token of deviceTokens.values()) {
        if (token.tokenHash === tokenHash) return token;
      }
      return null;
    },
    async findDeviceTokenById(id) {
      return deviceTokens.get(id) ?? null;
    },
    async listDeviceTokens(userId) {
      return [...deviceTokens.values()]
        .filter((token) => (userId ? token.userId === userId : true))
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
    },
    async touchDeviceToken(id, at) {
      const token = deviceTokens.get(id);
      if (token) token.lastUsedAt = at;
    },
    async revokeDeviceToken(id, at) {
      const token = deviceTokens.get(id);
      if (token) token.revokedAt = at;
    },

    // ---- login flows ----
    async createLoginFlow(record) {
      flows.set(record.id, record);
    },
    async getLoginFlow(id) {
      return flows.get(id) ?? null;
    },
    async updateLoginFlow(id, patch) {
      const flow = flows.get(id);
      if (!flow) return null;
      if (patch.status !== undefined) flow.status = patch.status;
      if (patch.userId !== undefined) flow.userId = patch.userId;
      if (patch.authorizedAt !== undefined) flow.authorizedAt = patch.authorizedAt;
      if (patch.consumedAt !== undefined) flow.consumedAt = patch.consumedAt;
      if (patch.tokenId !== undefined) flow.tokenId = patch.tokenId;
      if (patch.lastPolledAt !== undefined) flow.lastPolledAt = patch.lastPolledAt;
      return flow;
    },
    async consumeLoginFlow(id, consumedAt) {
      const flow = flows.get(id);
      if (!flow || flow.status !== "authorized") return null;
      flow.status = "consumed";
      flow.consumedAt = consumedAt;
      return flow;
    },

    // ---- releases ----
    async insertRelease(input) {
      if (releases.has(input.revision)) {
        throw new Error(`Release revision 已存在: ${input.revision}`);
      }
      const record: ReleaseRecord = {
        revision: input.revision,
        content: input.content,
        publishedAt: nowIso(),
        publishedBy: input.publishedBy,
        isActive: false,
      };
      releases.set(record.revision, record);
      if (![...releases.values()].some((item) => item.isActive)) {
        record.isActive = true;
      }
      return record;
    },
    async listReleases() {
      return [...releases.values()].sort((a, b) => b.revision - a.revision);
    },
    async getRelease(revision) {
      return releases.get(revision) ?? null;
    },
    async getActiveRelease() {
      return [...releases.values()].find((item) => item.isActive) ?? null;
    },
    async activateRelease(revision) {
      const target = releases.get(revision);
      if (!target) return null;
      for (const item of releases.values()) item.isActive = false;
      target.isActive = true;
      return target;
    },
    async getMaxReleaseRevision() {
      if (releases.size === 0) return null;
      return Math.max(...releases.keys());
    },

    // ---- models ----
    async listModels(options) {
      return [...models.values()]
        .filter((model) => (options?.includeDisabled ? true : model.enabled))
        .sort((a, b) => a.sortOrder - b.sortOrder || a.modelId.localeCompare(b.modelId));
    },
    async getModel(modelId) {
      return models.get(modelId) ?? null;
    },
    async upsertModel(input) {
      const existing = models.get(input.modelId);
      const record: PlatformModelRecord = {
        modelId: input.modelId,
        displayName: input.displayName,
        upstreamModel: input.upstreamModel ?? existing?.upstreamModel ?? null,
        inputPricePerMtok: input.inputPricePerMtok,
        outputPricePerMtok: input.outputPricePerMtok,
        cacheReadPricePerMtok: input.cacheReadPricePerMtok ?? existing?.cacheReadPricePerMtok ?? "0",
        cacheWritePricePerMtok:
          input.cacheWritePricePerMtok ?? existing?.cacheWritePricePerMtok ?? "0",
        enabled: input.enabled ?? existing?.enabled ?? true,
        sortOrder: input.sortOrder ?? existing?.sortOrder ?? 0,
        createdAt: existing?.createdAt ?? nowIso(),
        updatedAt: nowIso(),
      };
      models.set(record.modelId, record);
      return record;
    },
    async deleteModel(modelId) {
      return models.delete(modelId);
    },

    // ---- config kv ----
    async getConfigValue(key) {
      return config.has(key) ? (config.get(key) as unknown) : null;
    },
    async setConfigValue(key, value) {
      config.set(key, value);
    },

    // ---- usage ----
    async insertUsage(record) {
      usage.push(record);
    },
    async listUsage(filter) {
      const limit = Math.min(Math.max(filter.limit ?? 50, 1), 500);
      return usage
        .filter((record) => matchesFilter(record, filter))
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
        .slice(0, limit);
    },
    async aggregateUsage(filter) {
      const grouped = new Map<string, UsageAggregateRow>();
      for (const record of usage) {
        if (!matchesFilter(record, filter)) continue;
        const row = grouped.get(record.model) ?? {
          model: record.model,
          requests: 0,
          inputTokens: 0,
          outputTokens: 0,
          creditsMicro: 0,
        };
        row.requests += 1;
        row.inputTokens += record.inputTokens;
        row.outputTokens += record.outputTokens;
        row.creditsMicro += record.creditsMicro;
        grouped.set(record.model, row);
      }
      return [...grouped.values()].sort((a, b) => b.creditsMicro - a.creditsMicro);
    },

    // ---- quota ----
    async tryReserveQuota(teamId, micro) {
      const team = teams.get(teamId);
      if (!team || team.disabledAt) return false;
      if (!team.unlimited) {
        const used = parseCreditsToMicro(team.usedCredits);
        const reserved = parseCreditsToMicro(team.reservedCredits);
        const quota = parseCreditsToMicro(team.quotaCredits);
        if (used + reserved + micro > quota) return false;
      }
      team.reservedCredits = formatMicroCredits(
        parseCreditsToMicro(team.reservedCredits) + micro,
      );
      return true;
    },
    async settleUsage(record, reservedMicro) {
      const team = teams.get(record.teamId);
      if (team) {
        team.reservedCredits = formatMicroCredits(
          Math.max(0, parseCreditsToMicro(team.reservedCredits) - reservedMicro),
        );
        team.usedCredits = formatMicroCredits(
          parseCreditsToMicro(team.usedCredits) + record.creditsMicro,
        );
      }
      usage.push(record);
    },
    async releaseReservation(teamId, micro) {
      const team = teams.get(teamId);
      if (!team) return;
      team.reservedCredits = formatMicroCredits(
        Math.max(0, parseCreditsToMicro(team.reservedCredits) - micro),
      );
    },

    async dispose() {
      // 无资源需要释放。
    },
  };

  if (options.demoSeed) {
    await seedDemo(store);
  }
  return store;
}

function matchesFilter(record: UsageRecord, filter: UsageFilter): boolean {
  if (filter.teamId && record.teamId !== filter.teamId) return false;
  if (filter.userId && record.userId !== filter.userId) return false;
  if (filter.from && record.createdAt < filter.from) return false;
  if (filter.to && record.createdAt > filter.to) return false;
  return true;
}

/**
 * 演示数据：与 pnpm seed 的 Postgres 种子保持同样内容，便于无数据库体验完整链路。
 * 密码仅用于本地演示，部署时由 SEED_ADMIN_PASSWORD 控制。
 */
async function seedDemo(store: PlatformStore): Promise<void> {
  const admin = await store.createUser({
    email: "admin@mendao.chat",
    passwordHash: await hashPassword("mendao-admin"),
    displayName: "平台管理员",
    role: "admin",
  });
  const member = await store.createUser({
    email: "member@mendao.chat",
    passwordHash: await hashPassword("mendao-member"),
    displayName: "演示成员",
    role: "member",
  });
  const team = await store.createTeam({
    name: "演示团队",
    quotaCredits: "1000.000000",
    unlimited: false,
  });
  await store.addMember(team.id, admin.id, "owner");
  await store.addMember(team.id, member.id, "member");
  await store.upsertModel({
    modelId: "claude-sonnet-4-5",
    displayName: "Claude Sonnet 4.5",
    inputPricePerMtok: "3",
    outputPricePerMtok: "15",
    cacheReadPricePerMtok: "0.3",
    cacheWritePricePerMtok: "3.75",
    sortOrder: 10,
  });
  await store.upsertModel({
    modelId: "claude-haiku-4-5",
    displayName: "Claude Haiku 4.5",
    inputPricePerMtok: "1",
    outputPricePerMtok: "5",
    cacheReadPricePerMtok: "0.1",
    cacheWritePricePerMtok: "1.25",
    sortOrder: 20,
  });
}
