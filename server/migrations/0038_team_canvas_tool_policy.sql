-- Canvas Agent policy is team-scoped and versioned independently from budget policy.
ALTER TABLE team_policies
  ADD COLUMN canvas_tool_revision bigint NOT NULL DEFAULT 0 CHECK (canvas_tool_revision >= 0),
  ADD COLUMN canvas_tool_enabled boolean NOT NULL DEFAULT false,
  ADD COLUMN canvas_tool_allowed_operations text[] NOT NULL DEFAULT '{}',
  ADD COLUMN canvas_tool_max_quote_micro bigint CHECK (canvas_tool_max_quote_micro BETWEEN 0 AND 1000000000000),
  ADD COLUMN canvas_tool_max_operations_per_day bigint CHECK (canvas_tool_max_operations_per_day BETWEEN 0 AND 1000000000000),
  ADD COLUMN canvas_tool_max_concurrent_operations bigint CHECK (canvas_tool_max_concurrent_operations BETWEEN 0 AND 1000000000000),
  ADD COLUMN canvas_tool_updated_at timestamptz NOT NULL DEFAULT now();
