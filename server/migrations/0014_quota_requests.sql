-- 额度申请仅代表人工授信申请/审批，不记录已付款状态，不接入支付供应商。
CREATE TABLE team_quota_requests (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 team_id uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
 requested_by uuid NOT NULL REFERENCES users(id),
 amount_credits numeric(18,6) NOT NULL CHECK(amount_credits>0),
 reason text NOT NULL CHECK(char_length(reason) BETWEEN 1 AND 500),
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','approved','rejected')),
 reviewed_by uuid REFERENCES users(id),
 review_note text NOT NULL DEFAULT '' CHECK(char_length(review_note)<=500),
 created_at timestamptz NOT NULL DEFAULT now(),
 reviewed_at timestamptz,
 CONSTRAINT team_quota_request_review_state CHECK(
  (status='pending' AND reviewed_by IS NULL AND reviewed_at IS NULL)
  OR (status IN ('approved','rejected') AND reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL)
 )
);
CREATE INDEX team_quota_requests_team_created_idx ON team_quota_requests(team_id,created_at DESC);
CREATE INDEX team_quota_requests_status_created_idx ON team_quota_requests(status,created_at DESC);
