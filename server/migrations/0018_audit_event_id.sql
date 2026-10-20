-- 审计可靠 outbox：本地 spool 重放时用 event_id 去重，避免数据库已写入但
-- 客户端收到错误后重复插入。历史记录保持 NULL。
ALTER TABLE admin_audit_logs ADD COLUMN event_id text;
CREATE UNIQUE INDEX admin_audit_logs_event_id_unique
  ON admin_audit_logs (event_id)
  WHERE event_id IS NOT NULL;
