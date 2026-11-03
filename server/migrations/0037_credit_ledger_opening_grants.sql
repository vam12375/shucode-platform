CREATE UNIQUE INDEX IF NOT EXISTS team_quota_adjustments_initial_grant_idx
  ON team_quota_adjustments (team_id) WHERE action = 'initial_grant';

-- 历史账户缺少开户事实，按最早调整前快照还原，不能把当前累计额度当作首次入账。
WITH opening AS (
  SELECT t.id AS team_id, t.created_at,
    COALESCE(first_adjustment.old_quota_credits, t.quota_credits) AS quota_credits,
    COALESCE(first_adjustment.old_unlimited, t.unlimited) AS unlimited
  FROM teams t
  LEFT JOIN LATERAL (
    SELECT a.old_quota_credits, a.old_unlimited
    FROM team_quota_adjustments a
    WHERE a.team_id = t.id
    ORDER BY a.created_at ASC, a.id ASC
    LIMIT 1
  ) first_adjustment ON true
  WHERE NOT EXISTS (
    SELECT 1 FROM team_quota_adjustments a
    WHERE a.team_id = t.id AND a.action = 'initial_grant'
  )
)
INSERT INTO team_quota_adjustments
  (team_id, actor_user_id, action, old_quota_credits, new_quota_credits, old_unlimited, new_unlimited, created_at)
SELECT team_id, NULL, 'initial_grant', 0, quota_credits, false, unlimited, created_at
FROM opening
WHERE quota_credits <> 0
ON CONFLICT DO NOTHING;
