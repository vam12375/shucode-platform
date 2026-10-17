/**
 * 平台数据层的端口（Port）定义。
 *
 * API 控制面与 Gateway 只依赖本接口；Postgres 实现（postgres.ts）与
 * 内存实现（memory.ts）必须保持相同的语义，特别是配额临界区：
 * - tryReserveQuota 必须是原子的「检查 + 预留」，并发下不得超发；
 * - settleUsage 必须同时完成账本写入与「预留转实扣」；
 * - 数值一律用定点字符串（numeric(18,6) 口径），micro-credits 用 number。
 */

export type UserRole = "admin" | "member";
export type TeamMemberRole = "owner" | "member";
export type LoginFlowStatus = "pending" | "authorized" | "consumed" | "expired" | "denied";
export type UsageStatus = "ok" | "error" | "aborted";
/**
 * 上游供应商的转发协议（阶段一同构透传）。
 * - openai-chat-completions：OpenAI 兼容主机，/v1/chat/completions 与 /v1/responses 两条路由都透传；
 * - openai-responses：Responses 专用接入（上游只提供 /v1/responses），不接受 chat 请求；
 * - anthropic-messages：Anthropic 协议主机。
 */
export type UpstreamProtocol =
  | "openai-chat-completions"
  | "openai-responses"
  | "anthropic-messages";

export interface UpstreamProviderRecord {
  id: string;
  name: string;
  baseUrl: string;
  protocol: UpstreamProtocol;
  /** AES-256-GCM 密文（PLATFORM_SECRET_KEY）；控制台只回显掩码，明文不出库。 */
  apiKeyCiphertext: string | null;
  apiKeyHint: string | null;
  anthropicVersion: string | null;
  /** 可空：留空回落网关全局 UPSTREAM_TIMEOUT_MS / UPSTREAM_IDLE_TIMEOUT_MS。 */
  timeoutMs: number | null;
  enabled: boolean;
  createdAt: string;
}

export interface UserRecord {
  id: string;
  email: string;
  passwordHash: string;
  displayName: string;
  role: UserRole;
  disabledAt: string | null;
  createdAt: string;
}

export interface SessionRecord {
  tokenHash: string;
  userId: string;
  createdAt: string;
  expiresAt: string;
  lastSeenAt: string;
}

export interface TeamRecord {
  id: string;
  name: string;
  /** numeric(18,6) 定点字符串。 */
  quotaCredits: string;
  usedCredits: string;
  reservedCredits: string;
  unlimited: boolean;
  createdAt: string;
  disabledAt: string | null;
}

export interface TeamMemberView {
  user: UserRecord;
  role: TeamMemberRole;
  createdAt: string;
}

export interface TeamWithUsage {
  team: TeamRecord;
  memberCount: number;
}

export interface DeviceTokenRecord {
  id: string;
  userId: string;
  name: string;
  platform: string | null;
  tokenHash: string;
  /** AES-256-GCM 密文；user-config 需要重新下发该 token 时解密。 */
  tokenCiphertext: string;
  tokenHint: string;
  createdAt: string;
  lastUsedAt: string | null;
  revokedAt: string | null;
}

export interface LoginFlowRecord {
  id: string;
  pollTokenHash: string;
  status: LoginFlowStatus;
  userId: string | null;
  deviceName: string;
  devicePlatform: string | null;
  createdAt: string;
  expiresAt: string;
  authorizedAt: string | null;
  consumedAt: string | null;
  tokenId: string | null;
  lastPolledAt: string | null;
}

export interface ReleaseRecord {
  revision: number;
  content: unknown;
  publishedAt: string;
  publishedBy: string | null;
  isActive: boolean;
}

export interface PlatformModelRecord {
  modelId: string;
  displayName: string;
  upstreamModel: string | null;
  /** 绑定的上游供应商；null = 回落网关 env 单上游。 */
  upstreamProviderId: string | null;
  inputPricePerMtok: string;
  outputPricePerMtok: string;
  cacheReadPricePerMtok: string;
  cacheWritePricePerMtok: string;
  enabled: boolean;
  sortOrder: number;
  createdAt: string;
  updatedAt: string;
}

