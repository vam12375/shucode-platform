ALTER TABLE team_asset_deletion_requests
  ADD COLUMN withdrawn_at timestamptz,
  DROP CONSTRAINT team_asset_deletion_requests_status_check,
  DROP CONSTRAINT team_asset_deletion_requests_check,
  ADD CONSTRAINT team_asset_deletion_requests_status_check
    CHECK (status IN ('pending','approved','rejected','withdrawn')),
  ADD CONSTRAINT team_asset_deletion_requests_terminal_check
    CHECK (
      (status='pending' AND reviewed_by_id IS NULL AND reviewed_at IS NULL AND withdrawn_at IS NULL) OR
      (status IN ('approved','rejected') AND reviewed_by_id IS NOT NULL AND reviewed_at IS NOT NULL AND withdrawn_at IS NULL) OR
      (status='withdrawn' AND reviewed_by_id IS NULL AND reviewed_at IS NULL AND withdrawn_at IS NOT NULL)
    );

DROP INDEX team_asset_deletions_reviewed_idx;
CREATE INDEX team_asset_deletions_completed_idx
  ON team_asset_deletion_requests(team_id,COALESCE(withdrawn_at,reviewed_at) DESC,id DESC)
  WHERE status<>'pending';
