package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type PaymentPackageStatus string

const (
	PaymentPackageDraft     PaymentPackageStatus = "draft"
	PaymentPackagePublished PaymentPackageStatus = "published"
	PaymentPackageArchived  PaymentPackageStatus = "archived"
	PaymentPurchaseCustom                        = "custom"
	PaymentPurchasePlan                          = "plan"
	PaymentPurchaseRootTest                      = "root_test"
)

var (
	ErrPaymentPackageCatalogConflict = errors.New("payment package catalog revision conflict")
	ErrPaymentPackageUnavailable     = errors.New("payment package is not purchasable")
	packageSKURegex                  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

type PaymentPackage struct {
	SKU         string               `json:"sku"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	TargetType  PaymentTargetType    `json:"targetType"`
	AmountFen   int64                `json:"amountFen"`
	Status      PaymentPackageStatus `json:"status"`
	Recommended bool                 `json:"recommended"`
	SortOrder   int                  `json:"sortOrder"`
}

type PaymentPackageCatalog struct {
	Revision         string           `json:"revision"`
	PreviousRevision string           `json:"previousRevision"`
	PublishedAt      *time.Time       `json:"publishedAt"`
	PublishedBy      string           `json:"publishedBy"`
	Packages         []PaymentPackage `json:"packages"`
}

type PaymentPackageSnapshot struct {
	SKU             string            `json:"sku"`
	CatalogRevision string            `json:"catalogRevision"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	TargetType      PaymentTargetType `json:"targetType"`
	AmountFen       int64             `json:"amountFen"`
	CreditMicro     int64             `json:"creditMicro"`
}

func ValidatePaymentPackageCatalog(catalog PaymentPackageCatalog) error {
	if !canonicalUUID(catalog.Revision) || (catalog.PreviousRevision != "" && !canonicalUUID(catalog.PreviousRevision)) || catalog.Revision == catalog.PreviousRevision || len(catalog.Packages) > 100 {
		return ErrInvalidInput
	}
	seen := make(map[string]struct{}, len(catalog.Packages))
	recommended := map[PaymentTargetType]bool{}
	for i := range catalog.Packages {
		p := &catalog.Packages[i]
		p.SKU = strings.TrimSpace(p.SKU)
		p.Name = strings.TrimSpace(p.Name)
		p.Description = strings.TrimSpace(p.Description)
		if !packageSKURegex.MatchString(p.SKU) || p.Name == "" || len(p.Name) > 120 || len(p.Description) > 500 ||
			(p.TargetType != PaymentTargetPersonal && p.TargetType != PaymentTargetTeam) ||
			p.AmountFen <= 0 || p.AmountFen > 10_000_000 ||
			(p.Status != PaymentPackageDraft && p.Status != PaymentPackagePublished && p.Status != PaymentPackageArchived) || p.SortOrder < 0 {
			return ErrInvalidInput
		}
		if _, exists := seen[p.SKU]; exists {
			return fmt.Errorf("%w: duplicate sku", ErrInvalidInput)
		}
		seen[p.SKU] = struct{}{}
		if p.Recommended {
			if recommended[p.TargetType] {
				return fmt.Errorf("%w: multiple recommendations", ErrInvalidInput)
			}
			recommended[p.TargetType] = true
		}
	}
	return nil
}

func clonePaymentPackageCatalog(catalog *PaymentPackageCatalog) *PaymentPackageCatalog {
	if catalog == nil {
		return nil
	}
	copy := *catalog
	copy.Packages = append([]PaymentPackage(nil), catalog.Packages...)
	copy.PublishedAt = copyTime(catalog.PublishedAt)
	return &copy
}

func clonePackageSnapshot(snapshot *PaymentPackageSnapshot) *PaymentPackageSnapshot {
	if snapshot == nil {
		return nil
	}
	copy := *snapshot
	return &copy
}

func decodePaymentPackageCatalog(raw []byte) (*PaymentPackageCatalog, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var catalog PaymentPackageCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil || ValidatePaymentPackageCatalog(catalog) != nil {
		return nil, ErrInvalidInput
	}
	return &catalog, nil
}

func initialPaymentPackageCatalog() PaymentPackageCatalog {
	return PaymentPackageCatalog{
		Revision:    "00000000-0000-4000-8000-000000003035",
		PublishedBy: "system",
		Packages: []PaymentPackage{
			{SKU: "personal-lite", Name: "轻量包", Description: "个人一次性 credit 套餐", TargetType: PaymentTargetPersonal, AmountFen: 2900, Status: PaymentPackageDraft, SortOrder: 10},
			{SKU: "personal-standard", Name: "常用包", Description: "个人一次性 credit 套餐", TargetType: PaymentTargetPersonal, AmountFen: 9900, Status: PaymentPackageDraft, Recommended: true, SortOrder: 20},
			{SKU: "personal-creator", Name: "创作包", Description: "个人一次性 credit 套餐", TargetType: PaymentTargetPersonal, AmountFen: 29900, Status: PaymentPackageDraft, SortOrder: 30},
			{SKU: "team-collaboration", Name: "协作包", Description: "团队一次性 credit 套餐", TargetType: PaymentTargetTeam, AmountFen: 99900, Status: PaymentPackageDraft, Recommended: true, SortOrder: 10},
			{SKU: "team-studio", Name: "工作室包", Description: "团队一次性 credit 套餐", TargetType: PaymentTargetTeam, AmountFen: 299900, Status: PaymentPackageDraft, SortOrder: 20},
		},
	}
}

func packageSnapshot(catalog PaymentPackageCatalog, plan PaymentPackage) *PaymentPackageSnapshot {
	return &PaymentPackageSnapshot{SKU: plan.SKU, CatalogRevision: catalog.Revision, Name: plan.Name, Description: plan.Description,
		TargetType: plan.TargetType, AmountFen: plan.AmountFen, CreditMicro: plan.AmountFen * MicroCreditsPerFen}
}

func validatePackageCatalogUpdate(current, next PaymentPackageCatalog) error {
	for _, old := range current.Packages {
		found := false
		for _, plan := range next.Packages {
			if plan.SKU == old.SKU {
				if plan.TargetType != old.TargetType {
					return ErrInvalidInput
				}
				found = true
				break
			}
		}
		if !found {
			return ErrInvalidInput
		}
	}
	return nil
}
