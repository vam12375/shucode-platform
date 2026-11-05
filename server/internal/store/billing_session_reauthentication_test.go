package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testBillingSessionReauthentication(t *testing.T, ctx context.Context, s PlatformStore) {
	user := mustUser(t, ctx, s, "binding-relogin@store.test")
	device := func(userID string) string {
		t.Helper()
		id := uuid.NewString()
		if err := s.CreateDeviceToken(ctx, DeviceTokenRecord{ID: id, UserID: userID, TokenHash: uuid.NewString(), CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	originalDevice := device(user.ID)
	personal, err := s.EnsurePersonalAccount(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetBillingMode(ctx, user.ID, "personal"); err != nil {
		t.Fatal(err)
	}
	quota := "10.000000"
	if _, err := s.UpdateTeam(ctx, personal.ID, UpdateTeamPatch{QuotaCredits: &quota}); err != nil {
		t.Fatal(err)
	}
	input := BillingSessionBindingInput{SessionID: uuid.NewString(), WorkspaceIdentity: "remote:test", RemoteSessionID: "remote-attachment", ActorUserID: user.ID, DeviceTokenID: originalDevice}
	binding, err := s.CreateBillingSessionBinding(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	reservation := ReserveQuotaInput{ID: uuid.NewString(), RequestID: uuid.NewString(), TeamID: personal.ID, UserID: user.ID, BillingBindingID: binding.ID, DeviceTokenID: originalDevice}
	if result, err := s.AdmitUsage(ctx, reservation); err != nil || result != ReserveReserved {
		t.Fatalf("original admission: %s %v", result, err)
	}
	if err := s.SetBillingMode(ctx, user.ID, "team"); err != nil {
		t.Fatal(err)
	}

	devices := []string{device(user.ID), device(user.ID)}
	for _, id := range devices {
		if _, err := s.ResolveBillingSessionBinding(ctx, binding.ID, user.ID, id); !errors.Is(err, ErrBillingBindingForbidden) {
			t.Fatalf("unbound device: %v", err)
		}
	}
	var wg sync.WaitGroup
	for range 4 {
		for _, id := range devices {
			wg.Add(1)
			go func() {
				defer wg.Done()
				replay := input
				replay.DeviceTokenID = id
				got, err := s.CreateBillingSessionBinding(ctx, replay)
				if err != nil || got == nil {
					t.Errorf("same actor relogin: %v", err)
					return
				}
				got.CreatedAt = got.CreatedAt.UTC()
				if *got != *binding {
					t.Errorf("relogin changed frozen binding: %+v", got)
				}
			}()
		}
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	for _, id := range devices {
		if _, err := s.ResolveBillingSessionBinding(ctx, binding.ID, user.ID, id); err != nil {
			t.Fatal(err)
		}
		current := reservation
		current.DeviceTokenID = id
		if _, err := s.AdmitUsage(ctx, current); !errors.Is(err, ErrReservationMismatch) {
			t.Fatalf("old request cannot change device: %v", err)
		}
		current.ID, current.RequestID = uuid.NewString(), uuid.NewString()
		if result, err := s.AdmitUsage(ctx, current); err != nil || result != ReserveReserved {
			t.Fatalf("new admission: %s %v", result, err)
		}
		if err := s.ReleaseReservationByID(ctx, current.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	// 父会话不必先打开；fork 恢复仍由原 actor 继承固定钱包。
	child := input
	child.SessionID, child.ParentSessionID, child.DeviceTokenID = uuid.NewString(), input.SessionID, device(user.ID)
	inherited, err := s.CreateBillingSessionBinding(ctx, child)
	if err != nil || inherited.BillingAccountID != personal.ID {
		t.Fatalf("fork after relogin: %+v %v", inherited, err)
	}
	child.ParentSessionID, child.DeviceTokenID = "", device(user.ID)
	if replay, err := s.CreateBillingSessionBinding(ctx, child); err != nil || replay.ID != inherited.ID || replay.ParentSessionID != input.SessionID {
		t.Fatalf("media recovery without parent hint: %+v %v", replay, err)
	}
	conflict := input
	conflict.ParentSessionID, conflict.DeviceTokenID = uuid.NewString(), device(user.ID)
	if _, err := s.CreateBillingSessionBinding(ctx, conflict); !errors.Is(err, ErrBillingBindingConflict) {
		t.Fatalf("parent conflict: %v", err)
	}
	if _, err := s.ResolveBillingSessionBinding(ctx, binding.ID, user.ID, conflict.DeviceTokenID); !errors.Is(err, ErrBillingBindingForbidden) {
		t.Fatalf("conflict must not authorize: %v", err)
	}
	other := mustUser(t, ctx, s, "binding-other@store.test")
	crossActor := input
	crossActor.ActorUserID, crossActor.DeviceTokenID = other.ID, device(other.ID)
	if _, err := s.CreateBillingSessionBinding(ctx, crossActor); !errors.Is(err, ErrBillingBindingForbidden) {
		t.Fatalf("cross actor: %v", err)
	}
	crossActor.SessionID, crossActor.ParentSessionID = uuid.NewString(), input.SessionID
	if _, err := s.CreateBillingSessionBinding(ctx, crossActor); !errors.Is(err, ErrBillingBindingForbidden) {
		t.Fatalf("cross actor fork: %v", err)
	}
	if err := s.RevokeDeviceToken(ctx, originalDevice, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveBillingSessionBinding(ctx, binding.ID, user.ID, originalDevice); !errors.Is(err, ErrBillingBindingForbidden) {
		t.Fatalf("revoked original device: %v", err)
	}
	if _, err := s.CreateBillingSessionBinding(ctx, input); !errors.Is(err, ErrBillingBindingForbidden) {
		t.Fatalf("revoked original rebind: %v", err)
	}
	if _, err := s.ResolveBillingSessionBinding(ctx, binding.ID, user.ID, devices[0]); err != nil {
		t.Fatal(err)
	}
	settlement := UsageRecord{ID: uuid.NewString(), RequestID: reservation.RequestID, TeamID: personal.ID, UserID: user.ID, BillingBindingID: binding.ID, DeviceTokenID: originalDevice, Status: UsageStatusOk, CreatedAt: time.Now()}
	if err := s.SettleReservation(ctx, reservation.ID, settlement); err != nil {
		t.Fatalf("original snapshot settles after revoke: %v", err)
	}
}
