CREATE TABLE personal_accounts (
 user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE RESTRICT,
 team_id uuid NOT NULL UNIQUE REFERENCES teams(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE user_billing_modes (
 user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 mode text NOT NULL DEFAULT 'team' CHECK (mode IN ('team','personal'))
);
