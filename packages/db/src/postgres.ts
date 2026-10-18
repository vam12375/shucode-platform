import { formatMicroCredits } from "@shucode-platform/contracts/credits";
import postgres from "postgres";
import {
  MODEL_PRICING_KV_KEY,
  computePricingDocVersion,
  EMPTY_PRICING_VERSION,
  parsePricingDoc,
} from "./pricing-doc.js";
import type {
  DeviceTokenRecord,
  LoginFlowRecord,
  PlatformModelRecord,
  PlatformStore,
  PricingDocSnapshot,
  ReleaseRecord,
  SessionRecord,
  TeamMemberView,
  TeamRecord,
  TeamWithUsage,
  UpstreamProviderRecord,
  UsageAggregateRow,
  UsageRecord,
  UserRecord,
} from "./ports.js";

type Row = Record<string, unknown>;

function iso(value: unknown): string {
  if (value instanceof Date) return value.toISOString();
  if (typeof value === "string") return value;
  throw new Error(`无法解析时间字段: ${String(value)}`);
}

function isoNullable(value: unknown): string | null {
  return value === null || value === undefined ? null : iso(value);
}

function num(value: unknown): number {
  if (typeof value === "number") return value;
  if (typeof value === "bigint") return Number(value);
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) throw new Error(`无法解析数值字段: ${String(value)}`);
  return parsed;
}

function numNullable(value: unknown): number | null {
  return value === null || value === undefined ? null : num(value);
}

function str(value: unknown): string {
  if (typeof value === "string") return value;
  throw new Error(`无法解析字符串字段: ${String(value)}`);
}

function strNullable(value: unknown): string | null {
  return value === null || value === undefined ? null : str(value);
}

function mapUser(row: Row): UserRecord {
  return {
    id: str(row.id),
    email: str(row.email),
    passwordHash: str(row.password_hash),
    displayName: str(row.display_name),
    role: str(row.role) as UserRecord["role"],
    disabledAt: isoNullable(row.disabled_at),
    createdAt: iso(row.created_at),
  };
}

function mapTeam(row: Row): TeamRecord {
  return {
    id: str(row.id),
    name: str(row.name),
    quotaCredits: str(row.quota_credits),
    usedCredits: str(row.used_credits),
    reservedCredits: str(row.reserved_credits),
    unlimited: row.unlimited === true,
    createdAt: iso(row.created_at),
    disabledAt: isoNullable(row.disabled_at),
  };
}

function mapDeviceToken(row: Row): DeviceTokenRecord {
  return {
    id: str(row.id),
    userId: str(row.user_id),
    name: str(row.name),
    platform: strNullable(row.platform),
    tokenHash: str(row.token_hash),
    tokenCiphertext: str(row.token_ciphertext),
    tokenHint: str(row.token_hint),
    createdAt: iso(row.created_at),
    lastUsedAt: isoNullable(row.last_used_at),
    revokedAt: isoNullable(row.revoked_at),
  };
}

function mapLoginFlow(row: Row): LoginFlowRecord {
  return {
    id: str(row.id),
    pollTokenHash: str(row.poll_token_hash),
    status: str(row.status) as LoginFlowRecord["status"],
    userId: strNullable(row.user_id),
    deviceName: str(row.device_name),
    devicePlatform: strNullable(row.device_platform),
    createdAt: iso(row.created_at),
    expiresAt: iso(row.expires_at),
    authorizedAt: isoNullable(row.authorized_at),
    consumedAt: isoNullable(row.consumed_at),
    tokenId: strNullable(row.token_id),
    lastPolledAt: isoNullable(row.last_polled_at),
  };
}

function mapRelease(row: Row): ReleaseRecord {
  return {
    revision: num(row.revision),
    content: row.content,
    publishedAt: iso(row.published_at),
    publishedBy: strNullable(row.published_by),
    isActive: row.is_active === true,
  };
}

function mapModel(row: Row): PlatformModelRecord {
  return {
    modelId: str(row.model_id),
    displayName: str(row.display_name),
    upstreamModel: strNullable(row.upstream_model),
    upstreamProviderId: strNullable(row.upstream_provider_id),
    inputPricePerMtok: str(row.input_price_per_mtok),
    outputPricePerMtok: str(row.output_price_per_mtok),
    cacheReadPricePerMtok: str(row.cache_read_price_per_mtok),
    cacheWritePricePerMtok: str(row.cache_write_price_per_mtok),
    enabled: row.enabled === true,
    sortOrder: num(row.sort_order),
    createdAt: iso(row.created_at),
    updatedAt: iso(row.updated_at),
  };
}

