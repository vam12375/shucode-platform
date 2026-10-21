CREATE TABLE team_assets (
  team_id uuid NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
  id uuid NOT NULL,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 255),
  kind text NOT NULL CHECK (kind IN ('website', 'document', 'image', 'video', 'audio', 'code', 'other')),
  mime text NOT NULL CHECK (char_length(mime) BETWEEN 1 AND 255),
  size bigint NOT NULL CHECK (size BETWEEN 1 AND 104857600),
  created_at timestamptz NOT NULL,
  uploaded_by_id uuid NOT NULL,
  uploaded_by_name text NOT NULL,
  sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  blob_id uuid NOT NULL,
  PRIMARY KEY (team_id, id),
  UNIQUE (team_id, blob_id)
);

CREATE INDEX team_assets_page_idx
  ON team_assets (team_id, created_at DESC, id DESC);
