-- 审计游标按(created_at,id)比较；只有时间列会让并列时间依赖不稳定的物理顺序。
-- 新索引同时覆盖 root 全局、操作者筛选与团队筛选的稳定倒序分页。
DROP INDEX IF EXISTS admin_audit_logs_created_at_idx;
DROP INDEX IF EXISTS admin_audit_logs_actor_idx;
DROP INDEX IF EXISTS admin_audit_logs_team_created_idx;

CREATE INDEX admin_audit_logs_created_at_id_idx
  ON admin_audit_logs (created_at DESC, id DESC);
CREATE INDEX admin_audit_logs_actor_created_id_idx
  ON admin_audit_logs (actor_user_id, created_at DESC, id DESC);
CREATE INDEX admin_audit_logs_team_created_id_idx
  ON admin_audit_logs (team_id, created_at DESC, id DESC);