function mapProvider(row: Row): UpstreamProviderRecord {
  return {
    id: str(row.id),
    name: str(row.name),
    baseUrl: str(row.base_url),
    protocol: str(row.protocol) as UpstreamProviderRecord["protocol"],
    apiKeyCiphertext: strNullable(row.api_key_ciphertext),
    apiKeyHint: strNullable(row.api_key_hint),
    anthropicVersion: strNullable(row.anthropic_version),
    timeoutMs: numNullable(row.timeout_ms),
    enabled: row.enabled === true,
    disabledMode:
      row.disabled_mode === "manual" || row.disabled_mode === "auto"
        ? (row.disabled_mode as "manual" | "auto")
        : null,
    autoDisabledAt: isoNullable(row.auto_disabled_at),
    autoDisabledReason: strNullable(row.auto_disabled_reason),
    consecutiveFailures: num(row.consecutive_failures ?? 0),
    lastTestAt: isoNullable(row.last_test_at),
    lastTestOk: row.last_test_ok === null ? null : row.last_test_ok === true,
    lastTestKind: strNullable(row.last_test_kind),
    lastTestStatus: numNullable(row.last_test_status),
    lastTestLatencyMs: numNullable(row.last_test_latency_ms),
    lastTestModel: strNullable(row.last_test_model),
    lastTestDetail: strNullable(row.last_test_detail),
    createdAt: iso(row.created_at),
  };
}

function mapUsage(row: Row): UsageRecord {
  return {
    id: str(row.id),
    requestId: str(row.request_id),
    userId: str(row.user_id),
    teamId: str(row.team_id),
    deviceTokenId: strNullable(row.device_token_id),
    model: str(row.model),
    upstreamModel: str(row.upstream_model),
    inputTokens: num(row.input_tokens),
    outputTokens: num(row.output_tokens),
    cacheReadTokens: num(row.cache_read_tokens),
    cacheCreationTokens: num(row.cache_creation_tokens),
    creditsMicro: num(row.credits_micro),
    status: str(row.status) as UsageRecord["status"],
    errorType: strNullable(row.error_type),
    clientIp: strNullable(row.client_ip),
    latencyMs: numNullable(row.latency_ms),
    providerId: strNullable(row.provider_id),
    createdAt: iso(row.created_at),
  };
}

