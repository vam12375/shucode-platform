ALTER TABLE teams
  ADD COLUMN team_asset_used_bytes bigint NOT NULL DEFAULT 0 CHECK (team_asset_used_bytes >= 0);

UPDATE teams AS team
SET team_asset_used_bytes = totals.used_bytes
FROM (
  SELECT team_id, sum(size)::bigint AS used_bytes
  FROM team_assets
  GROUP BY team_id
) AS totals
WHERE team.id = totals.team_id;
