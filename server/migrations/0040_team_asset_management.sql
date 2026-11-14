ALTER TABLE teams ADD COLUMN team_asset_revision bigint NOT NULL DEFAULT 0;
ALTER TABLE team_assets ADD COLUMN tags text[] NOT NULL DEFAULT '{}';
ALTER TABLE team_assets ADD CONSTRAINT team_asset_tags_limit CHECK (cardinality(tags) <= 20);
CREATE INDEX team_assets_tags_idx ON team_assets USING gin(tags);
CREATE INDEX team_assets_kind_page_idx ON team_assets(team_id, kind, created_at DESC, id DESC);
CREATE INDEX team_assets_uploader_page_idx ON team_assets(team_id, uploaded_by_id, created_at DESC, id DESC);
CREATE TABLE team_asset_favorites (
 team_id uuid NOT NULL,
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 asset_id uuid NOT NULL,
 PRIMARY KEY(team_id,user_id,asset_id),
 FOREIGN KEY(team_id,asset_id) REFERENCES team_assets(team_id,id) ON DELETE CASCADE
);
