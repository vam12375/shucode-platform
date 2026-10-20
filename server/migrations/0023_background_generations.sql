CREATE TABLE gateway_background_generations (
  reservation_id uuid PRIMARY KEY REFERENCES quota_reservations (id) ON DELETE CASCADE,
  response_id text NOT NULL,
  provider_id uuid,
  base_url text NOT NULL DEFAULT '',
  team_id uuid NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  record_template jsonb NOT NULL,
  charge_rates jsonb NOT NULL,
  next_poll_at timestamptz NOT NULL DEFAULT now(),
  attempts bigint NOT NULL DEFAULT 0,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  final_response bytea,
  final_status integer,
  final_content_type text,
  completed_at timestamptz,
  UNIQUE (team_id, response_id)
);

CREATE INDEX gateway_background_generations_due_idx
  ON gateway_background_generations (next_poll_at, created_at);
