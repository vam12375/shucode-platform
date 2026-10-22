CREATE TABLE team_asset_deletion_requests (
  team_id uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  id uuid NOT NULL,
  asset_id uuid NOT NULL,
  asset_name text NOT NULL CHECK (char_length(asset_name) BETWEEN 1 AND 255),
  requested_by_id uuid NOT NULL,
  requested_by_name text NOT NULL CHECK (char_length(requested_by_name) <= 255),
  created_at timestamptz NOT NULL,
  status text NOT NULL CHECK (status IN ('pending','approved','rejected')),
  reviewed_by_id uuid,
  reviewed_by_name text NOT NULL DEFAULT '' CHECK (char_length(reviewed_by_name) <= 255),
  reviewed_at timestamptz,
  blob_id uuid NOT NULL,
  size bigint NOT NULL CHECK (size BETWEEN 1 AND 104857600),
  blob_cleaned_at timestamptz,
  PRIMARY KEY (team_id,id),
  CHECK ((status='pending' AND reviewed_by_id IS NULL AND reviewed_at IS NULL) OR
         (status<>'pending' AND reviewed_by_id IS NOT NULL AND reviewed_at IS NOT NULL)),
  CHECK (blob_cleaned_at IS NULL OR status='approved')
);
CREATE UNIQUE INDEX team_asset_deletions_pending_idx
  ON team_asset_deletion_requests(team_id,asset_id) WHERE status='pending';
CREATE INDEX team_asset_deletions_list_idx
  ON team_asset_deletion_requests(team_id,created_at DESC,id DESC);
CREATE INDEX team_asset_deletions_approved_asset_idx
  ON team_asset_deletion_requests(team_id,asset_id) WHERE status='approved';
CREATE INDEX team_asset_deletions_approved_blob_idx
  ON team_asset_deletion_requests(team_id,blob_id) WHERE status='approved';
CREATE INDEX team_asset_deletions_reviewed_idx
  ON team_asset_deletion_requests(team_id,reviewed_at DESC,id DESC) WHERE status<>'pending';
CREATE INDEX team_asset_deletions_cleanup_idx
  ON team_asset_deletion_requests(team_id,id) WHERE status='approved' AND blob_cleaned_at IS NULL;
