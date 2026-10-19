-- 历史记录保持 NULL，不能以当前定价回填旧请求的成本与毛利。
-- 快照包含请求时的成交单价及参考成本，仍与用量和额度在同一事务落库。
ALTER TABLE usage_records ADD COLUMN price_snapshot jsonb;
