-- 平台控制面初始 schema。
-- 与 packages/db/src/schema.ts（Drizzle 定义）保持一一对应。

CREATE TABLE users (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email text NOT NULL,
  password_hash text NOT NULL,
  display_name text NOT NULL,
  role text NOT NULL CHECK (role IN ('admin', 'member')),
  disabled_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_unique ON users (lower(email));

CREATE TABLE sessions (
  token_hash text PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  last_seen_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE teams (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL UNIQUE,
  quota_credits numeric(18, 6) NOT NULL DEFAULT 0,
  used_credits numeric(18, 6) NOT NULL DEFAULT 0,
  reserved_credits numeric(18, 6) NOT NULL DEFAULT 0,
  unlimited boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  disabled_at timestamptz
);

CREATE TABLE team_members (
  team_id uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role text NOT NULL CHECK (role IN ('owner', 'member')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (team_id, user_id)
);

CREATE TABLE device_tokens (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  platform text,
  token_hash text NOT NULL UNIQUE,
  token_ciphertext text NOT NULL,
  token_hint text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  revoked_at timestamptz
);

CREATE TABLE login_flows (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  poll_token_hash text NOT NULL,
  status text NOT NULL CHECK (status IN ('pending', 'authorized', 'consumed', 'expired', 'denied')),
  user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  device_name text NOT NULL,
  device_platform text,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  authorized_at timestamptz,
  consumed_at timestamptz,
  token_id uuid REFERENCES device_tokens(id) ON DELETE SET NULL,
  last_polled_at timestamptz
);

CREATE TABLE builtin_releases (
  revision integer PRIMARY KEY,
  content jsonb NOT NULL,
  published_at timestamptz NOT NULL DEFAULT now(),
  published_by uuid REFERENCES users(id) ON DELETE SET NULL,
  is_active boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX builtin_releases_single_active ON builtin_releases (is_active) WHERE is_active;

CREATE TABLE platform_models (
  model_id text PRIMARY KEY,
  display_name text NOT NULL,
  upstream_model text,
  input_price_per_mtok numeric(18, 6) NOT NULL DEFAULT 0,
  output_price_per_mtok numeric(18, 6) NOT NULL DEFAULT 0,
  cache_read_price_per_mtok numeric(18, 6) NOT NULL DEFAULT 0,
  cache_write_price_per_mtok numeric(18, 6) NOT NULL DEFAULT 0,
  enabled boolean NOT NULL DEFAULT true,
  sort_order integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE usage_records (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  request_id text NOT NULL,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  team_id uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  device_token_id uuid REFERENCES device_tokens(id) ON DELETE SET NULL,
  model text NOT NULL,
  upstream_model text NOT NULL,
  input_tokens bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  cache_read_tokens bigint NOT NULL DEFAULT 0,
  cache_creation_tokens bigint NOT NULL DEFAULT 0,
  credits_micro bigint NOT NULL DEFAULT 0,
  status text NOT NULL CHECK (status IN ('ok', 'error', 'aborted')),
  error_type text,
  latency_ms integer,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX usage_records_created_at_idx ON usage_records (created_at DESC);
CREATE INDEX usage_records_team_idx ON usage_records (team_id, created_at DESC);
CREATE INDEX usage_records_user_idx ON usage_records (user_id, created_at DESC);

CREATE TABLE platform_config (
  key text PRIMARY KEY,
  value jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
