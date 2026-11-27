-- 旧待核对记录没有保存上游任务号；保留 NULL，后续可由同一请求的补偿文件补齐。
ALTER TABLE pending_settlements ADD COLUMN background_response_id text;
