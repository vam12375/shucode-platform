-- 仅保存每次请求实际提供的思考参数；历史记录保持未知，不按模型默认回填。
ALTER TABLE usage_records
  ADD COLUMN reasoning_effort text,
  ADD COLUMN reasoning_budget_tokens bigint CHECK (reasoning_budget_tokens > 0);

ALTER TABLE pending_settlements
  ADD COLUMN reasoning_effort text,
  ADD COLUMN reasoning_budget_tokens bigint CHECK (reasoning_budget_tokens > 0);
