ALTER TABLE team_policies
    ADD COLUMN policy_revision bigint NOT NULL DEFAULT 0 CHECK (policy_revision >= 0);
