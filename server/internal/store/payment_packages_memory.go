package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const paymentPackageCatalogKey = "payments:packages:current"

func paymentPackageVersionKey(revision string) string { return "payments:packages:version:" + revision }

func (s *memoryStore) paymentPackageCatalogLocked() (*PaymentPackageCatalog, error) {
	var current struct {
		Revision string `json:"revision"`
	}
	raw := s.config[paymentPackageCatalogKey]
	if len(raw) == 0 {
		catalog := initialPaymentPackageCatalog()
		version, _ := json.Marshal(catalog)
		pointer, _ := json.Marshal(map[string]string{"revision": catalog.Revision})
		s.config[paymentPackageVersionKey(catalog.Revision)] = version
		s.config[paymentPackageCatalogKey] = pointer
		return clonePaymentPackageCatalog(&catalog), nil
	}
	if err := json.Unmarshal(raw, &current); err != nil || !canonicalUUID(current.Revision) {
		return nil, ErrInvalidInput
	}
	version := s.config[paymentPackageVersionKey(current.Revision)]
	catalog, err := decodePaymentPackageCatalog(version)
	if err != nil || catalog == nil || catalog.Revision != current.Revision {
		return nil, ErrInvalidInput
	}
	return catalog, nil
}

func (s *memoryStore) GetPaymentPackageCatalog(_ context.Context) (*PaymentPackageCatalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paymentPackageCatalogLocked()
}

func (s *memoryStore) SavePaymentPackageCatalog(_ context.Context, expectedRevision string, catalog PaymentPackageCatalog) (*PaymentPackageCatalog, error) {
	if err := ValidatePaymentPackageCatalog(catalog); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.paymentPackageCatalogLocked()
	if err != nil {
		return nil, err
	}
	if current.Revision != expectedRevision {
		return nil, ErrPaymentPackageCatalogConflict
	}
	if err := validatePackageCatalogUpdate(*current, catalog); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	catalog.Revision = uuid.NewString()
	catalog.PreviousRevision = current.Revision
	catalog.PublishedAt = &now
	version, _ := json.Marshal(catalog)
	pointer, _ := json.Marshal(map[string]string{"revision": catalog.Revision})
	s.config[paymentPackageVersionKey(catalog.Revision)] = version
	s.config[paymentPackageCatalogKey] = pointer
	return clonePaymentPackageCatalog(&catalog), nil
}

func (s *memoryStore) CreatePlanPaymentOrder(_ context.Context, record PaymentOrderRecord, catalogRevision, sku string) (*PaymentOrderRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.paymentOrders[record.OutTradeNo]; ok {
		if existing.PackageSnapshot == nil || existing.PackageSnapshot.SKU != sku || existing.PackageSnapshot.CatalogRevision != catalogRevision || existing.UserID != record.UserID || existing.TargetType != record.TargetType || existing.TargetID != record.TargetID || existing.TeamID != record.TeamID {
			return nil, ErrPaymentConflict
		}
		return clonePaymentOrder(existing), nil
	}
	current, err := s.paymentPackageCatalogLocked()
	if err != nil {
		return nil, err
	}
	catalog := *current
	if catalog.Revision != catalogRevision {
		return nil, ErrPaymentPackageCatalogConflict
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
	record.PackageSnapshot = packageSnapshot(catalog, *plan)
	record.AmountFen, record.CreditMicro = plan.AmountFen, plan.AmountFen*MicroCreditsPerFen
	normalized, err := normalizePaymentOrder(record)
	if err != nil {
		return nil, err
	}
	if _, ok := s.teams[normalized.TeamID]; !ok {
		return nil, ErrInvalidInput
	}
	if _, ok := s.users[normalized.UserID]; !ok {
		return nil, ErrInvalidInput
	}
	if normalized.TargetType == PaymentTargetPersonal && s.personalAccounts[normalized.TargetID] != normalized.TeamID || normalized.TargetType == PaymentTargetTeam && s.personalOwnerLocked(normalized.TeamID) != "" {
		return nil, ErrPaymentConflict
	}
	for _, saved := range s.paymentOrders {
		if saved.ID == normalized.ID {
			return nil, ErrPaymentConflict
		}
	}
	s.paymentOrders[normalized.OutTradeNo] = *clonePaymentOrder(normalized)
	return clonePaymentOrder(normalized), nil
}
