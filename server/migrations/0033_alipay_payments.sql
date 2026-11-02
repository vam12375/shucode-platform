CREATE TABLE payment_orders (
 id uuid PRIMARY KEY,
 out_trade_no text NOT NULL UNIQUE CHECK (out_trade_no ~ '^[A-Za-z0-9_-]{1,64}$'),
 provider text NOT NULL CHECK (provider = 'alipay'),
 configuration_id uuid NOT NULL,
 target_type text NOT NULL CHECK (target_type IN ('personal','team')),
 target_id uuid NOT NULL,
 team_id uuid NOT NULL REFERENCES teams(id) ON DELETE RESTRICT,
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 app_id text NOT NULL,
 seller_id text NOT NULL,
 amount_fen bigint NOT NULL CHECK (amount_fen > 0 AND amount_fen <= 99999999999999),
 credit_micro bigint NOT NULL CHECK (credit_micro = amount_fen * 10000),
 subject text NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','paid','closed')),
 trade_no text,
 notify_payload jsonb,
 test_payment boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL CHECK (expires_at > created_at),
 paid_at timestamptz,
 closed_at timestamptz,
 CHECK (target_type <> 'team' OR target_id = team_id),
 CHECK (target_type <> 'personal' OR target_id = user_id),
 CHECK ((status='pending' AND paid_at IS NULL AND closed_at IS NULL AND trade_no IS NULL) OR
        (status='paid' AND paid_at IS NOT NULL AND closed_at IS NULL AND trade_no IS NOT NULL) OR
        (status='closed' AND paid_at IS NULL AND closed_at IS NOT NULL AND trade_no IS NULL))
);
CREATE UNIQUE INDEX payment_orders_provider_trade_unique ON payment_orders(provider,trade_no) WHERE trade_no IS NOT NULL;
CREATE INDEX payment_orders_team_created_idx ON payment_orders(team_id,created_at DESC,id DESC);
CREATE INDEX payment_orders_user_created_idx ON payment_orders(user_id,created_at DESC,id DESC);
CREATE INDEX payment_orders_target_created_idx ON payment_orders(target_type,target_id,created_at DESC,id DESC);
CREATE INDEX payment_orders_pending_created_idx ON payment_orders(created_at DESC,id DESC) WHERE status='pending';
