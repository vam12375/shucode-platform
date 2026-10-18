import {
  boolean,
  index,
  integer,
  jsonb,
  numeric,
  pgTable,
  text,
  timestamp,
  uniqueIndex,
  uuid,
  bigint,
} from "drizzle-orm/pg-core";

/**
 * Drizzle schema 与 migrations/*.sql 保持一一对应。
 * 运行时查询走 postgres.js 直连（见 postgres.ts）；本文件用于类型化引用与
 * 后续 drizzle-kit 迁移生成。
 */

export const users = pgTable(
  "users",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    email: text("email").notNull(),
    passwordHash: text("password_hash").notNull(),
    displayName: text("display_name").notNull(),
    role: text("role").notNull(),
    disabledAt: timestamp("disabled_at", { withTimezone: true }),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  },
  (table) => [uniqueIndex("users_email_unique").on(table.email)],
);

export const sessions = pgTable("sessions", {
  tokenHash: text("token_hash").primaryKey(),
  userId: uuid("user_id")
    .notNull()
    .references(() => users.id, { onDelete: "cascade" }),
  createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  expiresAt: timestamp("expires_at", { withTimezone: true }).notNull(),
  lastSeenAt: timestamp("last_seen_at", { withTimezone: true }).notNull().defaultNow(),
});

export const teams = pgTable("teams", {
  id: uuid("id").primaryKey().defaultRandom(),
  name: text("name").notNull().unique(),
  quotaCredits: numeric("quota_credits", { precision: 18, scale: 6 }).notNull().default("0"),
  usedCredits: numeric("used_credits", { precision: 18, scale: 6 }).notNull().default("0"),
  reservedCredits: numeric("reserved_credits", { precision: 18, scale: 6 }).notNull().default("0"),
  unlimited: boolean("unlimited").notNull().default(false),
  createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  disabledAt: timestamp("disabled_at", { withTimezone: true }),
});

export const teamMembers = pgTable(
  "team_members",
  {
    teamId: uuid("team_id")
      .notNull()
      .references(() => teams.id, { onDelete: "cascade" }),
    userId: uuid("user_id")
      .notNull()
      .references(() => users.id, { onDelete: "cascade" }),
    role: text("role").notNull(),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  },
  (table) => [uniqueIndex("team_members_pk").on(table.teamId, table.userId)],
);

export const deviceTokens = pgTable("device_tokens", {
  id: uuid("id").primaryKey().defaultRandom(),
  userId: uuid("user_id")
    .notNull()
    .references(() => users.id, { onDelete: "cascade" }),
  name: text("name").notNull(),
  platform: text("platform"),
  tokenHash: text("token_hash").notNull().unique(),
  tokenCiphertext: text("token_ciphertext").notNull(),
  tokenHint: text("token_hint").notNull(),
  createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  lastUsedAt: timestamp("last_used_at", { withTimezone: true }),
  revokedAt: timestamp("revoked_at", { withTimezone: true }),
});

export const loginFlows = pgTable("login_flows", {
  id: uuid("id").primaryKey().defaultRandom(),
  pollTokenHash: text("poll_token_hash").notNull(),
  status: text("status").notNull(),
  userId: uuid("user_id").references(() => users.id, { onDelete: "set null" }),
  deviceName: text("device_name").notNull(),
  devicePlatform: text("device_platform"),
  createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  expiresAt: timestamp("expires_at", { withTimezone: true }).notNull(),
  authorizedAt: timestamp("authorized_at", { withTimezone: true }),
  consumedAt: timestamp("consumed_at", { withTimezone: true }),
  tokenId: uuid("token_id").references(() => deviceTokens.id, { onDelete: "set null" }),
  lastPolledAt: timestamp("last_polled_at", { withTimezone: true }),
});

export const builtinReleases = pgTable("builtin_releases", {
  revision: integer("revision").primaryKey(),
  content: jsonb("content").notNull(),
  publishedAt: timestamp("published_at", { withTimezone: true }).notNull().defaultNow(),
  publishedBy: uuid("published_by").references(() => users.id, { onDelete: "set null" }),
  isActive: boolean("is_active").notNull().default(false),
});

export const platformModels = pgTable("platform_models", {
  modelId: text("model_id").primaryKey(),
  displayName: text("display_name").notNull(),
  upstreamModel: text("upstream_model"),
  inputPricePerMtok: numeric("input_price_per_mtok", { precision: 18, scale: 6 })
    .notNull()
    .default("0"),
  outputPricePerMtok: numeric("output_price_per_mtok", { precision: 18, scale: 6 })
    .notNull()
    .default("0"),
  cacheReadPricePerMtok: numeric("cache_read_price_per_mtok", { precision: 18, scale: 6 })
    .notNull()
    .default("0"),
  cacheWritePricePerMtok: numeric("cache_write_price_per_mtok", { precision: 18, scale: 6 })
    .notNull()
    .default("0"),
  enabled: boolean("enabled").notNull().default(true),
  sortOrder: integer("sort_order").notNull().default(0),
  createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow(),
});

export const usageRecords = pgTable(
  "usage_records",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    requestId: text("request_id").notNull(),
    userId: uuid("user_id")
      .notNull()
      .references(() => users.id, { onDelete: "cascade" }),
    teamId: uuid("team_id")
      .notNull()
      .references(() => teams.id, { onDelete: "cascade" }),
    deviceTokenId: uuid("device_token_id").references(() => deviceTokens.id, {
      onDelete: "set null",
    }),
    model: text("model").notNull(),
    upstreamModel: text("upstream_model").notNull(),
    inputTokens: bigint("input_tokens", { mode: "number" }).notNull().default(0),
    outputTokens: bigint("output_tokens", { mode: "number" }).notNull().default(0),
    cacheReadTokens: bigint("cache_read_tokens", { mode: "number" }).notNull().default(0),
    cacheCreationTokens: bigint("cache_creation_tokens", { mode: "number" })
      .notNull()
      .default(0),
    creditsMicro: bigint("credits_micro", { mode: "number" }).notNull().default(0),
    status: text("status").notNull(),
    errorType: text("error_type"),
    clientIp: text("client_ip"),
    latencyMs: integer("latency_ms"),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  },
  (table) => [
    index("usage_records_created_at_idx").on(table.createdAt),
    index("usage_records_team_idx").on(table.teamId, table.createdAt),
    index("usage_records_user_idx").on(table.userId, table.createdAt),
  ],
);

export const platformConfig = pgTable("platform_config", {
  key: text("key").primaryKey(),
  value: jsonb("value").notNull(),
  updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow(),
});
