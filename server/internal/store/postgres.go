package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres 实现:pgx 移植自 packages/db/src/postgres.ts。
// SQL(尤其额度临界区的条件更新、同事务结算、熔断计数)逐条对齐 TS。
// 数值口径:numeric(18,6) 全部以字符串进出;uuid 与 timestamptz 显式 cast。

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgres(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	// 与 postgres.js max:10 对齐,临界区并发行为才有可比性。
	cfg.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &PostgresStore{pool: pool}, nil
}

func (s *PostgresStore) Dispose(_ context.Context) error {
	s.pool.Close()
	return nil
}

// uniqueViolation 判断 Postgres 唯一约束冲突(23505)。
func uniqueViolation(err error) bool {
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		return state.SQLState() == "23505"
	}
	return false
}

func queryErrNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// 用户写操作违反约束时翻译为端口语义错误；root 唯一索引与普通邮箱唯一索引必须区分。
func translateCreateUserError(err error, email string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23505" && pgErr.ConstraintName == "users_single_root":
			return ErrRootUserExists
		case pgErr.Code == "23505":
			return fmt.Errorf("%w: %s", ErrEmailTaken, strings.ToLower(strings.TrimSpace(email)))
		}
	}
	return err
}

func translateUpdateUserError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23505" && pgErr.ConstraintName == "users_single_root":
			return ErrRootUserExists
		case pgErr.Code == "23514" && pgErr.ConstraintName == "users_root_enabled":
			return ErrRootUserProtected
		}
	}
	return err
}

// ---- users ----

const userCols = `id::text, email, password_hash, display_name, role, disabled_at, created_at`

func scanUser(rows pgx.Rows) (*UserRecord, error) {
	var (
		u    UserRecord
		role string
	)
	if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &role, &u.DisabledAt, &u.CreatedAt); err != nil {
		return nil, err
	}
	u.Role = UserRole(role)
	return &u, nil
}

func (s *PostgresStore) FindUserByEmail(ctx context.Context, email string) (*UserRecord, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+userCols+` FROM users WHERE lower(email) = lower($1) LIMIT 1`, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanUser(rows)
}

func (s *PostgresStore) FindUserByID(ctx context.Context, id string) (*UserRecord, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+userCols+` FROM users WHERE id = $1::uuid LIMIT 1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanUser(rows)
}

func (s *PostgresStore) FindRootUser(ctx context.Context) (*UserRecord, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+userCols+` FROM users WHERE role = 'root' LIMIT 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanUser(rows)
}

func (s *PostgresStore) CreateUser(ctx context.Context, input UserInput) (*UserRecord, error) {
	rows, err := s.pool.Query(ctx, `
		INSERT INTO users (email, password_hash, display_name, role)
		VALUES (lower(trim($1)), $2, $3, $4)
		RETURNING `+userCols,
		input.Email, input.PasswordHash, input.DisplayName, string(input.Role))
	if err != nil {
		return nil, translateCreateUserError(err, input.Email)
	}
	defer rows.Close()
	if !rows.Next() {
		// pgx 把唯一约束错误延迟到 rows.Err(),必须在 Next() 之后翻译。
		if err := rows.Err(); err != nil {
			return nil, translateCreateUserError(err, input.Email)
		}
		return nil, nil
	}
	return scanUser(rows)
}

