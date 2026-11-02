package apiapp

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestMediaBillingBindingKeepsAccountAndChecksCurrentPermission(t *testing.T) {
	f, token, teamID := newUserConfigPolicyFixture(t)
	ctx := context.Background()
	device, err := f.store.FindDeviceTokenByHash(ctx, crypto.HashToken(token))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetBillingMode(ctx, device.UserID, "personal"); err != nil {
		t.Fatal(err)
	}
	personal, err := f.store.GetPersonalAccount(ctx, device.UserID)
	if err != nil {
		t.Fatal(err)
	}
	createBinding := func() *store.BillingSessionBinding {
		t.Helper()
		b, err := f.store.CreateBillingSessionBinding(ctx, store.BillingSessionBindingInput{SessionID: uuid.NewString(), WorkspaceIdentity: "local:media-binding", ActorUserID: device.UserID, DeviceTokenID: device.ID})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	personalBinding := createBinding()
	if err := f.store.SetBillingMode(ctx, device.UserID, "team"); err != nil {
		t.Fatal(err)
	}
	teamBinding := createBinding()
	request := func(deviceToken, binding string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/media/overview?teamId=ignored", nil)
		req.Header.Set("Authorization", "Bearer "+deviceToken)
		if binding != "" {
			req.Header.Set("x-mendao-billing-binding", binding)
		}
		rec := httptest.NewRecorder()
		f.engine.ServeHTTP(rec, req)
		return rec
	}
	for _, tc := range []struct{ binding, account string }{{personalBinding.ID, personal.ID}, {teamBinding.ID, teamID}, {"", teamID}} {
		rec := request(token, tc.binding)
		if rec.Code != 200 {
			t.Fatalf("overview: %d %s", rec.Code, rec.Body.String())
		}
		if dataOf(t, rec)["team"].(map[string]any)["id"] != tc.account {
			t.Fatalf("wrong account: %s", rec.Body.String())
		}
	}
	otherToken := createDeviceTokenForTest(t, f, device.UserID)
	for _, tc := range []struct{ token, binding string }{{token, "invalid"}, {otherToken, personalBinding.ID}} {
		rec := request(tc.token, tc.binding)
		if rec.Code != 403 {
			t.Fatalf("invalid binding: %d %s", rec.Code, rec.Body.String())
		}
	}
	replacement, err := f.store.CreateUser(ctx, store.UserInput{Email: "media-binding-owner@test.local", PasswordHash: "x", DisplayName: "Owner", Role: store.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddMember(ctx, teamID, replacement.ID, store.MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RemoveMember(ctx, teamID, device.UserID); err != nil {
		t.Fatal(err)
	}
	if rec := request(token, teamBinding.ID); rec.Code != 403 {
		t.Fatalf("revoked member: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request(token, personalBinding.ID); rec.Code != 200 {
		t.Fatalf("personal independent of membership: %d %s", rec.Code, rec.Body.String())
	}
}
