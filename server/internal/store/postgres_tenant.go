package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 所有成员写入先锁团队，再锁用户；团队锁保护最后一位管理员，用户锁保护单团队约束。
func lockTeam(ctx context.Context, tx pgx.Tx, teamID string) (bool, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM teams WHERE id=$1::uuid FOR UPDATE`, teamID).Scan(&id)
	if queryErrNoRows(err) {
		return false, nil
	}
	return err == nil, err
}

func lockActiveTeam(ctx context.Context, tx pgx.Tx, teamID string) (bool, error) {
	var active bool
	err := tx.QueryRow(ctx, `SELECT disabled_at IS NULL FROM teams WHERE id=$1::uuid FOR UPDATE`, teamID).Scan(&active)
	if queryErrNoRows(err) {
		return false, nil
	}
	return active, err
}
func lockMemberUser(ctx context.Context, tx pgx.Tx, userID string) error {
	var id string
	return tx.QueryRow(ctx, `SELECT id::text FROM users WHERE id=$1::uuid FOR UPDATE`, userID).Scan(&id)
}
func checkSingleTeam(ctx context.Context, tx pgx.Tx, teamID, userID string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM team_members WHERE user_id=$1::uuid AND team_id<>$2::uuid)`, userID, teamID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrUserAlreadyInTeam
	}
	return nil
}
func protectTeamAdmin(ctx context.Context, tx pgx.Tx, teamID, userID string) error {
	var protected bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM team_members m JOIN users target ON target.id=m.user_id WHERE m.team_id=$1::uuid AND m.user_id=$2::uuid AND m.role IN ('owner','admin') AND m.disabled_at IS NULL AND target.disabled_at IS NULL AND NOT EXISTS(SELECT 1 FROM team_members other JOIN users other_user ON other_user.id=other.user_id WHERE other.team_id=m.team_id AND other.user_id<>m.user_id AND other.role IN ('owner','admin') AND other.disabled_at IS NULL AND other_user.disabled_at IS NULL))`, teamID, userID).Scan(&protected)
	if err != nil {
		return err
	}
	if protected {
		return ErrLastTeamAdmin
	}
	return nil
}

func (s *PostgresStore) AddMember(ctx context.Context, teamID, userID string, role TeamMemberRole) error {
	if !IsTeamAdmin(role) && role != MemberRoleMember {
		return fmt.Errorf("成员角色无效")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	ok, err := lockTeam(ctx, tx, teamID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("团队不存在")
	}
	if err := lockMemberUser(ctx, tx, userID); err != nil {
		return err
	}
	if err := checkSingleTeam(ctx, tx, teamID, userID); err != nil {
		return err
	}
	if !IsTeamAdmin(role) {
		if err := protectTeamAdmin(ctx, tx, teamID, userID); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO team_members(team_id,user_id,role) VALUES($1::uuid,$2::uuid,$3) ON CONFLICT(team_id,user_id) DO UPDATE SET role=EXCLUDED.role`, teamID, userID, string(role))
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) RemoveMember(ctx context.Context, teamID, userID string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	ok, err := lockTeam(ctx, tx, teamID)
	if err != nil || !ok {
		return false, err
	}
	if err := lockMemberUser(ctx, tx, userID); err != nil {
		if queryErrNoRows(err) {
			return false, nil
		}
		return false, err
	}
	if err := protectTeamAdmin(ctx, tx, teamID, userID); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM team_members WHERE team_id=$1::uuid AND user_id=$2::uuid`, teamID, userID)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

type tenantRowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func getTeamMember(ctx context.Context, q tenantRowQuerier, teamID, userID string) (*TeamMemberView, error) {
	var record TeamMemberView
	err := q.QueryRow(ctx, `SELECT m.role,m.created_at,m.disabled_at,COALESCE(m.monthly_budget_credits::text,''),u.id::text,u.email,u.password_hash,u.display_name,u.role,u.disabled_at,u.created_at FROM team_members m JOIN users u ON u.id=m.user_id WHERE m.team_id=$1::uuid AND m.user_id=$2::uuid`, teamID, userID).Scan(&record.Role, &record.CreatedAt, &record.MemberDisabledAt, &record.MonthlyBudgetCredits, &record.User.ID, &record.User.Email, &record.User.PasswordHash, &record.User.DisplayName, &record.User.Role, &record.User.DisabledAt, &record.User.CreatedAt)
	if queryErrNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}
func (s *PostgresStore) GetTeamMember(ctx context.Context, teamID, userID string) (*TeamMemberView, error) {
	return getTeamMember(ctx, s.pool, teamID, userID)
}
func (s *PostgresStore) UpdateTeamMember(ctx context.Context, teamID, userID string, patch TeamMemberPatch) (*TeamMemberView, error) {
	if patch.Role != nil && !IsTeamAdmin(*patch.Role) && *patch.Role != MemberRoleMember {
		return nil, fmt.Errorf("成员角色无效")
	}
	var budget any
	if patch.MonthlyBudgetCredits != nil {
		normalized, err := normalizeBudget(*patch.MonthlyBudgetCredits)
		if err != nil {
			return nil, err
		}
		budget = nullIfEmpty(normalized)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	ok, err := lockTeam(ctx, tx, teamID)
	if err != nil || !ok {
		return nil, err
	}
	current, err := getTeamMember(ctx, tx, teamID, userID)
	if err != nil || current == nil {
		return nil, err
	}
	if (patch.Role != nil && !IsTeamAdmin(*patch.Role)) || (patch.HasDisabled && patch.DisabledAt != nil) {
		if err := protectTeamAdmin(ctx, tx, teamID, userID); err != nil {
			return nil, err
		}
	}
	var role any
	if patch.Role != nil {
		role = string(*patch.Role)
	}
	_, err = tx.Exec(ctx, `UPDATE team_members SET role=COALESCE($3,role),disabled_at=CASE WHEN $4::boolean THEN $5::timestamptz ELSE disabled_at END, monthly_budget_credits=CASE WHEN $6::boolean THEN $7::numeric ELSE monthly_budget_credits END WHERE team_id=$1::uuid AND user_id=$2::uuid`, teamID, userID, role, patch.HasDisabled, nullableTime(patch.DisabledAt), patch.MonthlyBudgetCredits != nil, budget)
	if err != nil {
		return nil, err
	}
	record, err := getTeamMember(ctx, tx, teamID, userID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return record, nil
}

func getTeamPolicy(ctx context.Context, q tenantRowQuerier, teamID string) (*TeamPolicyRecord, error) {
	var record TeamPolicyRecord
	err := q.QueryRow(ctx, `SELECT t.id::text,COALESCE(p.daily_budget_credits::text,''),COALESCE(p.monthly_budget_credits::text,''),COALESCE(p.allowed_model_ids,'{}'::text[]),COALESCE(p.budget_alert_percent,80),COALESCE(p.updated_at,t.created_at),COALESCE(p.rpm_limit,0),COALESCE(p.tpm_limit,0),COALESCE(p.concurrency_limit,0) FROM teams t LEFT JOIN team_policies p ON p.team_id=t.id WHERE t.id=$1::uuid`, teamID).Scan(&record.TeamID, &record.DailyBudgetCredits, &record.MonthlyBudgetCredits, &record.AllowedModelIDs, &record.BudgetAlertPercent, &record.UpdatedAt, &record.RPMLimit, &record.TPMLimit, &record.ConcurrencyLimit)
	if queryErrNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}
func (s *PostgresStore) GetTeamPolicy(ctx context.Context, teamID string) (*TeamPolicyRecord, error) {
	return getTeamPolicy(ctx, s.pool, teamID)
}
func (s *PostgresStore) SaveTeamPolicy(ctx context.Context, record TeamPolicyRecord) (*TeamPolicyRecord, error) {
	normalized, err := normalizeTeamPolicy(record)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	ok, err := lockTeam(ctx, tx, record.TeamID)
	if err != nil || !ok {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO team_policies(team_id,daily_budget_credits,monthly_budget_credits,allowed_model_ids,budget_alert_percent,updated_at,rpm_limit,tpm_limit,concurrency_limit) VALUES($1::uuid,$2::numeric,$3::numeric,$4,$5,$6,$7,$8,$9) ON CONFLICT(team_id) DO UPDATE SET daily_budget_credits=EXCLUDED.daily_budget_credits,monthly_budget_credits=EXCLUDED.monthly_budget_credits,allowed_model_ids=EXCLUDED.allowed_model_ids,budget_alert_percent=EXCLUDED.budget_alert_percent,updated_at=EXCLUDED.updated_at,rpm_limit=EXCLUDED.rpm_limit,tpm_limit=EXCLUDED.tpm_limit,concurrency_limit=EXCLUDED.concurrency_limit`, normalized.TeamID, nullIfEmpty(normalized.DailyBudgetCredits), nullIfEmpty(normalized.MonthlyBudgetCredits), normalized.AllowedModelIDs, normalized.BudgetAlertPercent, normalized.UpdatedAt, normalized.RPMLimit, normalized.TPMLimit, normalized.ConcurrencyLimit)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &normalized, nil
}

