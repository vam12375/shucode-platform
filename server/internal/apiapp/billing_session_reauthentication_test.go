package apiapp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func requestBillingBinding(f *apiFixture, token string, body map[string]any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/client/payments/session-bindings", strings.NewReader(string(data)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	return rec
}

func TestBillingSessionReloginKeepsAccountAndRejectsOtherActor(t *testing.T) {
	f, token, _ := newUserConfigPolicyFixture(t)
	ctx := context.Background()
	device, err := f.store.FindDeviceTokenByHash(ctx, crypto.HashToken(token))
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"sessionId": uuid.NewString(), "workspaceIdentity": "local:relogin"}
	first := requestBillingBinding(f, token, body)
	if first.Code != 200 {
		t.Fatal(first.Body.String())
	}
	newToken := createDeviceTokenForTest(t, f, device.UserID)
	if err := f.store.SetBillingMode(ctx, device.UserID, "personal"); err != nil {
		t.Fatal(err)
	}
	replay := requestBillingBinding(f, newToken, body)
	if replay.Code != 200 || replay.Body.String() != first.Body.String() {
		t.Fatalf("relogin: %d %s", replay.Code, replay.Body.String())
	}
	other, err := f.store.CreateUser(ctx, store.UserInput{Email: "other-binding@test.local", PasswordHash: "x", Role: store.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	otherToken := createDeviceTokenForTest(t, f, other.ID)
	rejected := requestBillingBinding(f, otherToken, body)
	assertBillingReason(t, rejected, 403, "billing_binding_actor_mismatch")
	if !strings.Contains(rejected.Body.String(), "创建此会话的账号") {
		t.Fatal(rejected.Body.String())
	}
	conflict := map[string]any{"sessionId": body["sessionId"], "workspaceIdentity": body["workspaceIdentity"], "parentSessionId": "different-parent"}
	assertBillingReason(t, requestBillingBinding(f, newToken, conflict), 409, "billing_binding_identity_conflict")
}

func assertBillingReason(t *testing.T, rec *httptest.ResponseRecorder, status int, reason string) {
	t.Helper()
	var payload struct {
		Details struct {
			Reason string `json:"reason"`
		} `json:"details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if rec.Code != status || payload.Details.Reason != reason {
		t.Fatalf("want %d/%s: %d %s", status, reason, rec.Code, rec.Body.String())
	}
}

func TestBillingSessionBindingReportsDistinctCauses(t *testing.T) {
	f, token, teamID := newUserConfigPolicyFixture(t)
	ctx := context.Background()
	body := map[string]any{"sessionId": uuid.NewString(), "workspaceIdentity": "local:causes"}
	first := requestBillingBinding(f, token, body)
	if first.Code != 200 {
		t.Fatal(first.Body.String())
	}
	now := time.Now()
	if _, err := f.store.UpdateTeam(ctx, teamID, store.UpdateTeamPatch{HasDisabled: true, DisabledAt: &now}); err != nil {
		t.Fatal(err)
	}
	assertBillingReason(t, requestBillingBinding(f, token, body), 403, "billing_binding_account_unavailable")
	body["sessionId"] = uuid.NewString()
	assertBillingReason(t, requestBillingBinding(f, token, body), 403, "billing_binding_no_active_team")
	f.deps.Store = &billingBindingErrorStore{PlatformStore: f.store, err: store.ErrAmbiguousTeam}
	assertBillingReason(t, requestBillingBinding(f, token, body), 409, "billing_binding_ambiguous_team")
	f.deps.Store = f.store
	body["parentSessionId"] = "missing-parent"
	assertBillingReason(t, requestBillingBinding(f, token, body), 404, "billing_binding_parent_not_found")
}

type billingBindingErrorStore struct {
	store.PlatformStore
	err error
}

func (s *billingBindingErrorStore) CreateBillingSessionBinding(context.Context, store.BillingSessionBindingInput) (*store.BillingSessionBinding, error) {
	return nil, s.err
}

func TestBillingBindingDeviceMismatchReason(t *testing.T) {
	f, token, _ := newUserConfigPolicyFixture(t)
	f.deps.Store = &billingBindingErrorStore{PlatformStore: f.store, err: store.ErrBillingBindingDeviceMismatch}
	assertBillingReason(t, requestBillingBinding(f, token, map[string]any{"sessionId": "session", "workspaceIdentity": "local:test"}), 403, "billing_binding_device_mismatch")
}
