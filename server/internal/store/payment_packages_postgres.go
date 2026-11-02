package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

func readPaymentPackageCatalog(ctx context.Context, q tenantRowQuerier) (*PaymentPackageCatalog, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT value FROM platform_config WHERE key=$1`, paymentPackageCatalogKey).Scan(&raw)
	if queryErrNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pointer struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal(raw, &pointer); err != nil || !canonicalUUID(pointer.Revision) {
		return nil, ErrInvalidInput
	}
	err = q.QueryRow(ctx, `SELECT value FROM platform_config WHERE key=$1`, paymentPackageVersionKey(pointer.Revision)).Scan(&raw)
	if err != nil {
		return nil, err
	}
	catalog, err := decodePaymentPackageCatalog(raw)
	if err != nil || catalog == nil || catalog.Revision != pointer.Revision {
		return nil, ErrInvalidInput
	}
	return catalog, nil
}

func (s *PostgresStore) GetPaymentPackageCatalog(ctx context.Context) (*PaymentPackageCatalog, error) {
	catalog, err := readPaymentPackageCatalog(ctx, s.pool)
	if err == nil && catalog == nil {
		return nil, ErrPaymentPackageUnavailable
	}
	return catalog, err
}

func (s *PostgresStore) SavePaymentPackageCatalog(ctx context.Context, expectedRevision string, catalog PaymentPackageCatalog) (*PaymentPackageCatalog, error) {
	if err := ValidatePaymentPackageCatalog(catalog); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var currentRaw []byte
	err = tx.QueryRow(ctx, `SELECT value FROM platform_config WHERE key=$1 FOR UPDATE`, paymentPackageCatalogKey).Scan(&currentRaw)
	if queryErrNoRows(err) {
		return nil, ErrPaymentPackageUnavailable
	}
	if err != nil {
		return nil, err
	}
	var pointer struct {
		Revision string `json:"revision"`
	}
	if err = json.Unmarshal(currentRaw, &pointer); err != nil || !canonicalUUID(pointer.Revision) {
		return nil, ErrInvalidInput
	}
	if pointer.Revision != expectedRevision {
		return nil, ErrPaymentPackageCatalogConflict
	}
	current, err := readPaymentPackageCatalog(ctx, tx)
	if err != nil || current == nil {
		if err == nil {
			err = ErrInvalidInput
		}
		return nil, err
	}
	if err := validatePackageCatalogUpdate(*current, catalog); err != nil {
		return nil, err
	}
	catalog.Revision = uuid.NewString()
	catalog.PreviousRevision = pointer.Revision
	now := time.Now().UTC()
	catalog.PublishedAt = &now
	versionRaw, err := json.Marshal(catalog)
	if err != nil {
		return nil, err
	}
	newPointer, _ := json.Marshal(map[string]string{"revision": catalog.Revision})
	if _, err = tx.Exec(ctx, `INSERT INTO platform_config(key,value,updated_at) VALUES($1,$2::jsonb,$3)`, paymentPackageVersionKey(catalog.Revision), versionRaw, now); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE platform_config SET value=$2::jsonb,updated_at=$3 WHERE key=$1`, paymentPackageCatalogKey, newPointer, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return clonePaymentPackageCatalog(&catalog), nil
}

