package apiapp

import (
	"context"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"strings"
	"testing"
)

func TestPersonalBillingAccountAndPublicCreditProjection(t *testing.T) {
	f := newAPIEmptyFixture(t)
	ctx := context.Background()
	user, err := f.store.CreateUser(ctx, store.UserInput{Email: "personal-credit@test.local", PasswordHash: "x", DisplayName: "个人余额", Role: store.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	personal, err := f.store.EnsurePersonalAccount(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	quota := "3.000000"
	if _, err = f.store.UpdateTeam(ctx, personal.ID, store.UpdateTeamPatch{QuotaCredits: &quota}); err != nil {
		t.Fatal(err)
	}
	if err = f.store.SetBillingMode(ctx, user.ID, "personal"); err != nil {
		t.Fatal(err)
	}
	token := createDeviceTokenForTest(t, f, user.ID)
	rec := requestClientCredit(t, f, token)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"teamName":"个人余额"`) || !strings.Contains(rec.Body.String(), personal.ID) {
		t.Fatalf("personal credit projection: %d %s", rec.Code, rec.Body.String())
	}
	if mode, err := f.store.GetBillingMode(ctx, user.ID); err != nil || mode != "personal" {
		t.Fatalf("billing mode: %s %v", mode, err)
	}
}

func TestPersonalUserConfigWorksWithoutOrganizationMembership(t *testing.T) {
	f := newAPIEmptyFixture(t)
	ctx := context.Background()
	user, err := f.store.CreateUser(ctx, store.UserInput{Email: "personal-config@test.local", PasswordHash: "x", DisplayName: "个人配置", Role: store.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.SetBillingMode(ctx, user.ID, "personal"); err != nil {
		t.Fatal(err)
	}
	token := createDeviceTokenForTest(t, f, user.ID)
	rec := requestUserConfig(f, token, "")
	if rec.Code != 200 {
		t.Fatalf("personal user-config: %d %s", rec.Code, rec.Body.String())
	}
	envelope := dataOf(t, rec)["envelope"].(map[string]any)
	if _, exists := envelope["teamId"]; exists {
		t.Fatalf("strict Provisioning envelope must not add teamId: %s", rec.Body.String())
	}
	personalSyncID := envelope["syncId"]
	personal, err := f.store.GetPersonalAccount(ctx, user.ID)
	if err != nil || personal == nil {
		t.Fatalf("personal account: %+v %v", personal, err)
	}
	credit := requestClientCredit(t, f, token)
	if credit.Code != 200 || dataOf(t, credit)["teamId"] != personal.ID {
		t.Fatalf("credit must identify personal account: %d %s", credit.Code, credit.Body.String())
	}
	if organization, err := store.ResolveUserTeam(ctx, f.store, user.ID); err != nil || organization != nil {
		t.Fatalf("personal account must not grant organization membership: %+v %v", organization, err)
	}
	team, err := f.store.CreateTeam(ctx, store.CreateTeamInput{Name: "Organization", QuotaCredits: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddMember(ctx, team.ID, user.ID, store.MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetBillingMode(ctx, user.ID, "team"); err != nil {
		t.Fatal(err)
	}
	teamConfig := requestUserConfig(f, token, rec.Header().Get("etag"))
	if teamConfig.Code != 200 {
		t.Fatalf("account switch must invalidate ETag: %d %s", teamConfig.Code, teamConfig.Body.String())
	}
	teamEnvelope := dataOf(t, teamConfig)["envelope"].(map[string]any)
	if teamEnvelope["syncId"] == personalSyncID {
		t.Fatal("account switch must change syncId")
	}
	if _, exists := teamEnvelope["teamId"]; exists {
		t.Fatal("team mode must preserve strict envelope schema")
	}
	credit = requestClientCredit(t, f, token)
	if credit.Code != 200 || dataOf(t, credit)["teamId"] != team.ID {
		t.Fatalf("credit must identify selected team account: %d %s", credit.Code, credit.Body.String())
	}
}
