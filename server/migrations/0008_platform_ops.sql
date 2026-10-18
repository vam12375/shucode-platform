-- 平台运维化：模型路由池、团队级流控、额度预留状态、小时 rollup、告警。
-- 所有改动均为增量；既有 platform_models.upstream_provider_id 继续作为兼容回退。

-- 1) 渠道池：一个模型可绑定多个上游路由，按 priority/weight 调度。
CREATE TABLE upstream_routes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  model_id text NOT NULL REFERENCES platform_models (model_id) ON DELETE CASCADE,
  provider_id uuid NOT NULL REFERENCES upstream_providers (id) ON DELETE CASCADE,
  upstream_model text NOT NULL DEFAULT '',
  priority integer NOT NULL DEFAULT 100,
  weight integer NOT NULL DEFAULT 1 CHECK (weight > 0),
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (model_id, provider_id)
);
CREATE INDEX upstream_routes_model_idx ON upstream_routes (model_id, priority, id);

-- 把现有单绑定迁移成一条默认路由；之后控制台可继续添加备用供应商。
INSERT INTO upstream_routes (model_id, provider_id, upstream_model, priority, weight)
SELECT model_id, upstream_provider_id, COALESCE(NULLIF(upstream_model, ''), model_id), 100, 1
FROM platform_models
WHERE upstream_provider_id IS NOT NULL
ON CONFLICT (model_id, provider_id) DO NOTHING;

-- 2) 团队级流控：0 = 不限制（RPM/TPM/并发）。
ALTER TABLE teams ADD COLUMN rpm_limit integer NOT NULL DEFAULT 0;
ALTER TABLE teams ADD COLUMN tpm_limit integer NOT NULL DEFAULT 0;
ALTER TABLE teams ADD COLUMN concurrency_limit integer NOT NULL DEFAULT 0;

-- 3) 额度预留状态机：request_id 唯一，保证重试不会重复扣费。
CREATE TABLE quota_reservations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  team_id uuid NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
  request_id text NOT NULL,
  reserved_credits numeric(18, 6) NOT NULL,
  status text NOT NULL CHECK (status IN ('reserved', 'settled', 'released', 'expired')),
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  settled_at timestamptz,
  released_at timestamptz,
  usage_id uuid REFERENCES usage_records (id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX quota_reservations_request_unique ON quota_reservations (request_id);
CREATE INDEX quota_reservations_open_idx ON quota_reservations (status, expires_at);
CREATE INDEX quota_reservations_team_created_idx ON quota_reservations (team_id, created_at DESC);

-- 4) 小时汇总：趋势图优先读 rollup，明细维度仍回退 usage_records。
CREATE TABLE usage_hourly_rollups (
  bucket_start timestamptz PRIMARY KEY,
  requests bigint NOT NULL DEFAULT 0,
  input_tokens bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  cache_read_tokens bigint NOT NULL DEFAULT 0,
  cache_creation_tokens bigint NOT NULL DEFAULT 0,
  credits_micro bigint NOT NULL DEFAULT 0,
  latency_ms_sum bigint NOT NULL DEFAULT 0
);

INSERT INTO usage_hourly_rollups (
  bucket_start, requests, input_tokens, output_tokens,
  cache_read_tokens, cache_creation_tokens, credits_micro, latency_ms_sum
)
SELECT
  date_trunc('hour', created_at),
  count(*),
  COALESCE(sum(input_tokens), 0),
  COALESCE(sum(output_tokens), 0),
  COALESCE(sum(cache_read_tokens), 0),
  COALESCE(sum(cache_creation_tokens), 0),
  COALESCE(sum(credits_micro), 0),
  COALESCE(sum(latency_ms), 0)
FROM usage_records
GROUP BY 1
ON CONFLICT (bucket_start) DO NOTHING;

-- 5) 用量检索索引：request_id 精确搜索与供应商维度运维统计。
CREATE INDEX usage_records_request_id_idx ON usage_records (request_id);
CREATE INDEX usage_records_provider_created_idx ON usage_records (provider_id, created_at DESC);
CREATE INDEX usage_records_status_created_idx ON usage_records (status, created_at DESC);

-- 6) 告警规则与事件（站内事件 + 可选 webhook）。
CREATE TABLE admin_alert_rules (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL,
  metric text NOT NULL CHECK (metric IN (
    'error_rate', 'p95_latency_ms', 'open_reservations', 'daily_credits', 'provider_failures'
  )),
  threshold numeric(18, 6) NOT NULL,
  window_minutes integer NOT NULL DEFAULT 15,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE admin_alert_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  rule_id uuid NOT NULL REFERENCES admin_alert_rules (id) ON DELETE CASCADE,
  metric text NOT NULL,
  value numeric(18, 6) NOT NULL,
  threshold numeric(18, 6) NOT NULL,
  message text NOT NULL,
  status text NOT NULL CHECK (status IN ('open', 'resolved')),
  created_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz
);
CREATE INDEX admin_alert_events_open_idx ON admin_alert_events (status, created_at DESC);
CREATE INDEX admin_alert_events_rule_idx ON admin_alert_events (rule_id, status);
