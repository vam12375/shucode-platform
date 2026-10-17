-- 多上游：上游供应商成为一等配置对象；模型绑定供应商，网关按绑定转发。

CREATE TABLE upstream_providers (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL UNIQUE,
  base_url text NOT NULL,
  -- 同构透传（阶段一）：provider 协议须与请求协议一致，跨协议翻译是后续项。
  protocol text NOT NULL DEFAULT 'openai-chat-completions'
    CHECK (protocol IN ('openai-chat-completions', 'anthropic-messages')),
  -- 上游 key 以 AES-256-GCM 密文落库（PLATFORM_SECRET_KEY），不回显明文。
  api_key_ciphertext text,
  api_key_hint text,
  anthropic_version text,
  -- 可空：留空用网关全局 UPSTREAM_TIMEOUT_MS / UPSTREAM_IDLE_TIMEOUT_MS。
  timeout_ms integer,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- 模型绑定：空 = 回落网关 env 的单上游（兼容既有部署）。
ALTER TABLE platform_models
  ADD COLUMN upstream_provider_id uuid REFERENCES upstream_providers (id) ON DELETE SET NULL;

-- 计量带 provider 维度（按供应商汇总的成本口径）。
ALTER TABLE usage_records
  ADD COLUMN provider_id uuid REFERENCES upstream_providers (id) ON DELETE SET NULL;