const invitationCols = `id::text,team_id::text,email,token_hash,COALESCE(created_by::text,''),created_at,expires_at,accepted_at,revoked_at`

func scanInvitation(row pgx.Row) (*TeamInvitationRecord, error) {
	var record TeamInvitationRecord
	err := row.Scan(&record.ID, &record.TeamID, &record.Email, &record.TokenHash, &record.CreatedBy, &record.CreatedAt, &record.ExpiresAt, &record.AcceptedAt, &record.RevokedAt)
	if err != nil {
		return nil, err
	}
	return &record, nil
}
func (s *PostgresStore) CreateTeamInvitation(ctx context.Context, record TeamInvitationRecord) error {
	if record.ID == "" {
		record.ID = newUUID()
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now()
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO team_invitations(id,team_id,email,token_hash,created_by,created_at,expires_at,accepted_at,revoked_at) VALUES($1::uuid,$2::uuid,$3,$4,$5::uuid,$6,$7,$8,$9)`, record.ID, record.TeamID, strings.ToLower(strings.TrimSpace(record.Email)), record.TokenHash, record.CreatedBy, record.CreatedAt, record.ExpiresAt, record.AcceptedAt, record.RevokedAt)
	return err
}
func (s *PostgresStore) ListTeamInvitations(ctx context.Context, teamID string) ([]TeamInvitationRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+invitationCols+` FROM team_invitations WHERE team_id=$1::uuid ORDER BY created_at DESC`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TeamInvitationRecord{}
	for rows.Next() {
		record, err := scanInvitation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *record)
	}
	return out, rows.Err()
}
func (s *PostgresStore) RevokeTeamInvitation(ctx context.Context, teamID, id string, at time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE team_invitations SET revoked_at=$3 WHERE team_id=$1::uuid AND id=$2::uuid AND accepted_at IS NULL AND revoked_at IS NULL`, teamID, id, at)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}
func (s *PostgresStore) AcceptTeamInvitation(ctx context.Context, tokenHash, userID string, at time.Time) (*TeamInvitationRecord, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	record, err := scanInvitation(tx.QueryRow(ctx, `SELECT `+invitationCols+` FROM team_invitations WHERE token_hash=$1 FOR UPDATE`, tokenHash))
	if queryErrNoRows(err) {
		return nil, ErrInvitationInvalid
	}
	if err != nil {
		return nil, err
	}
	if record.AcceptedAt != nil || record.RevokedAt != nil || !record.ExpiresAt.After(at) {
		return nil, ErrInvitationInvalid
	}
	ok, err := lockTeam(ctx, tx, record.TeamID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrInvitationInvalid
	}
	var teamDisabled *time.Time
	if err := tx.QueryRow(ctx, `SELECT disabled_at FROM teams WHERE id=$1::uuid`, record.TeamID).Scan(&teamDisabled); err != nil {
		return nil, err
	}
	if teamDisabled != nil {
		return nil, ErrInvitationInvalid
	}
	var email string
	var userDisabled *time.Time
	if err := tx.QueryRow(ctx, `SELECT email,disabled_at FROM users WHERE id=$1::uuid FOR UPDATE`, userID).Scan(&email, &userDisabled); err != nil {
		if queryErrNoRows(err) {
			return nil, ErrInvitationInvalid
		}
		return nil, err
	}
	if userDisabled != nil {
		return nil, ErrInvitationInvalid
	}
	if !strings.EqualFold(email, record.Email) {
		return nil, ErrInvitationEmailMismatch
	}
	if err := checkSingleTeam(ctx, tx, record.TeamID, userID); err != nil {
		return nil, err
	}
	// 用户锁保证「尚未加入」检查与新增原子，既有成员不能消费邀请。
	var alreadyMember bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM team_members WHERE team_id=$1::uuid AND user_id=$2::uuid)`, record.TeamID, userID).Scan(&alreadyMember); err != nil {
		return nil, err
	}
	if alreadyMember {
		return nil, ErrInvitationInvalid
	}
	if _, err := tx.Exec(ctx, `INSERT INTO team_members(team_id,user_id,role) VALUES($1::uuid,$2::uuid,'member')`, record.TeamID, userID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE team_invitations SET accepted_at=$2 WHERE id=$1::uuid`, record.ID, at); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	record.AcceptedAt = copyTime(&at)
	return record, nil
}

