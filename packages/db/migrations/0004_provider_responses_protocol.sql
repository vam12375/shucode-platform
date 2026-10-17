-- 供应商协议新增 openai-responses（Responses 专用接入：上游只提供 /v1/responses）。
-- 0003 的 CHECK 只允许 chat 与 anthropic 两个值，需替换约束。

ALTER TABLE upstream_providers DROP CONSTRAINT upstream_providers_protocol_check;
ALTER TABLE upstream_providers ADD CONSTRAINT upstream_providers_protocol_check
  CHECK (protocol IN ('openai-chat-completions', 'openai-responses', 'anthropic-messages'));
