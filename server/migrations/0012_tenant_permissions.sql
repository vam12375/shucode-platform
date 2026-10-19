-- 团队管理权限与预算属于成员关系，不能再借全局 admin 账号跨租户管理。
ALTER TABLE team_members DROP CONSTRAINT team_members_role_check;
ALTER TABLE team_members ADD CONSTRAINT team_members_role_check CHECK (role IN ('owner', 'admin', 'member'));
ALTER TABLE team_members ADD COLUMN disabled_at timestamptz;
ALTER TABLE team_members ADD COLUMN monthly_budget_credits numeric(18, 6) CHECK (monthly_budget_credits >= 0);
CREATE INDEX team_members_user_idx ON team_members(user_id);

CREATE TABLE team_policies (
 team_id uuid PRIMARY KEY REFERENCES teams(id) ON DELETE CASCADE,
 daily_budget_credits numeric(18, 6) CHECK (daily_budget_credits >= 0),
 monthly_budget_credits numeric(18, 6) CHECK (monthly_budget_credits >= 0),
 allowed_model_ids text[] NOT NULL DEFAULT '{}',
 budget_alert_percent integer NOT NULL DEFAULT 80 CHECK (budget_alert_percent BETWEEN 1 AND 100),
 updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE quota_reservations ADD COLUMN user_id uuid REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX quota_reservations_user_created_idx ON quota_reservations(user_id, created_at) WHERE status = 'reserved';
ALTER TABLE admin_audit_logs ADD COLUMN team_id uuid REFERENCES teams(id) ON DELETE SET NULL;
CREATE INDEX admin_audit_logs_team_created_idx ON admin_audit_logs(team_id, created_at DESC);

CREATE TABLE team_invitations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 team_id uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
 email text NOT NULL,
 token_hash text NOT NULL UNIQUE,
 created_by uuid REFERENCES users(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL,
 accepted_at timestamptz,
 revoked_at timestamptz
);
CREATE INDEX team_invitations_team_created_idx ON team_invitations(team_id, created_at DESC);
