package store

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"sync"
	"testing"
	"time"
)

func testBillingSessionBindings(t *testing.T, ctx context.Context, s PlatformStore) {
	user := mustUser(t, ctx, s, "binding@store.test")
	token := DeviceTokenRecord{ID: uuid.NewString(), UserID: user.ID, Name: "binding", TokenHash: uuid.NewString(), CreatedAt: time.Now()}
	if err := s.CreateDeviceToken(ctx, token); err != nil {
		t.Fatal(err)
	}
	personal, err := s.EnsurePersonalAccount(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetBillingMode(ctx, user.ID, "personal"); err != nil {
		t.Fatal(err)
	}
	input := BillingSessionBindingInput{SessionID: uuid.NewString(), WorkspaceIdentity: "local:test", ActorUserID: user.ID, DeviceTokenID: token.ID}
	binding, err := s.CreateBillingSessionBinding(ctx, input)
	if err != nil || binding.BillingAccountID != personal.ID {
		t.Fatalf("create: %+v %v", binding, err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, e := s.CreateBillingSessionBinding(ctx, input)
			if e != nil || got.ID != binding.ID {
				t.Errorf("idempotent: %+v %v", got, e)
			}
		}()
	}
	wg.Wait()
	if err = s.SetBillingMode(ctx, user.ID, "team"); err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateBillingSessionBinding(ctx, input)
	if err != nil || replay.BillingAccountID != personal.ID {
		t.Fatalf("mode replay: %+v %v", replay, err)
	}
	child := input
	child.SessionID = uuid.NewString()
	child.ParentSessionID = input.SessionID
	inherited, err := s.CreateBillingSessionBinding(ctx, child)
	if err != nil || inherited.BillingAccountID != personal.ID {
		t.Fatalf("inherit: %+v %v", inherited, err)
	}
	future := input
	future.SessionID = uuid.NewString()
	if _, err = s.CreateBillingSessionBinding(ctx, future); !errors.Is(err, ErrBillingBindingForbidden) {
		t.Fatalf("missing team: %v", err)
	}
	child.ParentSessionID = "missing"
	child.SessionID = uuid.NewString()
	if _, err = s.CreateBillingSessionBinding(ctx, child); !errors.Is(err, ErrBillingBindingNotFound) {
		t.Fatalf("missing parent: %v", err)
	}
	second := token
	second.ID = uuid.NewString()
	second.TokenHash = uuid.NewString()
	if err = s.CreateDeviceToken(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveBillingSessionBinding(ctx, binding.ID, user.ID, second.ID); !errors.Is(err, ErrBillingBindingForbidden) {
		t.Fatalf("cross-device resolve: %v", err)
	}
	account, err := ResolveBillingBindingAccount(ctx, s, binding)
	if err != nil || account.Team.ID != personal.ID {
		t.Fatalf("bound personal: %+v %v", account, err)
	}
	reservationID, requestID := uuid.NewString(), uuid.NewString()
	personalQuota := "1.000000"
	if _, err := s.UpdateTeam(ctx, personal.ID, UpdateTeamPatch{QuotaCredits: &personalQuota}); err != nil {
		t.Fatal(err)
	}
	inputQuota := ReserveQuotaInput{BillingBindingID: binding.ID, DeviceTokenID: token.ID, ID: reservationID, RequestID: requestID, TeamID: personal.ID, UserID: user.ID}
	if result, err := s.AdmitUsage(ctx, inputQuota); err != nil || result != ReserveReserved {
		t.Fatalf("bound admission: %s %v", result, err)
	}
	t.Cleanup(func() {
		if err := s.ReleaseReservationByID(ctx, reservationID, time.Now().UTC()); err != nil {
			t.Errorf("close binding test reservation: %v", err)
		}
	})
	reservation, err := s.GetQuotaReservation(ctx, reservationID)
	if err != nil || reservation == nil || reservation.BillingBindingID != binding.ID || reservation.DeviceTokenID != token.ID {
		t.Fatalf("reservation binding snapshot: %+v %v", reservation, err)
	}
	if result, err := s.AdmitUsage(ctx, ReserveQuotaInput{BillingBindingID: binding.ID, DeviceTokenID: second.ID, ID: reservationID, RequestID: requestID, TeamID: personal.ID, UserID: user.ID}); !errors.Is(err, ErrBillingBindingForbidden) || result != ReserveDenied {
		t.Fatalf("wrong device duplicate admission: %s %v", result, err)
	}
	for _, field := range []string{"request", "binding", "missing-binding"} {
		changedQuota := inputQuota
		switch field {
		case "request":
			changedQuota.RequestID = uuid.NewString()
		case "binding":
			changedQuota.BillingBindingID = inherited.ID
		case "missing-binding":
			changedQuota.BillingBindingID = ""
		}
		if result, err := s.AdmitUsage(ctx, changedQuota); !errors.Is(err, ErrReservationMismatch) || result != ReserveDenied {
			t.Fatalf("bound duplicate changed %s: %s %v", field, result, err)
		}
	}
	missingDevice := inputQuota
	missingDevice.DeviceTokenID = ""
	if result, err := s.AdmitUsage(ctx, missingDevice); !errors.Is(err, ErrBillingBindingForbidden) || result != ReserveDenied {
		t.Fatalf("missing device duplicate admission: %s %v", result, err)
	}
	missingBinding := inputQuota
	missingBinding.BillingBindingID = uuid.NewString()
	if result, err := s.AdmitUsage(ctx, missingBinding); !errors.Is(err, ErrBillingBindingNotFound) || result != ReserveDenied {
		t.Fatalf("missing binding admission: %s %v", result, err)
	}
	wrongSettlement := UsageRecord{ID: uuid.NewString(), RequestID: requestID, TeamID: personal.ID, UserID: user.ID, BillingBindingID: binding.ID, DeviceTokenID: second.ID, Status: UsageStatusOk, CreatedAt: time.Now().UTC()}
	if err := s.SettleReservation(ctx, reservationID, wrongSettlement); !errors.Is(err, ErrReservationMismatch) {
		t.Fatalf("wrong device settlement: %v", err)
	}
	settlement := wrongSettlement
	settlement.DeviceTokenID = token.ID
	if err := s.SettleReservation(ctx, reservationID, settlement); err != nil {
		t.Fatalf("original device settlement: %v", err)
	}
	if err := s.SettleReservation(ctx, reservationID, wrongSettlement); !errors.Is(err, ErrReservationMismatch) {
		t.Fatalf("wrong device terminal replay: %v", err)
	}
	disabledAt := time.Now().UTC()
	if _, err := s.UpdateTeam(ctx, personal.ID, UpdateTeamPatch{HasDisabled: true, DisabledAt: &disabledAt}); err != nil {
		t.Fatal(err)
	}
	if result, err := s.AdmitUsage(ctx, inputQuota); err != nil || result != ReserveDuplicate {
		t.Fatalf("disabled account original replay: %s %v", result, err)
	}
	newQuota := inputQuota
	newQuota.ID, newQuota.RequestID = uuid.NewString(), uuid.NewString()
	if result, err := s.AdmitUsage(ctx, newQuota); err != nil || result != ReserveDeniedInactive {
		t.Fatalf("disabled account new admission: %s %v", result, err)
	}
	inputQuota.DeviceTokenID = second.ID
	if result, err := s.AdmitUsage(ctx, inputQuota); !errors.Is(err, ErrBillingBindingForbidden) || result != ReserveDenied {
		t.Fatalf("disabled account wrong device replay: %s %v", result, err)
	}
}
