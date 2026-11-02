package store

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) EnsurePersonalAccount(ctx context.Context, userID string) (*TeamRecord, error) {
	if !canonicalUUID(userID) {
		return nil, ErrInvalidInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockMemberUser(ctx, tx, userID); queryErrNoRows(err) {
		return nil, ErrInvalidInput
	} else if err != nil {
		return nil, err
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT team_id::text FROM personal_accounts WHERE user_id=$1::uuid`, userID).Scan(&id)
	if queryErrNoRows(err) {
		// 普通团队可以使用任意名称，个人开户不能依赖可预测的用户名称仍然空闲。
		id = uuid.NewString()
		if _, err = tx.Exec(ctx, `INSERT INTO teams(id,name,quota_credits,unlimited) VALUES($1::uuid,$2,0,false)`, id, personalTeamName(id)); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO personal_accounts(user_id,team_id) VALUES($1::uuid,$2::uuid)`, userID, id); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetTeam(ctx, id)
}

func (s *PostgresStore) GetPersonalAccount(ctx context.Context, userID string) (*TeamRecord, error) {
	if !canonicalUUID(userID) {
		return nil, ErrInvalidInput
	}
	var id string
	err := s.pool.QueryRow(ctx, `SELECT team_id::text FROM personal_accounts WHERE user_id=$1::uuid`, userID).Scan(&id)
	if queryErrNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.GetTeam(ctx, id)
}

func (s *PostgresStore) GetBillingMode(ctx context.Context, userID string) (string, error) {
	if !canonicalUUID(userID) {
		return "", ErrInvalidInput
	}
	var mode string
	err := s.pool.QueryRow(ctx, `SELECT mode FROM user_billing_modes WHERE user_id=$1::uuid`, userID).Scan(&mode)
	if queryErrNoRows(err) {
		return "team", nil
	}
	return mode, err
}

func (s *PostgresStore) SetBillingMode(ctx context.Context, userID, mode string) error {
	if mode != "personal" && mode != "team" {
		return ErrBillingModeInvalid
	}
	if !canonicalUUID(userID) {
		return ErrInvalidInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockMemberUser(ctx, tx, userID); err != nil {
		if queryErrNoRows(err) {
			return ErrInvalidInput
		}
		return err
	}
	if mode == "personal" {
		var id string
		err = tx.QueryRow(ctx, `SELECT team_id::text FROM personal_accounts WHERE user_id=$1::uuid`, userID).Scan(&id)
		if queryErrNoRows(err) {
			id = uuid.NewString()
			if _, err = tx.Exec(ctx, `INSERT INTO teams(id,name,quota_credits,unlimited) VALUES($1::uuid,$2,0,false)`, id, personalTeamName(id)); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO personal_accounts(user_id,team_id) VALUES($1::uuid,$2::uuid)`, userID, id); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO user_billing_modes(user_id,mode) VALUES($1::uuid,$2) ON CONFLICT(user_id) DO UPDATE SET mode=EXCLUDED.mode`, userID, mode); err != nil {
		return paymentWriteError(err)
	}
	return tx.Commit(ctx)
}

func privateAccountOwner(ctx context.Context, q tenantRowQuerier, teamID string) (string, error) {
	var owner string
	err := q.QueryRow(ctx, `SELECT user_id::text FROM personal_accounts WHERE team_id=$1::uuid`, teamID).Scan(&owner)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return owner, err
}

func (s *PostgresStore) IsPersonalAccountTeam(ctx context.Context, teamID string) (bool, error) {
	if !canonicalUUID(teamID) {
		return false, ErrInvalidInput
	}
	owner, err := privateAccountOwner(ctx, s.pool, teamID)
	return owner != "", err
}
