-- 结算失败补偿：网关结算重试耗尽后，把完整 usage 持久化到这里，
-- 由 api/gateway 的补偿 worker 幂等重放 SettleReservation。
-- 预留被 TTL 回收后仍可结算（存储层会只记实扣），因此不能因过期丢弃。
CREATE TABLE pending_settlements (
  reservation_id uuid PRIMARY KEY REFERENCES quota_reservations(id) ON DELETE CASCADE,
  request_id text NOT NULL,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  team_id uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  device_token_id uuid REFERENCES device_tokens(id) ON DELETE SET NULL,
  model text NOT NULL,
  upstream_model text NOT NULL,
  provider_id uuid REFERENCES upstream_providers(id) ON DELETE SET NULL,
  input_tokens bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  cache_read_tokens bigint NOT NULL DEFAULT 0,
  cache_creation_tokens bigint NOT NULL DEFAULT 0,
  credits_micro bigint NOT NULL DEFAULT 0,
  price_snapshot jsonb,
  usage_status text NOT NULL CHECK (usage_status IN ('ok', 'error', 'aborted')),
  error_type text,
  client_ip text,
  latency_ms integer,
  usage_created_at timestamptz NOT NULL,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'applied', 'failed')),
  attempts integer NOT NULL DEFAULT 0,
  last_error text,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX pending_settlements_due_idx
  ON pending_settlements (next_attempt_at)
  WHERE status = 'pending';
CREATE INDEX pending_settlements_team_idx
  ON pending_settlements (team_id, created_at DESC);