func (s *PostgresStore) CreatePlanPaymentOrder(ctx context.Context, record PaymentOrderRecord, catalogRevision, sku string) (*PaymentOrderRecord, error) {
	if !packageSKURegex.MatchString(sku) || !canonicalUUID(catalogRevision) {
		return nil, ErrInvalidInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// 幂等重试先匹配已有订单快照；目录已改价或下架不应阻止原订单恢复。
	existing, err := scanPaymentOrder(tx.QueryRow(ctx, `SELECT `+paymentOrderCols+` FROM payment_orders WHERE out_trade_no=$1 FOR UPDATE`, record.OutTradeNo))
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.PackageSnapshot == nil || existing.PackageSnapshot.SKU != sku || existing.PackageSnapshot.CatalogRevision != catalogRevision || existing.UserID != record.UserID || existing.TargetType != record.TargetType || existing.TargetID != record.TargetID || existing.TeamID != record.TeamID {
			return nil, ErrPaymentConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return existing, nil
	}
	var currentRaw []byte
	if err = tx.QueryRow(ctx, `SELECT value FROM platform_config WHERE key=$1 FOR SHARE`, paymentPackageCatalogKey).Scan(&currentRaw); err != nil {
		return nil, err
	}
	var pointer struct {
		Revision string `json:"revision"`
	}
	if err = json.Unmarshal(currentRaw, &pointer); err != nil || !canonicalUUID(pointer.Revision) {
		return nil, ErrInvalidInput
	}
	if pointer.Revision != catalogRevision {
		return nil, ErrPaymentPackageCatalogConflict
	}
	catalog, err := readPaymentPackageCatalog(ctx, tx)
	if err != nil || catalog == nil {
		if err == nil {
			err = ErrPaymentPackageUnavailable
		}
		return nil, err
	}
	var plan *PaymentPackage
	for i := range catalog.Packages {
		if catalog.Packages[i].SKU == sku {
			plan = &catalog.Packages[i]
			break
		}
	}
	if plan == nil || plan.Status != PaymentPackagePublished || plan.TargetType != record.TargetType {
		return nil, ErrPaymentPackageUnavailable
	}
	record.PurchaseKind = PaymentPurchasePlan
	record.PackageSnapshot = packageSnapshot(*catalog, *plan)
	record.AmountFen = plan.AmountFen
	record.CreditMicro = plan.AmountFen * MicroCreditsPerFen
	if record.Subject == "" {
		record.Subject = plan.Name
	}
	record, err = normalizePaymentOrder(record)
	if err != nil {
		return nil, err
	}
	var validTarget bool
	if err = tx.QueryRow(ctx, `SELECT CASE WHEN $1='personal' THEN EXISTS(SELECT 1 FROM personal_accounts WHERE user_id=$2::uuid AND team_id=$3::uuid) ELSE NOT EXISTS(SELECT 1 FROM personal_accounts WHERE team_id=$3::uuid) END`, record.TargetType, record.TargetID, record.TeamID).Scan(&validTarget); err != nil {
		return nil, err
	}
	if !validTarget {
		return nil, ErrPaymentConflict
	}
	var snapshot []byte
	snapshot, _ = json.Marshal(record.PackageSnapshot)
	_, err = tx.Exec(ctx, `INSERT INTO payment_orders(id,out_trade_no,provider,configuration_id,target_type,target_id,team_id,user_id,app_id,seller_id,amount_fen,credit_micro,subject,status,test_payment,purchase_kind,package_snapshot,created_at,expires_at) VALUES($1::uuid,$2,$3,$4::uuid,$5,$6::uuid,$7::uuid,$8::uuid,$9,$10,$11,$12,$13,'pending',false,'plan',$14::jsonb,$15,$16) ON CONFLICT(out_trade_no) DO NOTHING`, record.ID, record.OutTradeNo, record.Provider, record.ConfigurationID, record.TargetType, record.TargetID, record.TeamID, record.UserID, record.AppID, record.SellerID, record.AmountFen, record.CreditMicro, record.Subject, snapshot, record.CreatedAt, record.ExpiresAt)
	if err != nil {
		return nil, paymentWriteError(err)
	}
	saved, err := scanPaymentOrder(tx.QueryRow(ctx, `SELECT `+paymentOrderCols+` FROM payment_orders WHERE out_trade_no=$1 FOR UPDATE`, record.OutTradeNo))
	if err != nil || saved == nil || !samePaymentOrder(*saved, record) {
		if err != nil {
			return nil, paymentWriteError(err)
		}
		return nil, ErrPaymentConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, paymentWriteError(err)
	}
	return saved, nil
}