func reservationBudgetAllowed(ctx context.Context, tx pgx.Tx, input ReserveQuotaInput, at time.Time) (ReserveResult, error) {
	var memberBudget string
	if input.UserID != "" {
		member, err := getTeamMember(ctx, tx, input.TeamID, input.UserID)
		if err != nil {
			return "", err
		}
		if member == nil || member.MemberDisabledAt != nil || member.User.DisabledAt != nil {
			return ReserveDeniedInactive, nil
		}
		memberBudget = member.MonthlyBudgetCredits
	}
	policy, err := getTeamPolicy(ctx, tx, input.TeamID)
	if err != nil {
		return "", err
	}
	if policy == nil {
		return ReserveDeniedBudget, nil
	}
	if memberBudget == "" && policy.DailyBudgetCredits == "" && policy.MonthlyBudgetCredits == "" {
		if input.Micro >= 0 {
			return ReserveReserved, nil
		}
		return ReserveDeniedBudget, nil
	}
	day, endDay, month, endMonth := budgetWindow(at)
	var daily, monthly, personal int64
	// 后付费只统计已结算用量；旧预留模式仍计算跨日/月在途占款，保持历史兼容。
	err = tx.QueryRow(ctx, `WITH costs AS (
 SELECT created_at,user_id,credits_micro::numeric AS micro,false AS active FROM usage_records WHERE team_id=$1::uuid AND created_at >= $4 AND created_at < $5
 UNION ALL SELECT created_at,user_id,reserved_credits*1000000,true FROM quota_reservations WHERE team_id=$1::uuid AND status='reserved' AND NOT $7
 ) SELECT COALESCE(sum(micro) FILTER(WHERE active OR (created_at >= $2 AND created_at < $3)),0)::bigint,COALESCE(sum(micro),0)::bigint,COALESCE(sum(micro) FILTER(WHERE user_id=$6::uuid),0)::bigint FROM costs`, input.TeamID, day, endDay, month, endMonth, nullIfEmpty(input.UserID), input.Postpaid).Scan(&daily, &monthly, &personal)
	if err != nil {
		return "", err
	}
	for _, check := range []struct {
		limit string
		spent int64
	}{{policy.DailyBudgetCredits, daily}, {policy.MonthlyBudgetCredits, monthly}, {memberBudget, personal}} {
		allowBudget := budgetAllows
		if input.Postpaid {
			allowBudget = postpaidBudgetAllows
		}
		allowed, err := allowBudget(check.limit, check.spent, input.Micro)
		if err != nil {
			return "", err
		}
		if !allowed {
			return ReserveDeniedBudget, nil
		}
	}
	return ReserveReserved, nil
}

func (s *PostgresStore) FindTeamInvitationByHash(ctx context.Context, tokenHash string) (*TeamInvitationRecord, error) {
	record, err := scanInvitation(s.pool.QueryRow(ctx, `SELECT `+invitationCols+` FROM team_invitations WHERE token_hash=$1`, tokenHash))
	if queryErrNoRows(err) {
		return nil, nil
	}
	return record, err
}