func (s *PostgresStore) ListUsers(ctx context.Context) ([]UserRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserRecord{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateUser(ctx context.Context, id string, patch UpdateUserPatch) (*UserRecord, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 行锁把「判定 root 保护 → 写入」串行化；root 唯一/不可禁用再由 DB 约束兜底。
	var currentRole string
	if err := tx.QueryRow(ctx,
		`SELECT role FROM users WHERE id = $1::uuid FOR UPDATE`, id).Scan(&currentRole); err != nil {
		if queryErrNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	effectiveRole := currentRole
	if patch.Role != nil {
		effectiveRole = string(*patch.Role)
	}
	disabling := patch.HasDisabled && patch.DisabledAt != nil
	if currentRole == string(RoleRoot) &&
		((patch.Role != nil && *patch.Role != RoleRoot) || disabling) {
		return nil, ErrRootUserProtected
	}
	if effectiveRole == string(RoleRoot) && currentRole != string(RoleRoot) {
		var otherRoot bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM users WHERE role = 'root' AND id <> $1::uuid)`, id).Scan(&otherRoot); err != nil {
			return nil, err
		}
		if otherRoot {
			return nil, ErrRootUserExists
		}
	}
	if effectiveRole == string(RoleRoot) && disabling {
		return nil, ErrRootUserProtected
	}

	rows, err := tx.Query(ctx, `
		UPDATE users SET
			display_name = COALESCE($2, display_name),
			password_hash = COALESCE($3, password_hash),
			role = COALESCE($4, role),
			disabled_at = CASE WHEN $5::boolean THEN $6::timestamptz ELSE disabled_at END
		WHERE id = $1::uuid
		RETURNING `+userCols,
		id, patch.DisplayName, patch.PasswordHash, nullableRole(patch.Role),
		patch.HasDisabled, nullableTime(patch.DisabledAt))
	if err != nil {
		return nil, translateUpdateUserError(err)
	}
	if !rows.Next() {
		err := rows.Err()
		rows.Close()
		if err != nil {
			return nil, translateUpdateUserError(err)
		}
		return nil, nil
	}
	user, err := scanUser(rows)
	rows.Close()
	if err != nil {
		return nil, translateUpdateUserError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return user, nil
}

// ---- sessions ----

func (s *PostgresStore) CreateSession(ctx context.Context, record SessionRecord) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (token_hash, user_id, created_at, expires_at, last_seen_at)
		VALUES ($1, $2::uuid, $3::timestamptz, $4::timestamptz, $5::timestamptz)`,
		record.TokenHash, record.UserID, record.CreatedAt, record.ExpiresAt, record.LastSeenAt)
	return err
}

func (s *PostgresStore) FindSession(ctx context.Context, tokenHash string) (*SessionRecord, error) {
	var r SessionRecord
	err := s.pool.QueryRow(ctx, `
		SELECT token_hash, user_id::text, created_at, expires_at, last_seen_at
		FROM sessions WHERE token_hash = $1 LIMIT 1`, tokenHash).
		Scan(&r.TokenHash, &r.UserID, &r.CreatedAt, &r.ExpiresAt, &r.LastSeenAt)
	if err != nil {
		if queryErrNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

func (s *PostgresStore) TouchSession(ctx context.Context, tokenHash string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET last_seen_at = $2::timestamptz WHERE token_hash = $1`, tokenHash, at)
	return err
}

func (s *PostgresStore) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

func (s *PostgresStore) DeleteSessionsForUser(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1::uuid`, userID)
	return err
}

// ---- teams ----

const teamCols = `id::text, name, quota_credits::text, used_credits::text, reserved_credits::text, unlimited,
	rpm_limit, tpm_limit, concurrency_limit, created_at, disabled_at`

func scanTeamTeamRow(rows pgx.Rows) (*TeamRecord, error) {
	var (
		t                TeamRecord
		quota, used, res *string
	)
	if err := rows.Scan(&t.ID, &t.Name, &quota, &used, &res, &t.Unlimited,
		&t.RPMLimit, &t.TPMLimit, &t.ConcurrencyLimit, &t.CreatedAt, &t.DisabledAt); err != nil {
		return nil, err
	}
	t.QuotaCredits, t.UsedCredits, t.ReservedCredits = derefString2(quota), derefString2(used), derefString2(res)
	return &t, nil
}

// numeric 列以二进制协议读到 *string;非空保证下用指针承接。
func derefString2(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *PostgresStore) CreateTeam(ctx context.Context, input CreateTeamInput) (*TeamRecord, error) {
	rows, err := s.pool.Query(ctx, `
		INSERT INTO teams (name, quota_credits, unlimited)
		VALUES (trim($1), $2::numeric, $3)
		RETURNING `+teamCols, input.Name, input.QuotaCredits, input.Unlimited)
	if err != nil {
		if uniqueViolation(err) {
			return nil, fmt.Errorf("%w: %s", ErrTeamNameTaken, strings.TrimSpace(input.Name))
		}
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			if uniqueViolation(err) {
				return nil, fmt.Errorf("%w: %s", ErrTeamNameTaken, strings.TrimSpace(input.Name))
			}
			return nil, err
		}
		return nil, nil
	}
	return scanTeamTeamRow(rows)
}

func (s *PostgresStore) GetTeam(ctx context.Context, id string) (*TeamRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+teamCols+` FROM teams WHERE id = $1::uuid LIMIT 1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanTeamTeamRow(rows)
}

func (s *PostgresStore) ListTeams(ctx context.Context) ([]TeamWithUsage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.id::text, t.name, t.quota_credits::text, t.used_credits::text, t.reserved_credits::text, t.unlimited,
			t.rpm_limit, t.tpm_limit, t.concurrency_limit, t.created_at, t.disabled_at,
			(SELECT count(*)::int FROM team_members m WHERE m.team_id = t.id) AS member_count
		FROM teams t
		ORDER BY t.created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TeamWithUsage{}
	for rows.Next() {
		var (
			team        TeamRecord
			quota       *string
			used        *string
			reserved    *string
			memberCount int64
		)
		if err := rows.Scan(&team.ID, &team.Name, &quota, &used, &reserved, &team.Unlimited,
			&team.RPMLimit, &team.TPMLimit, &team.ConcurrencyLimit, &team.CreatedAt, &team.DisabledAt, &memberCount); err != nil {
			return nil, err
		}
		team.QuotaCredits, team.UsedCredits, team.ReservedCredits = derefString2(quota), derefString2(used), derefString2(reserved)
		out = append(out, TeamWithUsage{Team: team, MemberCount: memberCount})
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateTeam(ctx context.Context, id string, patch UpdateTeamPatch) (*TeamRecord, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE teams SET
			name = COALESCE($2, name),
			quota_credits = COALESCE($3::numeric, quota_credits),
			unlimited = COALESCE($4::boolean, unlimited),
			rpm_limit = COALESCE($7::integer, rpm_limit),
			tpm_limit = COALESCE($8::integer, tpm_limit),
			concurrency_limit = COALESCE($9::integer, concurrency_limit),
			disabled_at = CASE WHEN $5::boolean THEN $6::timestamptz ELSE disabled_at END
		WHERE id = $1::uuid
		RETURNING `+teamCols,
		id, patch.Name, patch.QuotaCredits, patch.Unlimited, patch.HasDisabled, nullableTime(patch.DisabledAt),
		patch.RPMLimit, patch.TPMLimit, patch.ConcurrencyLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanTeamTeamRow(rows)
}

func (s *PostgresStore) ListMembers(ctx context.Context, teamID string) ([]TeamMemberView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.role, m.created_at,
			u.id::text, u.email, u.password_hash, u.display_name, u.role, u.disabled_at, u.created_at
		FROM team_members m
		JOIN users u ON u.id = m.user_id
		WHERE m.team_id = $1::uuid
		ORDER BY m.created_at ASC`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TeamMemberView{}
	for rows.Next() {
		var (
			v        TeamMemberView
			memRole  string
			userRole string
		)
		if err := rows.Scan(&memRole, &v.CreatedAt, &v.User.ID, &v.User.Email, &v.User.PasswordHash, &v.User.DisplayName, &userRole, &v.User.DisabledAt, &v.User.CreatedAt); err != nil {
			return nil, err
		}
		v.User.Role = UserRole(userRole)
		v.Role = TeamMemberRole(memRole)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *PostgresStore) AddMember(ctx context.Context, teamID, userID string, role TeamMemberRole) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO team_members (team_id, user_id, role)
		VALUES ($1::uuid, $2::uuid, $3)
		ON CONFLICT (team_id, user_id) DO UPDATE SET role = EXCLUDED.role`,
		teamID, userID, string(role))
	return err
}

func (s *PostgresStore) RemoveMember(ctx context.Context, teamID, userID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM team_members WHERE team_id = $1::uuid AND user_id = $2::uuid`, teamID, userID)
	return tag.RowsAffected() > 0, err
}

func (s *PostgresStore) ListTeamsForUser(ctx context.Context, userID string) ([]TeamMembership, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.role AS member_role,
			t.id::text, t.name, t.quota_credits::text, t.used_credits::text, t.reserved_credits::text, t.unlimited,
			t.rpm_limit, t.tpm_limit, t.concurrency_limit, t.created_at, t.disabled_at
		FROM team_members m
		JOIN teams t ON t.id = m.team_id
		WHERE m.user_id = $1::uuid
		ORDER BY m.created_at ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TeamMembership{}
	for rows.Next() {
		var (
			role     string
			member   TeamRecord
			quota    *string
			used     *string
			reserved *string
		)
		if err := rows.Scan(&role, &member.ID, &member.Name, &quota, &used, &reserved, &member.Unlimited,
			&member.RPMLimit, &member.TPMLimit, &member.ConcurrencyLimit, &member.CreatedAt, &member.DisabledAt); err != nil {
			return nil, err
		}
		member.QuotaCredits, member.UsedCredits, member.ReservedCredits = derefString2(quota), derefString2(used), derefString2(reserved)
		out = append(out, TeamMembership{Team: member, Role: TeamMemberRole(role)})
	}
	return out, rows.Err()
}

func nullableRole(r *UserRole) any {
	if r == nil {
		return nil
	}
	return string(*r)
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}
