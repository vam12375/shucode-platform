package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const paymentOrderCols = `id::text,out_trade_no,provider,configuration_id::text,target_type,target_id::text,team_id::text,user_id::text,app_id,seller_id,amount_fen,credit_micro,subject,status,COALESCE(trade_no,''),notify_payload,test_payment,purchase_kind,package_snapshot,created_at,expires_at,paid_at,closed_at`

func scanPaymentOrder(row pgx.Row) (*PaymentOrderRecord, error) {
	var record PaymentOrderRecord
	var snapshot []byte
	err := row.Scan(&record.ID, &record.OutTradeNo, &record.Provider, &record.ConfigurationID, &record.TargetType, &record.TargetID, &record.TeamID, &record.UserID, &record.AppID, &record.SellerID, &record.AmountFen, &record.CreditMicro, &record.Subject, &record.Status, &record.TradeNo, &record.NotifyPayload, &record.TestPayment, &record.PurchaseKind, &snapshot, &record.CreatedAt, &record.ExpiresAt, &record.PaidAt, &record.ClosedAt)
	if queryErrNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(snapshot) > 0 && string(snapshot) != "null" {
		if err := json.Unmarshal(snapshot, &record.PackageSnapshot); err != nil {
			return nil, err
		}
	}
	return &record, nil
}

func paymentWriteError(err error) error {
	if uniqueViolation(err) {
		return ErrPaymentConflict
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23503" || pgErr.Code == "23514") {
		return ErrInvalidInput
	}
	return err
}

