CREATE TABLE billing_session_bindings (
 id uuid PRIMARY KEY,
 session_id text NOT NULL CHECK(length(session_id) BETWEEN 1 AND 128),
 workspace_identity text NOT NULL CHECK(length(workspace_identity) BETWEEN 1 AND 2048),
 remote_session_id text NOT NULL DEFAULT '',
 parent_session_id text NOT NULL DEFAULT '',
 actor_user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 device_token_id uuid NOT NULL REFERENCES device_tokens(id) ON DELETE RESTRICT,
 billing_account_id uuid NOT NULL REFERENCES teams(id) ON DELETE RESTRICT,
 billing_account_type text NOT NULL CHECK(billing_account_type IN ('personal','team')),
 billing_account_name text NOT NULL,
 created_at timestamptz NOT NULL,
 UNIQUE(session_id,workspace_identity,remote_session_id)
);
ALTER TABLE quota_reservations ADD COLUMN billing_binding_id uuid REFERENCES billing_session_bindings(id) ON DELETE RESTRICT;
ALTER TABLE quota_reservations ADD COLUMN billing_device_token_id uuid REFERENCES device_tokens(id) ON DELETE RESTRICT;
ALTER TABLE usage_records ADD COLUMN billing_binding_id uuid REFERENCES billing_session_bindings(id) ON DELETE RESTRICT;
ALTER TABLE pending_settlements ADD COLUMN billing_binding_id uuid REFERENCES billing_session_bindings(id) ON DELETE RESTRICT;