export interface UsageRecord {
  id: string;
  requestId: string;
  userId: string;
  teamId: string;
  deviceTokenId: string | null;
  model: string;
  upstreamModel: string;
  /** 请求实际命中的上游供应商；null = env 单上游或历史数据。 */
  providerId: string | null;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  creditsMicro: number;
  status: UsageStatus;
  errorType: string | null;
  latencyMs: number | null;
  createdAt: string;
}

export interface UsageFilter {
  from?: string;
  to?: string;
  teamId?: string;
  userId?: string;
  /** listUsage 专用，默认 50，最大 500。 */
  limit?: number;
  /**
   * listUsage 游标（ created_at, id 元组）：取严格排在该行之前（更新）的记录，
   * 实现稳定的「下一页」语义——新写入的记录不影响已有翻页位置。
   * 两字段必须同时设置；aggregateUsage 不消费。
   */
  cursorCreatedAt?: string;
  cursorId?: string;
}

export interface UsageAggregateRow {
  model: string;
  requests: number;
  inputTokens: number;
  outputTokens: number;
  creditsMicro: number;
}

export interface PlatformStore {
  // ---- users ----
  findUserByEmail(email: string): Promise<UserRecord | null>;
  findUserById(id: string): Promise<UserRecord | null>;
  createUser(input: {
    email: string;
    passwordHash: string;
    displayName: string;
    role: UserRole;
  }): Promise<UserRecord>;
  listUsers(): Promise<UserRecord[]>;
  /** 管理端用户 patch：未提供的字段保持原值（disabledAt 传 null 表示启用）。 */
  updateUser(
    id: string,
    patch: {
      disabledAt?: string | null;
      displayName?: string;
      passwordHash?: string;
      role?: UserRole;
    },
  ): Promise<UserRecord | null>;

  // ---- sessions ----
  createSession(record: SessionRecord): Promise<void>;
  findSession(tokenHash: string): Promise<SessionRecord | null>;
  touchSession(tokenHash: string, at: string): Promise<void>;
  deleteSession(tokenHash: string): Promise<void>;

  /** 吊销某用户的全部后台会话（重置密码/禁用/降级后旧会话必须立即失效）。 */
  deleteSessionsForUser(userId: string): Promise<void>;

  // ---- teams ----
  createTeam(input: { name: string; quotaCredits: string; unlimited: boolean }): Promise<TeamRecord>;
  getTeam(id: string): Promise<TeamRecord | null>;
  listTeams(): Promise<TeamWithUsage[]>;
  updateTeam(
    id: string,
    patch: {
      name?: string;
      quotaCredits?: string;
      unlimited?: boolean;
      disabledAt?: string | null;
    },
  ): Promise<TeamRecord | null>;
  listMembers(teamId: string): Promise<TeamMemberView[]>;
  addMember(teamId: string, userId: string, role: TeamMemberRole): Promise<void>;
  removeMember(teamId: string, userId: string): Promise<boolean>;
  listTeamsForUser(userId: string): Promise<Array<{ team: TeamRecord; role: TeamMemberRole }>>;

  // ---- device tokens ----
  createDeviceToken(record: DeviceTokenRecord): Promise<void>;
  findDeviceTokenByHash(tokenHash: string): Promise<DeviceTokenRecord | null>;
  findDeviceTokenById(id: string): Promise<DeviceTokenRecord | null>;
  listDeviceTokens(userId?: string): Promise<DeviceTokenRecord[]>;
  touchDeviceToken(id: string, at: string): Promise<void>;
  revokeDeviceToken(id: string, at: string): Promise<void>;
  /** 吊销某用户的全部设备凭据，返回本次新吊销的条数；已吊销的保持原 revoked_at。 */
  revokeDeviceTokensForUser(userId: string, at: string): Promise<number>;

