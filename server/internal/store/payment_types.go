package store

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

type PaymentOrderStatus string
type PaymentTargetType string

const (
	PaymentPending        PaymentOrderStatus = "pending"
	PaymentPaid           PaymentOrderStatus = "paid"
	PaymentClosed         PaymentOrderStatus = "closed"
	PaymentProviderAlipay                    = "alipay"
	MicroCreditsPerFen    int64              = 10_000
	PaymentTargetPersonal PaymentTargetType  = "personal"
	PaymentTargetTeam     PaymentTargetType  = "team"
)

var (
	ErrPaymentNotFound      = errors.New("payment order not found")
	ErrPaymentConflict      = errors.New("payment order conflicts with frozen values")
	ErrPaymentClosed        = errors.New("payment order is closed")
	paymentReferencePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// PaymentOrderRecord freezes the payment destination and monetary values.
// NotifyPayload contains only sanitized transaction metadata, never buyer data.
type PaymentOrderRecord struct {
	ID              string
	OutTradeNo      string
	Provider        string
	ConfigurationID string
	TargetType      PaymentTargetType
	TargetID        string
	TeamID          string
	UserID          string
	AppID           string
	SellerID        string
	AmountFen       int64
	CreditMicro     int64
	Subject         string
	Status          PaymentOrderStatus
	TradeNo         string
	NotifyPayload   []byte
	TestPayment     bool
	PurchaseKind    string
	PackageSnapshot *PaymentPackageSnapshot
	CreatedAt       time.Time
	ExpiresAt       time.Time
	PaidAt          *time.Time
	ClosedAt        *time.Time
}

type PaymentSuccessInput struct {
	OutTradeNo    string
	TargetType    PaymentTargetType
	TargetID      string
	TeamID        string
	AppID         string
	SellerID      string
	AmountFen     int64
	TradeNo       string
	PaidAt        time.Time
	NotifyPayload []byte
}

type PaymentOrderFilter struct {
	TargetType      PaymentTargetType
	TargetID        string
	TeamID          string
	UserID          string
	Status          PaymentOrderStatus
	BeforeCreatedAt *time.Time
	BeforeID        string
	Limit           int64
}

func normalizePaymentOrder(record PaymentOrderRecord) (PaymentOrderRecord, error) {
	record.Subject = strings.TrimSpace(record.Subject)
	if record.PurchaseKind == "" {
		if record.TestPayment {
			record.PurchaseKind = PaymentPurchaseRootTest
		} else {
			record.PurchaseKind = PaymentPurchaseCustom
		}
	}
	if record.PurchaseKind == PaymentPurchasePlan && record.PackageSnapshot == nil || record.PurchaseKind != PaymentPurchasePlan && record.PackageSnapshot != nil ||
		(record.PurchaseKind != PaymentPurchasePlan && record.PurchaseKind != PaymentPurchaseCustom && record.PurchaseKind != PaymentPurchaseRootTest) ||
		(record.PurchaseKind == PaymentPurchaseRootTest) != record.TestPayment {
		return PaymentOrderRecord{}, ErrInvalidInput
	}
	if snapshot := record.PackageSnapshot; snapshot != nil {
		if !packageSKURegex.MatchString(snapshot.SKU) || !canonicalUUID(snapshot.CatalogRevision) || snapshot.Name == "" || len(snapshot.Name) > 120 ||
			snapshot.TargetType != record.TargetType || snapshot.AmountFen != record.AmountFen || snapshot.CreditMicro != record.CreditMicro {
			return PaymentOrderRecord{}, ErrInvalidInput
		}
	}
	if !canonicalUUID(record.ID) || !canonicalUUID(record.ConfigurationID) || !canonicalUUID(record.TeamID) || !canonicalUUID(record.UserID) || !canonicalUUID(record.TargetID) ||
		(record.TargetType != PaymentTargetPersonal && record.TargetType != PaymentTargetTeam) ||
		(record.TargetType == PaymentTargetPersonal && record.TargetID != record.UserID) ||
		(record.TargetType == PaymentTargetTeam && record.TargetID != record.TeamID) ||
		!paymentReferencePattern.MatchString(record.OutTradeNo) || record.Provider != PaymentProviderAlipay ||
		!paymentReferencePattern.MatchString(record.AppID) || !paymentReferencePattern.MatchString(record.SellerID) ||
		record.AmountFen <= 0 || record.AmountFen > maxCreditFieldMicro/MicroCreditsPerFen ||
		record.CreditMicro != record.AmountFen*MicroCreditsPerFen || record.Subject == "" || len(record.Subject) > 256 ||
		record.CreatedAt.IsZero() || !record.ExpiresAt.After(record.CreatedAt) ||
		(record.Status != "" && record.Status != PaymentPending) || record.TradeNo != "" || len(record.NotifyPayload) > 0 || record.PaidAt != nil || record.ClosedAt != nil {
		return PaymentOrderRecord{}, ErrInvalidInput
	}
	record.Status = PaymentPending
	record.CreatedAt, record.ExpiresAt = record.CreatedAt.UTC(), record.ExpiresAt.UTC()
	return record, nil
}

func samePaymentOrder(a, b PaymentOrderRecord) bool {
	if !(a.OutTradeNo == b.OutTradeNo && a.Provider == b.Provider && a.ConfigurationID == b.ConfigurationID && a.TargetType == b.TargetType && a.TargetID == b.TargetID && a.TeamID == b.TeamID && a.UserID == b.UserID && a.AppID == b.AppID && a.SellerID == b.SellerID && a.AmountFen == b.AmountFen && a.CreditMicro == b.CreditMicro && a.Subject == b.Subject && a.TestPayment == b.TestPayment && a.PurchaseKind == b.PurchaseKind) {
		return false
	}
	if a.PackageSnapshot == nil || b.PackageSnapshot == nil {
		return a.PackageSnapshot == nil && b.PackageSnapshot == nil
	}
	return *a.PackageSnapshot == *b.PackageSnapshot
}

func validatePaymentSuccess(input PaymentSuccessInput) error {
	if !paymentReferencePattern.MatchString(input.OutTradeNo) || !canonicalUUID(input.TeamID) || !canonicalUUID(input.TargetID) ||
		(input.TargetType != PaymentTargetPersonal && input.TargetType != PaymentTargetTeam) ||
		!paymentReferencePattern.MatchString(input.AppID) || !paymentReferencePattern.MatchString(input.SellerID) ||
		!paymentReferencePattern.MatchString(input.TradeNo) || input.AmountFen <= 0 || input.PaidAt.IsZero() ||
		len(input.NotifyPayload) > 4096 || (len(input.NotifyPayload) > 0 && !json.Valid(input.NotifyPayload)) {
		return ErrInvalidInput
	}
	return nil
}

func matchPaymentSuccess(record PaymentOrderRecord, input PaymentSuccessInput) error {
	if record.TargetType != input.TargetType || record.TargetID != input.TargetID || record.TeamID != input.TeamID || record.AppID != input.AppID || record.SellerID != input.SellerID || record.AmountFen != input.AmountFen || (record.TradeNo != "" && record.TradeNo != input.TradeNo) {
		return ErrPaymentConflict
	}
	return nil
}

func clonePaymentOrder(record PaymentOrderRecord) *PaymentOrderRecord {
	record.NotifyPayload = append([]byte(nil), record.NotifyPayload...)
	record.PaidAt, record.ClosedAt = copyTime(record.PaidAt), copyTime(record.ClosedAt)
	record.PackageSnapshot = clonePackageSnapshot(record.PackageSnapshot)
	return &record
}

func normalizePaymentFilter(filter PaymentOrderFilter) (PaymentOrderFilter, error) {
	if (filter.TargetType != "" && filter.TargetType != PaymentTargetPersonal && filter.TargetType != PaymentTargetTeam) || (filter.TargetID != "" && !canonicalUUID(filter.TargetID)) ||
		(filter.TeamID != "" && !canonicalUUID(filter.TeamID)) || (filter.UserID != "" && !canonicalUUID(filter.UserID)) ||
		(filter.Status != "" && filter.Status != PaymentPending && filter.Status != PaymentPaid && filter.Status != PaymentClosed) ||
		(filter.BeforeCreatedAt == nil && filter.BeforeID != "") || (filter.BeforeCreatedAt != nil && !canonicalUUID(filter.BeforeID)) {
		return filter, ErrInvalidInput
	}
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 100 {
		filter.Limit = 100
	}
	return filter, nil
}

func paymentAudit(record PaymentOrderRecord, input PaymentSuccessInput) InsertAuditLogInput {
	detail, _ := json.Marshal(map[string]any{"outTradeNo": record.OutTradeNo, "tradeNo": input.TradeNo, "amountFen": record.AmountFen, "creditMicro": record.CreditMicro, "targetType": record.TargetType, "targetId": record.TargetID, "testPayment": record.TestPayment})
	return InsertAuditLogInput{EventID: "payment:" + record.OutTradeNo, TeamID: record.TeamID, ActorUserID: record.UserID, Action: "payment.recharge", TargetType: "payment", TargetID: record.ID, Detail: detail}
}
