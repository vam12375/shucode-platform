-- 供应商运维化（New API 借鉴）：测试结果持久化 + 手动/自动停用区分。
-- 人工停用是管理员的决定，自动熔断不得覆盖；两者都体现为 enabled=false，用 disabled_mode 区分原因。

ALTER TABLE upstream_providers
  -- 停用原因：manual（管理员操作）/ auto（网关连续失败熔断）；enabled=true 时为 NULL。
  ADD COLUMN IF NOT EXISTS disabled_mode text,
  -- 自动熔断的现场：时间、原因（如 upstream_502）、触发前的连续失败计数。
  ADD COLUMN IF NOT EXISTS auto_disabled_at timestamptz,
  ADD COLUMN IF NOT EXISTS auto_disabled_reason text,
  ADD COLUMN IF NOT EXISTS consecutive_failures integer NOT NULL DEFAULT 0,
  -- 最近一次「连通/推理测试」结果（控制台发起，密钥不落库）。
  ADD COLUMN IF NOT EXISTS last_test_at timestamptz,
  ADD COLUMN IF NOT EXISTS last_test_ok boolean,
  ADD COLUMN IF NOT EXISTS last_test_kind text,
  ADD COLUMN IF NOT EXISTS last_test_status integer,
  ADD COLUMN IF NOT EXISTS last_test_latency_ms integer,
  -- NULL = 只测了连通鉴权，未做模型推理探测。
  ADD COLUMN IF NOT EXISTS last_test_model text,
  ADD COLUMN IF NOT EXISTS last_test_detail text;