  // ---- login flows ----
  createLoginFlow(record: LoginFlowRecord): Promise<void>;
  getLoginFlow(id: string): Promise<LoginFlowRecord | null>;
  updateLoginFlow(
    id: string,
    patch: Partial<
      Pick<
        LoginFlowRecord,
        "status" | "userId" | "authorizedAt" | "consumedAt" | "tokenId" | "lastPolledAt"
      >
    >,
  ): Promise<LoginFlowRecord | null>;
  /** 条件消费：仅当 status='authorized' 时置为 consumed 并返回；防止并发轮询重复签发 token。 */
  consumeLoginFlow(id: string, consumedAt: string): Promise<LoginFlowRecord | null>;

  // ---- releases ----
  insertRelease(input: {
    revision: number;
    content: unknown;
    publishedBy: string | null;
  }): Promise<ReleaseRecord>;
  listReleases(): Promise<ReleaseRecord[]>;
  getRelease(revision: number): Promise<ReleaseRecord | null>;
  getActiveRelease(): Promise<ReleaseRecord | null>;
  activateRelease(revision: number): Promise<ReleaseRecord | null>;
  getMaxReleaseRevision(): Promise<number | null>;

  // ---- models ----
  listModels(options?: { includeDisabled?: boolean }): Promise<PlatformModelRecord[]>;
  getModel(modelId: string): Promise<PlatformModelRecord | null>;
  upsertModel(input: {
    modelId: string;
    displayName: string;
    upstreamModel?: string | null;
    /** 显式传 null = 解除绑定回落 env；缺省（undefined）= 保持原绑定。 */
    upstreamProviderId?: string | null;
    inputPricePerMtok: string;
    outputPricePerMtok: string;
    cacheReadPricePerMtok?: string;
    cacheWritePricePerMtok?: string;
    enabled?: boolean;
    sortOrder?: number;
  }): Promise<PlatformModelRecord>;
  deleteModel(modelId: string): Promise<boolean>;

  // ---- upstream providers（多上游） ----
  listUpstreamProviders(options?: { includeDisabled?: boolean }): Promise<UpstreamProviderRecord[]>;
  getUpstreamProvider(id: string): Promise<UpstreamProviderRecord | null>;
  createUpstreamProvider(input: {
    name: string;
    baseUrl: string;
    protocol: UpstreamProtocol;
    apiKeyCiphertext: string | null;
    apiKeyHint: string | null;
    anthropicVersion?: string | null;
    timeoutMs?: number | null;
    enabled?: boolean;
  }): Promise<UpstreamProviderRecord>;
  /** 管理端 patch：apiKey 两字段未提供则保持原值（不提供「清除密钥」）。 */
  updateUpstreamProvider(
    id: string,
    patch: {
      name?: string;
      baseUrl?: string;
      protocol?: UpstreamProtocol;
      apiKeyCiphertext?: string | null;
      apiKeyHint?: string | null;
      anthropicVersion?: string | null;
      timeoutMs?: number | null;
      enabled?: boolean;
    },
  ): Promise<UpstreamProviderRecord | null>;
  deleteUpstreamProvider(id: string): Promise<boolean>;

  // ---- config kv ----
  getConfigValue(key: string): Promise<unknown | null>;
  setConfigValue(key: string, value: unknown): Promise<void>;

  // ---- usage ----
  insertUsage(record: UsageRecord): Promise<void>;
  listUsage(filter: UsageFilter): Promise<UsageRecord[]>;
  aggregateUsage(filter: UsageFilter): Promise<UsageAggregateRow[]>;

  // ---- quota（Gateway 临界区） ----
  tryReserveQuota(teamId: string, micro: number): Promise<boolean>;
  settleUsage(record: UsageRecord, reservedMicro: number): Promise<void>;
  releaseReservation(teamId: string, micro: number): Promise<void>;

  dispose(): Promise<void>;
}
