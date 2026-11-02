package media

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestFixedFeePersistsBindingAndRequiresOriginalDeviceOnSettlement(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	deviceID := uuid.NewString()
	if err := f.s.Repo.Store.CreateDeviceToken(ctx, store.DeviceTokenRecord{ID: deviceID, UserID: f.user, Name: "media", TokenHash: uuid.NewString(), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	binding, err := f.s.Repo.Store.CreateBillingSessionBinding(ctx, store.BillingSessionBindingInput{SessionID: uuid.NewString(), WorkspaceIdentity: "media:trace", ActorUserID: f.user, DeviceTokenID: deviceID})
	if err != nil {
		t.Fatal(err)
	}
	bound := WithBillingIdentity(ctx, BillingIdentity{BindingID: binding.ID, DeviceID: deviceID})
	fee := newFixedFee(f.team, f.user, "media:trace:"+uuid.NewString(), "media:trace", 123_000)
	bindFixedFeeIdentity(bound, &fee)
	if err := f.s.admitFixedFee(bound, f.team, f.user, fee); err != nil {
		t.Fatal(err)
	}
	identity, err := f.s.Repo.Store.GetQuotaReservation(ctx, fee.ID)
	if err != nil || identity == nil || identity.BillingBindingID != binding.ID || identity.DeviceTokenID != deviceID {
		t.Fatalf("reservation trace missing: %+v %v", identity, err)
	}
	if err := f.s.settleFixedFee(bound, f.team, f.user, uuid.NewString(), fee); !errors.Is(err, store.ErrReservationMismatch) {
		t.Fatalf("wrong device settlement must fail: %v", err)
	}
	if err := f.s.settleFixedFee(bound, f.team, f.user, deviceID, fee); err != nil {
		t.Fatal(err)
	}
	usage, err := f.s.Repo.Store.ListUsage(ctx, store.UsageFilter{TeamID: f.team})
	if err != nil || len(usage) != 1 || usage[0].DeviceTokenID != deviceID {
		t.Fatalf("usage trace missing: %+v %v", usage, err)
	}
}

func TestMediaWorkerReservationRestoresFrozenProjectBinding(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	deviceID := uuid.NewString()
	if err := f.s.Repo.Store.CreateDeviceToken(ctx, store.DeviceTokenRecord{ID: deviceID, UserID: f.user, Name: "worker", TokenHash: uuid.NewString(), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	binding, err := f.s.Repo.Store.CreateBillingSessionBinding(ctx, store.BillingSessionBindingInput{SessionID: uuid.NewString(), WorkspaceIdentity: "media:worker", ActorUserID: f.user, DeviceTokenID: deviceID})
	if err != nil {
		t.Fatal(err)
	}
	boundCtx := WithBillingIdentity(ctx, BillingIdentity{BindingID: binding.ID, DeviceID: deviceID})
	brief := Brief{Mode: "auto", Product: "fixture product", AssetIDs: []string{"a"}, Platforms: []string{"douyin"}, VideoModel: "v", Resolution: "720p", AspectRatio: "16:9", Duration: 10, MaxCreditsMicro: 10_000_000}
	p, err := f.s.Create(boundCtx, f.team, f.user, deviceID, uuid.NewString(), brief)
	if err != nil {
		t.Fatal(err)
	}
	a := Analysis{TaskID: "analysis", InputID: "input", WorkspacePath: "/fixture"}
	p, err = f.s.Bind(boundCtx, f.team, p.ID, p.Version, a)
	if err != nil {
		t.Fatal(err)
	}
	plan := ShotPlan{SourceURL: "https://example.com/ref", SourceEnd: 5, Duration: 5, Framing: "close", Camera: "push", Action: "show", Purpose: "sell", Prompt: "product", AssetID: "a", Subtitle: "商品"}
	p, err = f.s.SetPlan(boundCtx, f.team, p.ID, p.Version, a.InputID, Plan{References: []Reference{{URL: plan.SourceURL, Title: "fixture", CapturedAt: time.Now(), Evidence: "observed", SelectionReason: "relevant"}}, Shots: []ShotPlan{plan, plan}})
	if err != nil {
		t.Fatal(err)
	}
	if p.BillingBindingID != binding.ID || p.BillingDeviceID != deviceID {
		t.Fatalf("project did not persist billing owner: %+v", p)
	}
	viewBytes, err := json.Marshal(p.View())
	if err != nil {
		t.Fatal(err)
	}
	if string(viewBytes) == "" || json.Valid(viewBytes) && (json.Valid(viewBytes) && containsJSONKey(viewBytes, "billingBindingId")) {
		t.Fatalf("internal billing ID leaked to ProjectView: %s", viewBytes)
	}
	if err := f.s.Repo.Store.SetBillingMode(ctx, f.user, "personal"); err != nil {
		t.Fatal(err)
	}
	f.s.Tick(ctx)
	updated := current(t, f, p)
	if updated.Shots[0].ReservationID == "" {
		t.Fatalf("worker did not admit shot: %+v", updated.Shots[0])
	}
	reservation, err := f.s.Repo.Store.GetQuotaReservation(ctx, updated.Shots[0].ReservationID)
	if err != nil || reservation == nil || reservation.TeamID != f.team || reservation.BillingBindingID != binding.ID || reservation.DeviceTokenID != deviceID {
		t.Fatalf("worker reservation lost frozen owner: %+v %v", reservation, err)
	}
	f.s.Tick(ctx)
	updated = current(t, f, updated)
	reservation, err = f.s.Repo.Store.GetQuotaReservation(ctx, updated.Shots[0].ReservationID)
	if err != nil || reservation == nil || reservation.BillingBindingID != binding.ID || reservation.DeviceTokenID != deviceID {
		t.Fatalf("worker retry changed binding: %+v %v", reservation, err)
	}
}

func containsJSONKey(raw []byte, key string) bool {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	_, ok := value[key]
	return ok
}
