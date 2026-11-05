package store

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	// 与消费模式更新共用 actor 行锁，首次创建及设备授权只有一个事务顺序，避免并发重登录丢授权。
	if err = tx.QueryRow(ctx, `SELECT disabled_at FROM users WHERE id=$1::uuid FOR UPDATE`, input.ActorUserID).Scan(&disabled); queryErrNoRows(err) {
		return nil, ErrBillingBindingForbidden
	} else if err != nil {
		return nil, err
	}
	if disabled != nil {
		return nil, ErrBillingBindingForbidden
	}
	var tokenUser string
	var revoked *time.Time
	if err = tx.QueryRow(ctx, `SELECT user_id::text,revoked_at FROM device_tokens WHERE id=$1::uuid FOR SHARE`, input.DeviceTokenID).Scan(&tokenUser, &revoked); queryErrNoRows(err) {
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
		if err := validateBillingBindingReplay(&b, input); err != nil {
			return nil, err
		}
		if err := authorizeBillingBindingDevice(ctx, tx, b.ID, input.DeviceTokenID); err != nil {
			return nil, err
		}
		return &b, tx.Commit(ctx)
	}
	if !queryErrNoRows(err) {
		return nil, err
	}
	var accountID, accountType, accountName string
	if input.ParentSessionID != "" {
		var parentActor string
		err = tx.QueryRow(ctx, `SELECT billing_account_id::text,billing_account_type,billing_account_name,actor_user_id::text FROM billing_session_bindings WHERE session_id=$1 AND workspace_identity=$2 AND remote_session_id=$3 FOR SHARE`, input.ParentSessionID, input.WorkspaceIdentity, input.RemoteSessionID).Scan(&accountID, &accountType, &accountName, &parentActor)
		if queryErrNoRows(err) {
			return nil, ErrBillingBindingNotFound
		}
		if err != nil {
			return nil, err
		}
		if parentActor != input.ActorUserID {
			return nil, ErrBillingBindingActorMismatch
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
				return nil, ErrBillingBindingAccountUnavailable
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
				return nil, ErrBillingBindingNoActiveTeam
			}
			accountType = "team"
		}
	}
	// PostgreSQL 时间戳精度为微秒，首次响应与恢复响应必须返回同一创建时间。
	b = BillingSessionBinding{ID: uuid.NewString(), SessionID: input.SessionID, WorkspaceIdentity: input.WorkspaceIdentity, RemoteSessionID: input.RemoteSessionID, ParentSessionID: input.ParentSessionID, BillingAccountID: accountID, BillingAccountType: accountType, BillingAccountName: accountName, ActorUserID: input.ActorUserID, DeviceTokenID: input.DeviceTokenID, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	result, err := tx.Exec(ctx, `INSERT INTO billing_session_bindings (id,session_id,workspace_identity,remote_session_id,parent_session_id,billing_account_id,billing_account_type,billing_account_name,actor_user_id,device_token_id,created_at) VALUES ($1::uuid,$2,$3,$4,$5,$6::uuid,$7,$8,$9::uuid,$10::uuid,$11) ON CONFLICT (session_id,workspace_identity,remote_session_id) DO NOTHING`, b.ID, b.SessionID, b.WorkspaceIdentity, b.RemoteSessionID, b.ParentSessionID, b.BillingAccountID, b.BillingAccountType, b.BillingAccountName, b.ActorUserID, b.DeviceTokenID, b.CreatedAt)
	if err != nil {
		return nil, err
	}
	// 不同 actor 仍可能争用同一会话 identity；读取赢家并拒绝跨账号接管。
	if result.RowsAffected() == 0 {
		existing, err := scanBillingBinding(tx.QueryRow(ctx, `SELECT `+billingBindingCols+` FROM billing_session_bindings WHERE session_id=$1 AND workspace_identity=$2 AND remote_session_id=$3`, input.SessionID, input.WorkspaceIdentity, input.RemoteSessionID))
		if err != nil {
			return nil, err
		}
		if err := validateBillingBindingReplay(existing, input); err != nil {
			return nil, err
		}
		b = *existing
	}
	if err := authorizeBillingBindingDevice(ctx, tx, b.ID, input.DeviceTokenID); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &b, nil
}
func (s *PostgresStore) ResolveBillingSessionBinding(ctx context.Context, id, userID, deviceID string) (*BillingSessionBinding, error) {
	return resolveBillingSessionBindingQuery(ctx, s.pool, id, userID, deviceID)
}

func authorizeBillingBindingDevice(ctx context.Context, tx pgx.Tx, bindingID, deviceID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO billing_session_binding_devices (binding_id,device_token_id,created_at) VALUES ($1::uuid,$2::uuid,$3) ON CONFLICT DO NOTHING`, bindingID, deviceID, time.Now().UTC())
	return err
}

type billingBindingQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func resolveBillingSessionBindingQuery(ctx context.Context, query billingBindingQuery, id, userID, deviceID string) (*BillingSessionBinding, error) {
	if !canonicalUUID(id) || !canonicalUUID(userID) || !canonicalUUID(deviceID) {
		return nil, ErrBillingBindingForbidden
	}
	b, err := scanBillingBinding(query.QueryRow(ctx, `SELECT `+billingBindingCols+` FROM billing_session_bindings WHERE id=$1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBillingBindingNotFound
	}
	if err != nil {
		return nil, err
	}
	if b.ActorUserID != userID {
		return nil, ErrBillingBindingActorMismatch
	}
	var authorized bool
	if err := query.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_session_binding_devices WHERE binding_id=$1::uuid AND device_token_id=$2::uuid)`, id, deviceID).Scan(&authorized); err != nil {
		return nil, err
	}
	if !authorized {
		return nil, ErrBillingBindingDeviceMismatch
	}
	var revoked *time.Time
	if err := query.QueryRow(ctx, `SELECT revoked_at FROM device_tokens WHERE id=$1::uuid AND user_id=$2::uuid`, deviceID, userID).Scan(&revoked); queryErrNoRows(err) {
		return nil, ErrBillingBindingForbidden
	} else if err != nil {
		return nil, err
	}
	if revoked != nil {
		return nil, ErrBillingBindingForbidden
	}
	return b, nil
}