func (s *PostgresStore) CreatePaymentOrder(ctx context.Context, record PaymentOrderRecord) (*PaymentOrderRecord, error) {
	record, err := normalizePaymentOrder(record)
	if err != nil {
		return nil, err
	}
	var validTarget bool
	if err := s.pool.QueryRow(ctx, `SELECT CASE WHEN $1='personal' THEN EXISTS(SELECT 1 FROM personal_accounts WHERE user_id=$2::uuid AND team_id=$3::uuid) ELSE NOT EXISTS(SELECT 1 FROM personal_accounts WHERE team_id=$3::uuid) END`, record.TargetType, record.TargetID, record.TeamID).Scan(&validTarget); err != nil {
		return nil, err
	}
	if !validTarget {
		return nil, ErrPaymentConflict
	}
	var snapshot []byte
	if record.PackageSnapshot != nil {
		snapshot, _ = json.Marshal(record.PackageSnapshot)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO payment_orders(id,out_trade_no,provider,configuration_id,target_type,target_id,team_id,user_id,app_id,seller_id,amount_fen,credit_micro,subject,status,test_payment,purchase_kind,package_snapshot,created_at,expires_at) VALUES($1::uuid,$2,$3,$4::uuid,$5,$6::uuid,$7::uuid,$8::uuid,$9,$10,$11,$12,$13,'pending',$14,$15,$16,$17,$18) ON CONFLICT(out_trade_no) DO NOTHING`, record.ID, record.OutTradeNo, record.Provider, record.ConfigurationID, record.TargetType, record.TargetID, record.TeamID, record.UserID, record.AppID, record.SellerID, record.AmountFen, record.CreditMicro, record.Subject, record.TestPayment, record.PurchaseKind, nullableBytes(snapshot), record.CreatedAt, record.ExpiresAt)
	if err != nil {
		return nil, paymentWriteError(err)
	}
	saved, err := s.GetPaymentOrder(ctx, record.OutTradeNo)
	if err != nil {
		return nil, err
	}
	if saved == nil || !samePaymentOrder(*saved, record) {
		return nil, ErrPaymentConflict
	}
	return saved, nil
}

func (s *PostgresStore) GetPaymentOrder(ctx context.Context, outTradeNo string) (*PaymentOrderRecord, error) {
	if !paymentReferencePattern.MatchString(outTradeNo) {
		return nil, ErrInvalidInput
	}
	return scanPaymentOrder(s.pool.QueryRow(ctx, `SELECT `+paymentOrderCols+` FROM payment_orders WHERE out_trade_no=$1`, outTradeNo))
}

func (s *PostgresStore) ListPaymentOrders(ctx context.Context, filter PaymentOrderFilter) ([]PaymentOrderRecord, error) {
	filter, err := normalizePaymentFilter(filter)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+paymentOrderCols+` FROM payment_orders WHERE ($1='' OR team_id=NULLIF($1,'')::uuid) AND ($2='' OR user_id=NULLIF($2,'')::uuid) AND ($3='' OR status=$3) AND ($4::timestamptz IS NULL OR (created_at,id)<($4,NULLIF($5,'')::uuid)) AND ($6='' OR target_type=$6) AND ($7='' OR target_id=NULLIF($7,'')::uuid) ORDER BY created_at DESC,id DESC LIMIT $8`, filter.TeamID, filter.UserID, filter.Status, filter.BeforeCreatedAt, filter.BeforeID, filter.TargetType, filter.TargetID, filter.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PaymentOrderRecord{}
	for rows.Next() {
		record, err := scanPaymentOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *record)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ClaimPaymentSuccess(ctx context.Context, input PaymentSuccessInput) (*PaymentOrderRecord, bool, error) {
	if err := validatePaymentSuccess(input); err != nil {
		return nil, false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	record, err := scanPaymentOrder(tx.QueryRow(ctx, `SELECT `+paymentOrderCols+` FROM payment_orders WHERE out_trade_no=$1 FOR UPDATE`, input.OutTradeNo))
	if err != nil {
		return nil, false, err
	}
	if record == nil {
		return nil, false, ErrPaymentNotFound
	}
	if err := matchPaymentSuccess(*record, input); err != nil {
		return nil, false, err
	}
	if record.Status == PaymentPaid {
		return record, true, nil
	}
	var oldQuota string
	var unlimited bool
	err = tx.QueryRow(ctx, `SELECT quota_credits::text,unlimited FROM teams WHERE id=$1::uuid FOR UPDATE`, record.TeamID).Scan(&oldQuota, &unlimited)
	if queryErrNoRows(err) {
		return nil, false, ErrInvalidInput
	}
	if err != nil {
		return nil, false, err
	}
	audit := paymentAudit(*record, input)
	patch, err := prepareTeamCreditIncrease(oldQuota, UpdateTeamPatch{QuotaIncreaseMicro: &record.CreditMicro}, &audit)
	if err != nil {
		return nil, false, err
	}
	// 支付事实、余额与两条审计账本同事务提交，避免通知重放或崩溃后重复入账。
	if _, err = tx.Exec(ctx, `UPDATE teams SET quota_credits=$2::numeric WHERE id=$1::uuid`, record.TeamID, *patch.QuotaCredits); err != nil {
		return nil, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO team_quota_adjustments(team_id,actor_user_id,action,old_quota_credits,new_quota_credits,old_unlimited,new_unlimited,created_at) VALUES($1::uuid,$2::uuid,'alipay_recharge',$3::numeric,$4::numeric,$5,$5,$6)`, record.TeamID, record.UserID, oldQuota, *patch.QuotaCredits, unlimited, input.PaidAt.UTC()); err != nil {
		return nil, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO admin_audit_logs(event_id,actor_user_id,action,target_type,target_id,detail,team_id,created_at) VALUES($1,$2::uuid,$3,$4,$5,$6,$7::uuid,$8)`, audit.EventID, audit.ActorUserID, audit.Action, audit.TargetType, audit.TargetID, audit.Detail, audit.TeamID, input.PaidAt.UTC()); err != nil {
		return nil, false, paymentWriteError(err)
	}
	saved, err := scanPaymentOrder(tx.QueryRow(ctx, `UPDATE payment_orders SET status='paid',trade_no=$2,notify_payload=$3,paid_at=$4,closed_at=NULL WHERE out_trade_no=$1 RETURNING `+paymentOrderCols, record.OutTradeNo, input.TradeNo, nullableBytes(input.NotifyPayload), input.PaidAt.UTC()))
	if err != nil {
		return nil, false, paymentWriteError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, false, paymentWriteError(err)
	}
	return saved, false, nil
}

func (s *PostgresStore) ClosePaymentOrder(ctx context.Context, outTradeNo string, at time.Time) (*PaymentOrderRecord, error) {
	if !paymentReferencePattern.MatchString(outTradeNo) || at.IsZero() {
		return nil, ErrInvalidInput
	}
	_, err := s.pool.Exec(ctx, `UPDATE payment_orders SET status='closed',closed_at=$2 WHERE out_trade_no=$1 AND status='pending'`, outTradeNo, at.UTC())
	if err != nil {
		return nil, err
	}
	saved, err := s.GetPaymentOrder(ctx, outTradeNo)
	if err != nil {
		return nil, err
	}
	if saved == nil {
		return nil, ErrPaymentNotFound
	}
	return saved, nil
}
