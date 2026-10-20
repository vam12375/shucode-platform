-- 未知上游成本不得因 HTTP TTL 自动变回可用余额；新网关预留由结算/核销归还。
ALTER TABLE quota_reservations ADD COLUMN retain_until_settled boolean NOT NULL DEFAULT false;
