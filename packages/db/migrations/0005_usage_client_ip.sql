-- 用量记录新增客户端真实 IP（可空）：网关在记录用量时采集，供控制台审计展示。
-- 历史数据无该值，保持 NULL；网关无法确定对端地址时也为 NULL。
ALTER TABLE usage_records ADD COLUMN client_ip text;
