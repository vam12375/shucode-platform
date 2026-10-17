-- 用量明细游标翻页与按模型聚合的索引支撑。

-- 1) 明细列表：翻页用 (created_at, id) 元组比较 + ORDER BY created_at DESC。
--    (created_at DESC, id DESC) 完全覆盖旧的单列 created_at 索引，直接替换。
DROP INDEX IF EXISTS usage_records_created_at_idx;
CREATE INDEX usage_records_created_at_id_idx ON usage_records (created_at DESC, id DESC);

-- 2) 聚合：GROUP BY model 所需的全部列进索引叶子（index-only scan），
--    无时间窗的全表聚合也不再逐行回表。若用量增长到需要按天预聚合，
--    再引入汇总表，不在本迁移展开。
CREATE INDEX usage_records_aggregate_idx
  ON usage_records (model)
  INCLUDE (created_at, team_id, user_id, input_tokens, output_tokens, credits_micro);
