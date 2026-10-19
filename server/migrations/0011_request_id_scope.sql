-- 请求幂等键按团队隔离：调用方可自定义 x-request-id，全局唯一会让其他租户
-- 通过猜到/复用 ID 抢占对方的预留（可用性问题）。
DROP INDEX IF EXISTS quota_reservations_request_unique;
CREATE UNIQUE INDEX IF NOT EXISTS quota_reservations_team_request_unique
  ON quota_reservations (team_id, request_id);
CREATE INDEX IF NOT EXISTS usage_records_team_request_idx
  ON usage_records (team_id, request_id);
