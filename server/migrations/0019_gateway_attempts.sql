-- 上游尝试轨迹：故障转移时保留每个候选的供应商、状态码、供应商请求 ID
-- 与最终 outcome，用于核对“上游已扣费但平台重试/最终结算”的请求。
CREATE TABLE gateway_attempts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  request_id text NOT NULL,
  reservation_id uuid NOT NULL REFERENCES quota_reservations(id) ON DELETE CASCADE,
  attempt integer NOT NULL,
  provider_id uuid REFERENCES upstream_providers(id) ON DELETE SET NULL,
  upstream_model text NOT NULL,
  base_host text NOT NULL,
  status_code integer,
  error_type text,
  provider_request_id text,
  started_at timestamptz NOT NULL,
  duration_ms integer NOT NULL,
  final boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX gateway_attempts_request_idx
  ON gateway_attempts (request_id, created_at DESC);
CREATE INDEX gateway_attempts_reservation_idx
  ON gateway_attempts (reservation_id, attempt);
CREATE INDEX gateway_attempts_created_idx
  ON gateway_attempts (created_at DESC);
