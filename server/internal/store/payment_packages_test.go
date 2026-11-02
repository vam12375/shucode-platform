package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testPaymentPackages(t *testing.T, ctx context.Context, s PlatformStore) {
	catalog, err := s.GetPaymentPackageCatalog(ctx)
	if err != nil || catalog == nil || len(catalog.Packages) != 5 {
		t.Fatalf("initial package drafts: %+v %v", catalog, err)
	}
	for _, p := range catalog.Packages {
		if p.Status != PaymentPackageDraft {
			t.Fatalf("default packages must start as draft: %+v", p)
		}
	}
	owner := mustUser(t, ctx, s, "payment-packages@store.test")
	personal, err := s.EnsurePersonalAccount(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	first := *catalog
	first.Packages = append([]PaymentPackage(nil), catalog.Packages...)
	first.Packages[1].Status = PaymentPackagePublished
	first.Packages[1].Name = "常用包 v1"
	first.PublishedBy = owner.ID
	firstSaved, err := s.SavePaymentPackageCatalog(ctx, catalog.Revision, first)
	if err != nil || firstSaved.PreviousRevision != catalog.Revision || firstSaved.Revision == catalog.Revision {
		t.Fatalf("publish package: %+v %v", firstSaved, err)
	}

	orderInput := func(id string) PaymentOrderRecord {
		now := time.Now().UTC()
		return PaymentOrderRecord{ID: uuid.NewString(), OutTradeNo: "pkg_" + id, Provider: PaymentProviderAlipay,
			ConfigurationID: uuid.NewString(), TargetType: PaymentTargetPersonal, TargetID: owner.ID, TeamID: personal.ID,
			UserID: owner.ID, AppID: "2026000000000001", SellerID: "2088000000000001", Subject: "常用包",
			CreatedAt: now, ExpiresAt: now.Add(30 * time.Minute)}
	}
	order, err := s.CreatePlanPaymentOrder(ctx, orderInput("one"), firstSaved.Revision, "personal-standard")
	if err != nil || order.AmountFen != 9900 || order.CreditMicro != 99*1_000_000 || order.PackageSnapshot == nil || order.PackageSnapshot.Name != "常用包 v1" {
		t.Fatalf("frozen plan order: %+v %v", order, err)
	}

	changed := *firstSaved
	changed.Packages = append([]PaymentPackage(nil), firstSaved.Packages...)
	changed.Packages[1].Name = "常用包 v2"
	changed.Packages[1].AmountFen = 12300
	secondSaved, err := s.SavePaymentPackageCatalog(ctx, firstSaved.Revision, changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreatePlanPaymentOrder(ctx, orderInput("stale"), firstSaved.Revision, "personal-standard"); !errors.Is(err, ErrPaymentPackageCatalogConflict) {
		t.Fatalf("stale catalog order: %v", err)
	}
	// 幂等恢复先匹配旧单快照，版本已改价后仍返回最初订单。
	replay := *order
	replay.ID = uuid.NewString()
	replay.AmountFen = 0
	replay.CreditMicro = 0
	replayed, err := s.CreatePlanPaymentOrder(ctx, replay, firstSaved.Revision, "personal-standard")
	if err != nil || replayed.PackageSnapshot.Name != "常用包 v1" || replayed.AmountFen != 9900 {
		t.Fatalf("old order idempotency after repricing: %+v %v", replayed, err)
	}
	stored, err := s.GetPaymentOrder(ctx, order.OutTradeNo)
	if err != nil || stored.PackageSnapshot.Name != "常用包 v1" || stored.AmountFen != 9900 {
		t.Fatalf("order snapshot mutated by catalog: %+v %v", stored, err)
	}

	// 相同 expected revision 的并发写只允许一个版本成为 current。
	var wins atomic.Int64
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			candidate := *secondSaved
			candidate.Packages = append([]PaymentPackage(nil), secondSaved.Packages...)
			candidate.Packages[0].Description = uuid.NewString()
			candidate.PublishedBy = owner.ID
			if _, saveErr := s.SavePaymentPackageCatalog(ctx, secondSaved.Revision, candidate); saveErr == nil {
				wins.Add(1)
			} else if !errors.Is(saveErr, ErrPaymentPackageCatalogConflict) {
				t.Errorf("concurrent package CAS: %v", saveErr)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("exactly one package CAS must win, got %d", wins.Load())
	}
	if err := ValidatePaymentPackageCatalog(PaymentPackageCatalog{Revision: uuid.NewString(), Packages: []PaymentPackage{
		{SKU: "a", Name: "A", TargetType: PaymentTargetPersonal, AmountFen: 100, Status: PaymentPackagePublished, Recommended: true},
		{SKU: "b", Name: "B", TargetType: PaymentTargetPersonal, AmountFen: 200, Status: PaymentPackageDraft, Recommended: true},
	}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("multiple same-target recommendations rejected: %v", err)
	}
}
