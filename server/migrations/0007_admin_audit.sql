-- 管理动作可追溯（New API 借鉴）：通用审计日志 + 团队额度调整流水。
-- 审计只记元数据（谁、对谁、做了什么），绝不记录任何凭据明文。

CREATE TABLE admin_audit_logs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  actor_user_id uuid REFERENCES users (id) ON DELETE SET NULL,
  -- 动作命名 <资源>.<动作>：如 user.update / provider.delete / pricing.update。
  action text NOT NULL,
  target_type text,
  target_id text,
  detail jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX admin_audit_logs_created_at_idx ON admin_audit_logs (created_at DESC);
CREATE INDEX admin_audit_logs_actor_idx ON admin_audit_logs (actor_user_id, created_at DESC);

-- 团队额度调整流水：与通用审计分开，保留数值前后快照便于对账。
CREATE TABLE team_quota_adjustments (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  team_id uuid NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
  actor_user_id uuid REFERENCES users (id) ON DELETE SET NULL,
  -- set_quota（含 unlimited 切换）。
  action text NOT NULL,
  old_quota_credits numeric(18, 6) NOT NULL,
  new_quota_credits numeric(18, 6) NOT NULL,
  old_unlimited boolean NOT NULL,
  new_unlimited boolean NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX team_quota_adjustments_team_idx ON team_quota_adjustments (team_id, created_at DESC);