/** Postgres 实现：SQL 直连（postgres.js），语义以 ports.ts 的契约为准。 */
export function createPostgresStore(databaseUrl: string): PlatformStore {
  const sql = postgres(databaseUrl, { max: 10, onnotice: () => {} });

  return {
    // ---- users ----
    async findUserByEmail(email) {
      const rows = await sql`SELECT * FROM users WHERE lower(email) = lower(${email}) LIMIT 1`;
      return rows[0] ? mapUser(rows[0] as Row) : null;
    },
    async findUserById(id) {
      const rows = await sql`SELECT * FROM users WHERE id = ${id}::uuid LIMIT 1`;
      return rows[0] ? mapUser(rows[0] as Row) : null;
    },
    async createUser(input) {
      const rows = await sql`
        INSERT INTO users (email, password_hash, display_name, role)
        VALUES (${input.email.trim().toLowerCase()}, ${input.passwordHash}, ${input.displayName}, ${input.role})
        RETURNING *
      `;
      return mapUser(rows[0] as Row);
    },
    async listUsers() {
      const rows = await sql`SELECT * FROM users ORDER BY created_at ASC`;
      return rows.map((row) => mapUser(row as Row));
    },
    async updateUser(id, patch) {
      const hasDisabled = Object.prototype.hasOwnProperty.call(patch, "disabledAt");
      const disabledAt = patch.disabledAt ?? null;
      const rows = await sql`
        UPDATE users SET
          display_name = COALESCE(${patch.displayName ?? null}, display_name),
          password_hash = COALESCE(${patch.passwordHash ?? null}, password_hash),
          role = COALESCE(${patch.role ?? null}, role),
          disabled_at = CASE WHEN ${hasDisabled} THEN ${disabledAt}::timestamptz ELSE disabled_at END
        WHERE id = ${id}::uuid
        RETURNING *
      `;
      return rows[0] ? mapUser(rows[0] as Row) : null;
    },

    // ---- sessions ----
    async createSession(record: SessionRecord) {
      await sql`
        INSERT INTO sessions (token_hash, user_id, created_at, expires_at, last_seen_at)
        VALUES (${record.tokenHash}, ${record.userId}::uuid, ${record.createdAt}::timestamptz, ${record.expiresAt}::timestamptz, ${record.lastSeenAt}::timestamptz)
      `;
    },
    async findSession(tokenHash) {
      const rows = await sql`SELECT * FROM sessions WHERE token_hash = ${tokenHash} LIMIT 1`;
      const row = rows[0] as Row | undefined;
      if (!row) return null;
      return {
        tokenHash: str(row.token_hash),
        userId: str(row.user_id),
        createdAt: iso(row.created_at),
        expiresAt: iso(row.expires_at),
        lastSeenAt: iso(row.last_seen_at),
      };
    },
    async touchSession(tokenHash, at) {
      await sql`UPDATE sessions SET last_seen_at = ${at}::timestamptz WHERE token_hash = ${tokenHash}`;
    },
    async deleteSession(tokenHash) {
      await sql`DELETE FROM sessions WHERE token_hash = ${tokenHash}`;
    },
    async deleteSessionsForUser(userId) {
      await sql`DELETE FROM sessions WHERE user_id = ${userId}::uuid`;
    },

    // ---- teams ----
    async createTeam(input) {
      const rows = await sql`
        INSERT INTO teams (name, quota_credits, unlimited)
        VALUES (${input.name.trim()}, ${input.quotaCredits}::numeric, ${input.unlimited})
        RETURNING *
      `;
      return mapTeam(rows[0] as Row);
    },
    async getTeam(id) {
      const rows = await sql`SELECT * FROM teams WHERE id = ${id}::uuid LIMIT 1`;
      return rows[0] ? mapTeam(rows[0] as Row) : null;
    },
    async listTeams(): Promise<TeamWithUsage[]> {
      const rows = await sql`
        SELECT t.*, (SELECT count(*)::int FROM team_members m WHERE m.team_id = t.id) AS member_count
        FROM teams t
        ORDER BY t.created_at ASC
      `;
      return rows.map((row) => ({
        team: mapTeam(row as Row),
        memberCount: num((row as Row).member_count),
      }));
    },
    async updateTeam(id, patch) {
      const name = patch.name ?? null;
      const quotaCredits = patch.quotaCredits ?? null;
      const unlimited = patch.unlimited ?? null;
      const hasDisabled = Object.prototype.hasOwnProperty.call(patch, "disabledAt");
      const disabledAt = patch.disabledAt ?? null;
      const rows = await sql`
        UPDATE teams SET
          name = COALESCE(${name}, name),
          quota_credits = COALESCE(${quotaCredits}::numeric, quota_credits),
          unlimited = COALESCE(${unlimited}::boolean, unlimited),
          disabled_at = CASE WHEN ${hasDisabled} THEN ${disabledAt}::timestamptz ELSE disabled_at END
        WHERE id = ${id}::uuid
        RETURNING *
      `;
      return rows[0] ? mapTeam(rows[0] as Row) : null;
    },
    async listMembers(teamId): Promise<TeamMemberView[]> {
      const rows = await sql`
        SELECT u.*, m.role AS member_role, m.created_at AS member_created_at
        FROM team_members m
        JOIN users u ON u.id = m.user_id
        WHERE m.team_id = ${teamId}::uuid
        ORDER BY m.created_at ASC
      `;
      return rows.map((row) => ({
        user: mapUser(row as Row),
        role: str((row as Row).member_role) as TeamMemberView["role"],
        createdAt: iso((row as Row).member_created_at),
      }));
    },
    async addMember(teamId, userId, role) {
      await sql`
        INSERT INTO team_members (team_id, user_id, role)
        VALUES (${teamId}::uuid, ${userId}::uuid, ${role})
        ON CONFLICT (team_id, user_id) DO UPDATE SET role = EXCLUDED.role
      `;
    },
    async removeMember(teamId, userId) {
      const rows = await sql`
        DELETE FROM team_members WHERE team_id = ${teamId}::uuid AND user_id = ${userId}::uuid
        RETURNING team_id
      `;
      return rows.length > 0;
    },
    async listTeamsForUser(userId) {
      const rows = await sql`
        SELECT t.*, m.role AS member_role
        FROM team_members m
        JOIN teams t ON t.id = m.team_id
        WHERE m.user_id = ${userId}::uuid
        ORDER BY m.created_at ASC
      `;
      return rows.map((row) => ({
        team: mapTeam(row as Row),
        role: str((row as Row).member_role) as "owner" | "member",
      }));
    },

    // ---- device tokens ----
    async createDeviceToken(record) {
      await sql`
        INSERT INTO device_tokens (id, user_id, name, platform, token_hash, token_ciphertext, token_hint, created_at)
        VALUES (
          ${record.id}::uuid, ${record.userId}::uuid, ${record.name}, ${record.platform},
          ${record.tokenHash}, ${record.tokenCiphertext}, ${record.tokenHint},
          ${record.createdAt}::timestamptz
        )
      `;
    },
    async findDeviceTokenByHash(tokenHash) {
      const rows = await sql`SELECT * FROM device_tokens WHERE token_hash = ${tokenHash} LIMIT 1`;
      return rows[0] ? mapDeviceToken(rows[0] as Row) : null;
    },
    async findDeviceTokenById(id) {
      const rows = await sql`SELECT * FROM device_tokens WHERE id = ${id}::uuid LIMIT 1`;
      return rows[0] ? mapDeviceToken(rows[0] as Row) : null;
    },
    async listDeviceTokens(userId) {
      const rows = userId
        ? await sql`SELECT * FROM device_tokens WHERE user_id = ${userId}::uuid ORDER BY created_at DESC`
        : await sql`SELECT * FROM device_tokens ORDER BY created_at DESC LIMIT 500`;
      return rows.map((row) => mapDeviceToken(row as Row));
    },
    async touchDeviceToken(id, at) {
      await sql`UPDATE device_tokens SET last_used_at = ${at}::timestamptz WHERE id = ${id}::uuid`;
    },
    async revokeDeviceToken(id, at) {
      await sql`UPDATE device_tokens SET revoked_at = ${at}::timestamptz WHERE id = ${id}::uuid`;
    },
    async revokeDeviceTokensForUser(userId, at) {
      const rows = await sql`
        UPDATE device_tokens SET revoked_at = ${at}::timestamptz
        WHERE user_id = ${userId}::uuid AND revoked_at IS NULL
        RETURNING id
      `;
      return rows.length;
    },

    // ---- login flows ----
    async createLoginFlow(record) {
      await sql`
        INSERT INTO login_flows (
          id, poll_token_hash, status, device_name, device_platform, created_at, expires_at
        ) VALUES (
          ${record.id}::uuid, ${record.pollTokenHash}, ${record.status},
          ${record.deviceName}, ${record.devicePlatform},
          ${record.createdAt}::timestamptz, ${record.expiresAt}::timestamptz
        )
      `;
    },
    async getLoginFlow(id) {
      const rows = await sql`SELECT * FROM login_flows WHERE id = ${id}::uuid LIMIT 1`;
      return rows[0] ? mapLoginFlow(rows[0] as Row) : null;
    },
    async updateLoginFlow(id, patch) {
      // 该表所有补丁字段都是「只前进、不置空」的集合语义，COALESCE 即正确。
      const status = patch.status ?? null;
      const userId = patch.userId ?? null;
      const authorizedAt = patch.authorizedAt ?? null;
      const consumedAt = patch.consumedAt ?? null;
      const tokenId = patch.tokenId ?? null;
      const lastPolledAt = patch.lastPolledAt ?? null;
      const rows = await sql`
        UPDATE login_flows SET
          status = COALESCE(${status}, status),
          user_id = COALESCE(${userId}::uuid, user_id),
          authorized_at = COALESCE(${authorizedAt}::timestamptz, authorized_at),
          consumed_at = COALESCE(${consumedAt}::timestamptz, consumed_at),
          token_id = COALESCE(${tokenId}::uuid, token_id),
          last_polled_at = COALESCE(${lastPolledAt}::timestamptz, last_polled_at)
        WHERE id = ${id}::uuid
        RETURNING *
      `;
      return rows[0] ? mapLoginFlow(rows[0] as Row) : null;
    },
    async consumeLoginFlow(id, consumedAt) {
      const rows = await sql`
        UPDATE login_flows SET status = 'consumed', consumed_at = ${consumedAt}::timestamptz
        WHERE id = ${id}::uuid AND status = 'authorized'
        RETURNING *
      `;
      return rows[0] ? mapLoginFlow(rows[0] as Row) : null;
    },
    async insertRelease(input) {
      return sql.begin(async (tx) => {
        const rows = await tx`
          INSERT INTO builtin_releases (revision, content, published_by, is_active)
          VALUES (
            ${input.revision},
            ${tx.json(input.content as never)},
            ${input.publishedBy}::uuid,
            NOT EXISTS (SELECT 1 FROM builtin_releases WHERE is_active)
          )
          RETURNING *
        `;
        return mapRelease(rows[0] as Row);
      }) as Promise<ReleaseRecord>;
    },
    async listReleases() {
      const rows = await sql`SELECT * FROM builtin_releases ORDER BY revision DESC`;
      return rows.map((row) => mapRelease(row as Row));
    },
    async getRelease(revision) {
      const rows = await sql`SELECT * FROM builtin_releases WHERE revision = ${revision} LIMIT 1`;
      return rows[0] ? mapRelease(rows[0] as Row) : null;
    },
    async getActiveRelease() {
      const rows = await sql`SELECT * FROM builtin_releases WHERE is_active LIMIT 1`;
      return rows[0] ? mapRelease(rows[0] as Row) : null;
    },
    async activateRelease(revision) {
      return sql.begin(async (tx) => {
        await tx`UPDATE builtin_releases SET is_active = false WHERE is_active AND revision <> ${revision}`;
        const rows = await tx`
          UPDATE builtin_releases SET is_active = true WHERE revision = ${revision} RETURNING *
        `;
        return rows[0] ? mapRelease(rows[0] as Row) : null;
      }) as Promise<ReleaseRecord | null>;
    },
    async getMaxReleaseRevision() {
      const rows = await sql`SELECT max(revision) AS max_revision FROM builtin_releases`;
      const value = (rows[0] as Row | undefined)?.max_revision;
      return value === null || value === undefined ? null : num(value);
    },

    // ---- models ----
    async listModels(options) {
      const rows = options?.includeDisabled
        ? await sql`SELECT * FROM platform_models ORDER BY sort_order ASC, model_id ASC`
        : await sql`SELECT * FROM platform_models WHERE enabled ORDER BY sort_order ASC, model_id ASC`;
      return rows.map((row) => mapModel(row as Row));
    },
    async getModel(modelId) {
      const rows = await sql`SELECT * FROM platform_models WHERE model_id = ${modelId} LIMIT 1`;
      return rows[0] ? mapModel(rows[0] as Row) : null;
    },
    async upsertModel(input) {
      // upstreamModel / upstreamProviderId 两个绑定字段遵循「缺省=保持、显式 null=清除」，
      // 与 updateUser 的 disabledAt 同一套 hasOwnProperty CASE 语义。
      const hasUpstreamModel = Object.prototype.hasOwnProperty.call(input, "upstreamModel");
      const hasProviderId = Object.prototype.hasOwnProperty.call(input, "upstreamProviderId");
      const rows = await sql`
        INSERT INTO platform_models (
          model_id, display_name, upstream_model, upstream_provider_id,
          input_price_per_mtok, output_price_per_mtok,
          cache_read_price_per_mtok, cache_write_price_per_mtok,
          enabled, sort_order
        ) VALUES (
          ${input.modelId}, ${input.displayName}, ${input.upstreamModel ?? null},
          ${input.upstreamProviderId ?? null}::uuid,
          ${input.inputPricePerMtok}::numeric, ${input.outputPricePerMtok}::numeric,
          ${input.cacheReadPricePerMtok ?? "0"}::numeric, ${input.cacheWritePricePerMtok ?? "0"}::numeric,
          ${input.enabled ?? true}, ${input.sortOrder ?? 0}
        )
        ON CONFLICT (model_id) DO UPDATE SET
          display_name = EXCLUDED.display_name,
          upstream_model = CASE WHEN ${hasUpstreamModel}
            THEN EXCLUDED.upstream_model ELSE platform_models.upstream_model END,
          upstream_provider_id = CASE WHEN ${hasProviderId}
            THEN EXCLUDED.upstream_provider_id ELSE platform_models.upstream_provider_id END,
          input_price_per_mtok = EXCLUDED.input_price_per_mtok,
          output_price_per_mtok = EXCLUDED.output_price_per_mtok,
          cache_read_price_per_mtok = EXCLUDED.cache_read_price_per_mtok,
          cache_write_price_per_mtok = EXCLUDED.cache_write_price_per_mtok,
          enabled = EXCLUDED.enabled,
          sort_order = EXCLUDED.sort_order,
          updated_at = now()
        RETURNING *
      `;
      return mapModel(rows[0] as Row);
    },
    async deleteModel(modelId) {
      const rows = await sql`DELETE FROM platform_models WHERE model_id = ${modelId} RETURNING model_id`;
      return rows.length > 0;
    },

    // ---- upstream providers ----
    async listUpstreamProviders(options) {
      const rows = options?.includeDisabled
        ? await sql`SELECT * FROM upstream_providers ORDER BY created_at ASC`
        : await sql`SELECT * FROM upstream_providers WHERE enabled ORDER BY created_at ASC`;
      return rows.map((row) => mapProvider(row as Row));
    },
    async getUpstreamProvider(id) {
      const rows = await sql`SELECT * FROM upstream_providers WHERE id = ${id}::uuid LIMIT 1`;
      return rows[0] ? mapProvider(rows[0] as Row) : null;
    },
    async createUpstreamProvider(input) {
      const rows = await sql`
        INSERT INTO upstream_providers (
          name, base_url, protocol, api_key_ciphertext, api_key_hint,
          anthropic_version, timeout_ms, enabled
        ) VALUES (
          ${input.name.trim()}, ${input.baseUrl}, ${input.protocol},
          ${input.apiKeyCiphertext}, ${input.apiKeyHint},
          ${input.anthropicVersion ?? null}, ${input.timeoutMs ?? null}, ${input.enabled ?? true}
        )
        RETURNING *
      `;
      return mapProvider(rows[0] as Row);
    },
    async updateUpstreamProvider(id, patch) {
      // apiKey 两字段与 anthropicVersion/timeoutMs 遵循「缺省=保持、显式 null=清除」；
      // name/baseUrl/protocol/enabled 用 COALESCE（这些字段没有置空场景）。
      const name = patch.name ?? null;
      const baseUrl = patch.baseUrl ?? null;
      const protocol = patch.protocol ?? null;
      const hasKey = Object.prototype.hasOwnProperty.call(patch, "apiKeyCiphertext");
      const anthropicVersion = patch.anthropicVersion ?? null;
      const hasAnthropicVersion = Object.prototype.hasOwnProperty.call(patch, "anthropicVersion");
      const timeoutMs = patch.timeoutMs ?? null;
      const hasTimeout = Object.prototype.hasOwnProperty.call(patch, "timeoutMs");
      const rows = await sql`
        UPDATE upstream_providers SET
          name = COALESCE(${name}, name),
          base_url = COALESCE(${baseUrl}, base_url),
          protocol = COALESCE(${protocol}, protocol),
          api_key_ciphertext = CASE WHEN ${hasKey} THEN ${patch.apiKeyCiphertext ?? null} ELSE api_key_ciphertext END,
          api_key_hint = CASE WHEN ${hasKey} THEN ${patch.apiKeyHint ?? null} ELSE api_key_hint END,
          anthropic_version = CASE WHEN ${hasAnthropicVersion} THEN ${anthropicVersion} ELSE anthropic_version END,
          timeout_ms = CASE WHEN ${hasTimeout} THEN ${timeoutMs} ELSE timeout_ms END,
          enabled = COALESCE(${patch.enabled ?? null}, enabled)
        WHERE id = ${id}::uuid
        RETURNING *
      `;
      return rows[0] ? mapProvider(rows[0] as Row) : null;
    },
    async deleteUpstreamProvider(id) {
      const rows = await sql`DELETE FROM upstream_providers WHERE id = ${id}::uuid RETURNING id`;
      return rows.length > 0;
    },
    async recordProviderTestResult(id, result) {
      await sql`
        UPDATE upstream_providers SET
          last_test_at = ${new Date().toISOString()}::timestamptz,
          last_test_ok = ${result.ok},
          last_test_kind = ${result.kind},
          last_test_status = ${result.status},
          last_test_latency_ms = ${result.latencyMs},
          last_test_model = ${result.model},
          last_test_detail = ${result.detail}
        WHERE id = ${id}::uuid
      `;
    },
    async reportUpstreamFailure(id, reason, threshold) {
      // 单语句原子「计数 + 条件熔断」：并发失败不会漏计，也不会越过人工停用的行。
      const rows = await sql`
        UPDATE upstream_providers SET
          consecutive_failures = consecutive_failures + 1,
          enabled = CASE WHEN consecutive_failures + 1 >= ${threshold} THEN false ELSE enabled END,
          disabled_mode = CASE
            WHEN consecutive_failures + 1 >= ${threshold} THEN 'auto'
            ELSE disabled_mode END,
          auto_disabled_at = CASE
            WHEN consecutive_failures + 1 >= ${threshold} THEN now()
            ELSE auto_disabled_at END,
          auto_disabled_reason = CASE
            WHEN consecutive_failures + 1 >= ${threshold} THEN ${reason}
            ELSE auto_disabled_reason END
        WHERE id = ${id}::uuid AND enabled
        RETURNING disabled_mode
      `;
      return (rows[0] as Row | undefined)?.disabled_mode === "auto";
    },
    async reportUpstreamSuccess(id) {
      await sql`
        UPDATE upstream_providers SET consecutive_failures = 0 WHERE id = ${id}::uuid
      `;
    },

    // ---- model pricing（内容版本乐观锁） ----
    async getModelPricing() {
      const rows = await sql`SELECT value FROM platform_config WHERE key = ${MODEL_PRICING_KV_KEY} LIMIT 1`;
      const row = rows[0] as Row | undefined;
      return row ? parsePricingDoc(row.value) : null;
    },
    async saveModelPricing(entries, expectedVersion) {
      const version = computePricingDocVersion(entries);
      return sql.begin(async (tx) => {
        // advising 锁 + FOR UPDATE：读版本、比版本、写文档在同一临界区，
        // 内容版本即锁依据（旧 KV 数据无 version 列也不影响，版本按内容现算）。
        await tx`SELECT pg_advisory_xact_lock(hashtext(${MODEL_PRICING_KV_KEY}))`;
        const rows = await tx`
          SELECT value FROM platform_config WHERE key = ${MODEL_PRICING_KV_KEY} FOR UPDATE
        `;
        const row = rows[0] as Row | undefined;
        const current = row ? parsePricingDoc(row.value) : null;
        const currentVersion = current ? current.version : EMPTY_PRICING_VERSION;
        if (currentVersion !== expectedVersion) return null;
        await tx`
          INSERT INTO platform_config (key, value, updated_at)
          VALUES (${MODEL_PRICING_KV_KEY}, ${tx.json({ entries } as never)}, now())
          ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()
        `;
        return { entries, version };
      }) as Promise<PricingDocSnapshot | null>;
    },

    // ---- admin audit（审计 + 团队额度调整流水） ----
    async insertAuditLog(entry) {
      await sql`
        INSERT INTO admin_audit_logs (actor_user_id, action, target_type, target_id, detail)
        VALUES (
          ${entry.actorUserId}::uuid, ${entry.action},
          ${entry.targetType}, ${entry.targetId}, ${sql.json(entry.detail as never)}
        )
      `;
    },
    async listAuditLogs(filter) {
      const limit = Math.min(Math.max(filter.limit ?? 50, 1), 500);
      const actorUserId = filter.actorUserId ?? null;
      const cursorCreatedAt = filter.cursorCreatedAt ?? null;
      const cursorId = filter.cursorId ?? null;
      const rows = await sql`
        SELECT a.*, u.email AS actor_email
        FROM admin_audit_logs a
        LEFT JOIN users u ON u.id = a.actor_user_id
        WHERE (${actorUserId}::uuid IS NULL OR a.actor_user_id = ${actorUserId}::uuid)
          AND (
            ${cursorCreatedAt}::timestamptz IS NULL
            OR (a.created_at, a.id) < (${cursorCreatedAt}::timestamptz, ${cursorId}::uuid)
          )
        ORDER BY a.created_at DESC
        LIMIT ${limit}
      `;
      return rows.map((row) => ({
        id: str((row as Row).id),
        actorUserId: strNullable((row as Row).actor_user_id),
        action: str((row as Row).action),
        targetType: strNullable((row as Row).target_type),
        targetId: strNullable((row as Row).target_id),
        detail: (row as Row).detail,
        createdAt: iso((row as Row).created_at),
        actorEmail: strNullable((row as Row).actor_email),
      }));
    },
    async insertQuotaAdjustment(entry) {
      await sql`
        INSERT INTO team_quota_adjustments (
          team_id, actor_user_id, action,
          old_quota_credits, new_quota_credits, old_unlimited, new_unlimited
        ) VALUES (
          ${entry.teamId}::uuid, ${entry.actorUserId}::uuid, ${entry.action},
          ${entry.oldQuotaCredits}::numeric, ${entry.newQuotaCredits}::numeric,
          ${entry.oldUnlimited}, ${entry.newUnlimited}
        )
      `;
    },
    async listQuotaAdjustments(teamId, limit = 10) {
      const rows = await sql`
        SELECT q.*, u.email AS actor_email
        FROM team_quota_adjustments q
        LEFT JOIN users u ON u.id = q.actor_user_id
        WHERE q.team_id = ${teamId}::uuid
        ORDER BY q.created_at DESC
        LIMIT ${Math.min(Math.max(limit, 1), 100)}
      `;
      return rows.map((row) => ({
        id: str((row as Row).id),
        action: str((row as Row).action),
        oldQuotaCredits: str((row as Row).old_quota_credits),
        newQuotaCredits: str((row as Row).new_quota_credits),
        oldUnlimited: (row as Row).old_unlimited === true,
        newUnlimited: (row as Row).new_unlimited === true,
        createdAt: iso((row as Row).created_at),
        actorEmail: strNullable((row as Row).actor_email),
      }));
    },

    // ---- config kv ----
    async getConfigValue(key) {
      const rows = await sql`SELECT value FROM platform_config WHERE key = ${key} LIMIT 1`;
      const row = rows[0] as Row | undefined;
      return row ? row.value : null;
    },
    async setConfigValue(key, value) {
      await sql`
        INSERT INTO platform_config (key, value, updated_at)
        VALUES (${key}, ${sql.json(value as never)}, now())
        ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()
      `;
    },

    // ---- usage ----
    async insertUsage(record) {
      await sql`
        INSERT INTO usage_records (
          id, request_id, user_id, team_id, device_token_id, model, upstream_model,
          input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
          credits_micro, status, error_type, client_ip, latency_ms, provider_id, created_at
        ) VALUES (
          ${record.id}::uuid, ${record.requestId}, ${record.userId}::uuid, ${record.teamId}::uuid,
          ${record.deviceTokenId}::uuid, ${record.model}, ${record.upstreamModel},
          ${record.inputTokens}, ${record.outputTokens}, ${record.cacheReadTokens},
          ${record.cacheCreationTokens}, ${record.creditsMicro}, ${record.status},
          ${record.errorType}, ${record.clientIp}, ${record.latencyMs}, ${record.providerId}::uuid, ${record.createdAt}::timestamptz
        )
      `;
    },
    async listUsage(filter) {
      const limit = Math.min(Math.max(filter.limit ?? 50, 1), 500);
      const from = filter.from ?? null;
      const to = filter.to ?? null;
      const teamId = filter.teamId ?? null;
      const userId = filter.userId ?? null;
      const model = filter.model ?? null;
      const providerId = filter.providerId ?? null;
      const status = filter.status ?? null;
      const cursorCreatedAt = filter.cursorCreatedAt ?? null;
      const cursorId = filter.cursorId ?? null;
      const rows = await sql`
        SELECT * FROM usage_records
        WHERE (${from}::timestamptz IS NULL OR created_at >= ${from}::timestamptz)
          AND (${to}::timestamptz IS NULL OR created_at <= ${to}::timestamptz)
          AND (${teamId}::uuid IS NULL OR team_id = ${teamId}::uuid)
          AND (${userId}::uuid IS NULL OR user_id = ${userId}::uuid)
          AND (${model}::text IS NULL OR model = ${model})
          AND (${providerId}::uuid IS NULL OR provider_id = ${providerId}::uuid)
          AND (${status}::text IS NULL OR status = ${status})
          AND (
            ${cursorCreatedAt}::timestamptz IS NULL
            OR (created_at, id) < (${cursorCreatedAt}::timestamptz, ${cursorId}::uuid)
          )
        ORDER BY created_at DESC
        LIMIT ${limit}
      `;
      return rows.map((row) => mapUsage(row as Row));
    },
    async aggregateUsage(filter): Promise<UsageAggregateRow[]> {
      const from = filter.from ?? null;
      const to = filter.to ?? null;
      const teamId = filter.teamId ?? null;
      const userId = filter.userId ?? null;
      const model = filter.model ?? null;
      const providerId = filter.providerId ?? null;
      const status = filter.status ?? null;
      const rows = await sql`
        SELECT
          model,
          count(*)::int AS requests,
          COALESCE(sum(input_tokens), 0)::bigint AS input_tokens,
          COALESCE(sum(output_tokens), 0)::bigint AS output_tokens,
          COALESCE(sum(cache_read_tokens), 0)::bigint AS cache_read_tokens,
          COALESCE(sum(cache_creation_tokens), 0)::bigint AS cache_creation_tokens,
          COALESCE(sum(credits_micro), 0)::bigint AS credits_micro,
          COALESCE(sum(latency_ms), 0)::bigint AS latency_ms_sum
        FROM usage_records
        WHERE (${from}::timestamptz IS NULL OR created_at >= ${from}::timestamptz)
          AND (${to}::timestamptz IS NULL OR created_at <= ${to}::timestamptz)
          AND (${teamId}::uuid IS NULL OR team_id = ${teamId}::uuid)
          AND (${userId}::uuid IS NULL OR user_id = ${userId}::uuid)
          AND (${model}::text IS NULL OR model = ${model})
          AND (${providerId}::uuid IS NULL OR provider_id = ${providerId}::uuid)
          AND (${status}::text IS NULL OR status = ${status})
        GROUP BY model
        ORDER BY credits_micro DESC
      `;
      return rows.map((row) => ({
        model: str((row as Row).model),
        requests: num((row as Row).requests),
        inputTokens: num((row as Row).input_tokens),
        outputTokens: num((row as Row).output_tokens),
        cacheReadTokens: num((row as Row).cache_read_tokens),
        cacheCreationTokens: num((row as Row).cache_creation_tokens),
        creditsMicro: num((row as Row).credits_micro),
        latencyMsSum: num((row as Row).latency_ms_sum),
      }));
    },

    // 时间桶按上海墙钟切：created_at 转到 Asia/Shanghai 后 date_trunc，
    // to_char 输出 naive 串（不带时区），与展示端「上海时间」语义一致。
    async aggregateUsageSeries(filter, granularity) {
      const field = granularity === "hour" ? "hour" : "day";
      const from = filter.from ?? null;
      const to = filter.to ?? null;
      const teamId = filter.teamId ?? null;
      const userId = filter.userId ?? null;
      const model = filter.model ?? null;
      const providerId = filter.providerId ?? null;
      const status = filter.status ?? null;
      const rows = await sql`
        SELECT
          to_char(
            date_trunc(${field}, created_at AT TIME ZONE 'Asia/Shanghai'),
            'YYYY-MM-DD"T"HH24:MI:SS'
          ) AS bucket_start,
          count(*)::int AS requests,
          COALESCE(sum(input_tokens), 0)::bigint AS input_tokens,
          COALESCE(sum(output_tokens), 0)::bigint AS output_tokens,
          COALESCE(sum(cache_read_tokens), 0)::bigint AS cache_read_tokens,
          COALESCE(sum(cache_creation_tokens), 0)::bigint AS cache_creation_tokens,
          COALESCE(sum(credits_micro), 0)::bigint AS credits_micro,
          COALESCE(sum(latency_ms), 0)::bigint AS latency_ms_sum
        FROM usage_records
        WHERE (${from}::timestamptz IS NULL OR created_at >= ${from}::timestamptz)
          AND (${to}::timestamptz IS NULL OR created_at <= ${to}::timestamptz)
          AND (${teamId}::uuid IS NULL OR team_id = ${teamId}::uuid)
          AND (${userId}::uuid IS NULL OR user_id = ${userId}::uuid)
          AND (${model}::text IS NULL OR model = ${model})
          AND (${providerId}::uuid IS NULL OR provider_id = ${providerId}::uuid)
          AND (${status}::text IS NULL OR status = ${status})
        GROUP BY bucket_start
        ORDER BY bucket_start ASC
      `;
      return rows.map((row) => ({
        bucketStart: str((row as Row).bucket_start),
        requests: num((row as Row).requests),
        inputTokens: num((row as Row).input_tokens),
        outputTokens: num((row as Row).output_tokens),
        cacheReadTokens: num((row as Row).cache_read_tokens),
        cacheCreationTokens: num((row as Row).cache_creation_tokens),
        creditsMicro: num((row as Row).credits_micro),
        latencyMsSum: num((row as Row).latency_ms_sum),
      }));
    },

    // ---- quota ----
    async tryReserveQuota(teamId, micro) {
      const credits = formatMicroCredits(micro);
      const rows = await sql`
        UPDATE teams
        SET reserved_credits = reserved_credits + ${credits}::numeric
        WHERE id = ${teamId}::uuid
          AND disabled_at IS NULL
          AND (unlimited OR used_credits + reserved_credits + ${credits}::numeric <= quota_credits)
        RETURNING id
      `;
      return rows.length > 0;
    },
    async settleUsage(record, reservedMicro) {
      const reservedCredits = formatMicroCredits(reservedMicro);
      const actualCredits = formatMicroCredits(record.creditsMicro);
      await sql.begin(async (tx) => {
        await tx`
          UPDATE teams SET
            reserved_credits = GREATEST(reserved_credits - ${reservedCredits}::numeric, 0),
            used_credits = used_credits + ${actualCredits}::numeric
          WHERE id = ${record.teamId}::uuid
        `;
        await tx`
          INSERT INTO usage_records (
            id, request_id, user_id, team_id, device_token_id, model, upstream_model,
            input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
            credits_micro, status, error_type, client_ip, latency_ms, provider_id, created_at
          ) VALUES (
            ${record.id}::uuid, ${record.requestId}, ${record.userId}::uuid, ${record.teamId}::uuid,
            ${record.deviceTokenId}::uuid, ${record.model}, ${record.upstreamModel},
            ${record.inputTokens}, ${record.outputTokens}, ${record.cacheReadTokens},
            ${record.cacheCreationTokens}, ${record.creditsMicro}, ${record.status},
            ${record.errorType}, ${record.clientIp}, ${record.latencyMs}, ${record.providerId}::uuid, ${record.createdAt}::timestamptz
          )
        `;
      });
    },
    async releaseReservation(teamId, micro) {
      const credits = formatMicroCredits(micro);
      await sql`
        UPDATE teams SET reserved_credits = GREATEST(reserved_credits - ${credits}::numeric, 0)
        WHERE id = ${teamId}::uuid
      `;
    },

    async dispose() {
      await sql.end({ timeout: 5 });
    },
  };
}
