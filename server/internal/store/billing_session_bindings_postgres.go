package store

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"time"
)

const billingBindingCols = `id::text,session_id,workspace_identity,remote_session_id,parent_session_id,billing_account_id::text,billing_account_type,billing_account_name,actor_user_id::text,device_token_id::text,created_at`

func scanBillingBinding(row pgx.Row) (*BillingSessionBinding, error) {
	var b BillingSessionBinding
	err := row.Scan(&b.ID, &b.SessionID, &b.WorkspaceIdentity, &b.RemoteSessionID, &b.ParentSessionID, &b.BillingAccountID, &b.BillingAccountType, &b.BillingAccountName, &b.ActorUserID, &b.DeviceTokenID, &b.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &b, nil
}
func (s *PostgresStore) CreateBillingSessionBinding(ctx context.Context, raw BillingSessionBindingInput) (*BillingSessionBinding, error) {
	input, err := normalizeBillingBindingInput(raw)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var disabled *time.Time
	if err = tx.QueryRow(ctx, `SELECT disabled_at FROM users WHERE id=$1::uuid FOR SHARE`, input.ActorUserID).Scan(&disabled); queryErrNoRows(err) {
		return nil, ErrBillingBindingForbidden
	} else if err != nil {
		return nil, err
	}
	if disabled != nil {
		return nil, ErrBillingBindingForbidden
	}
	var tokenUser string
	var revoked *time.Time
	if err = tx.QueryRow(ctx, `SELECT user_id::text,revoked_at FROM device_tokens WHERE id=$1::uuid`, input.DeviceTokenID).Scan(&tokenUser, &revoked); queryErrNoRows(err) {
		return nil, ErrBillingBindingForbidden
	} else if err != nil {
		return nil, err
	}
	if tokenUser != input.ActorUserID || revoked != nil {
		return nil, ErrBillingBindingForbidden
	}
	var b BillingSessionBinding
	err = tx.QueryRow(ctx, `SELECT `+billingBindingCols+` FROM billing_session_bindings WHERE session_id=$1 AND workspace_identity=$2 AND remote_session_id=$3 FOR UPDATE`, input.SessionID, input.WorkspaceIdentity, input.RemoteSessionID).Scan(&b.ID, &b.SessionID, &b.WorkspaceIdentity, &b.RemoteSessionID, &b.ParentSessionID, &b.BillingAccountID, &b.BillingAccountType, &b.BillingAccountName, &b.ActorUserID, &b.DeviceTokenID, &b.CreatedAt)
	if err == nil {
		if b.ActorUserID != input.ActorUserID || b.DeviceTokenID != input.DeviceTokenID {
			return nil, ErrBillingBindingForbidden
		}
		return &b, tx.Commit(ctx)
	}
	if !queryErrNoRows(err) {
		return nil, err
	}
	var accountID, accountType, accountName string
	if input.ParentSessionID != "" {
		var parentActor, parentDevice string
		err = tx.QueryRow(ctx, `SELECT billing_account_id::text,billing_account_type,billing_account_name,actor_user_id::text,device_token_id::text FROM billing_session_bindings WHERE session_id=$1 AND workspace_identity=$2 AND remote_session_id=$3 FOR SHARE`, input.ParentSessionID, input.WorkspaceIdentity, input.RemoteSessionID).Scan(&accountID, &accountType, &accountName, &parentActor, &parentDevice)
		if queryErrNoRows(err) {
			return nil, ErrBillingBindingNotFound
		}
		if err != nil {
			return nil, err
		}
		if parentActor != input.ActorUserID || parentDevice != input.DeviceTokenID {
			return nil, ErrBillingBindingForbidden
		}
	} else {
		var mode string
		err = tx.QueryRow(ctx, `SELECT mode FROM user_billing_modes WHERE user_id=$1::uuid`, input.ActorUserID).Scan(&mode)
		if queryErrNoRows(err) {
			mode = "team"
		} else if err != nil {
			return nil, err
		}
		if mode == "personal" {
			err = tx.QueryRow(ctx, `SELECT pa.team_id::text,t.name FROM personal_accounts pa JOIN teams t ON t.id=pa.team_id WHERE pa.user_id=$1::uuid AND t.disabled_at IS NULL`, input.ActorUserID).Scan(&accountID, &accountName)
			if queryErrNoRows(err) {
				return nil, ErrBillingBindingForbidden
			}
			if err != nil {
				return nil, err
			}
			accountType = "personal"
		} else {
			rows, err := tx.Query(ctx, `SELECT t.id::text,t.name FROM team_members m JOIN teams t ON t.id=m.team_id WHERE m.user_id=$1::uuid AND m.disabled_at IS NULL AND t.disabled_at IS NULL`, input.ActorUserID)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			count := 0
			for rows.Next() {
				count++
				if count == 1 {
					if err = rows.Scan(&accountID, &accountName); err != nil {
						return nil, err
					}
				}
			}
			if err = rows.Err(); err != nil {
				return nil, err
			}
			if count != 1 {
				if count > 1 {
					return nil, ErrAmbiguousTeam
				}
				return nil, ErrBillingBindingForbidden
			}
			accountType = "team"
		}
	}
	b = BillingSessionBinding{ID: uuid.NewString(), SessionID: input.SessionID, WorkspaceIdentity: input.WorkspaceIdentity, RemoteSessionID: input.RemoteSessionID, ParentSessionID: input.ParentSessionID, BillingAccountID: accountID, BillingAccountType: accountType, BillingAccountName: accountName, ActorUserID: input.ActorUserID, DeviceTokenID: input.DeviceTokenID, CreatedAt: time.Now().UTC()}
	_, err = tx.Exec(ctx, `INSERT INTO billing_session_bindings (id,session_id,workspace_identity,remote_session_id,parent_session_id,billing_account_id,billing_account_type,billing_account_name,actor_user_id,device_token_id,created_at) VALUES ($1::uuid,$2,$3,$4,$5,$6::uuid,$7,$8,$9::uuid,$10::uuid,$11)`, b.ID, b.SessionID, b.WorkspaceIdentity, b.RemoteSessionID, b.ParentSessionID, b.BillingAccountID, b.BillingAccountType, b.BillingAccountName, b.ActorUserID, b.DeviceTokenID, b.CreatedAt)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			// 并发请求可能在首次 SELECT 后赢得唯一键；读取已提交行实现真正幂等。
			_ = tx.Rollback(ctx)
			var existing BillingSessionBinding
			qerr := s.pool.QueryRow(ctx, `SELECT `+billingBindingCols+` FROM billing_session_bindings WHERE session_id=$1 AND workspace_identity=$2 AND remote_session_id=$3`, input.SessionID, input.WorkspaceIdentity, input.RemoteSessionID).Scan(&existing.ID, &existing.SessionID, &existing.WorkspaceIdentity, &existing.RemoteSessionID, &existing.ParentSessionID, &existing.BillingAccountID, &existing.BillingAccountType, &existing.BillingAccountName, &existing.ActorUserID, &existing.DeviceTokenID, &existing.CreatedAt)
			if queryErrNoRows(qerr) {
				return nil, ErrBillingBindingConflict
			}
			if qerr != nil {
				return nil, qerr
			}
			if existing.ActorUserID != input.ActorUserID || existing.DeviceTokenID != input.DeviceTokenID {
				return nil, ErrBillingBindingForbidden
			}
			return &existing, nil
		}
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &b, nil
}
func (s *PostgresStore) ResolveBillingSessionBinding(ctx context.Context, id, userID, deviceID string) (*BillingSessionBinding, error) {
	if !canonicalUUID(id) || !canonicalUUID(userID) || !canonicalUUID(deviceID) {
		return nil, ErrBillingBindingForbidden
	}
	b, err := scanBillingBinding(s.pool.QueryRow(ctx, `SELECT `+billingBindingCols+` FROM billing_session_bindings WHERE id=$1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBillingBindingNotFound
	}
	if err != nil {
		return nil, err
	}
	if b.ActorUserID != userID || b.DeviceTokenID != deviceID {
		return nil, ErrBillingBindingForbidden
	}
	var revoked *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT revoked_at FROM device_tokens WHERE id=$1::uuid AND user_id=$2::uuid`, deviceID, userID).Scan(&revoked); queryErrNoRows(err) {
		return nil, ErrBillingBindingForbidden
	} else if err != nil {
		return nil, err
	}
	if revoked != nil {
		return nil, ErrBillingBindingForbidden
	}
	return b, nil
}
