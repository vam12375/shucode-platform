package gwapp

import (
	"context"
	"fmt"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func personalAccountForGateway(t *testing.T, f *gatewayFixture, quota string) *store.TeamRecord {
	t.Helper()
	ctx := context.Background()
	personal, err := f.store.EnsurePersonalAccount(ctx, f.userID)
	if err != nil {
		t.Fatal(err)
	}
	personal, err = f.store.UpdateTeam(ctx, personal.ID, store.UpdateTeamPatch{QuotaCredits: &quota})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetBillingMode(ctx, f.userID, "personal"); err != nil {
		t.Fatal(err)
	}
	return personal
}

func TestPersonalAndTeamGatewayChargesStaySeparate(t *testing.T) {
	f := newGatewayFixture(t, nil)
	ctx := context.Background()
	personal := personalAccountForGateway(t, f, "1")
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		fmt.Fprint(w, `{"usage":{"prompt_tokens":20,"completion_tokens":100}}`)
	})
	if rec := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5"}`); rec.Code != 200 {
		t.Fatalf("personal request: %d %s", rec.Code, rec.Body.String())
	}
	charged, _ := f.store.GetPersonalAccount(ctx, f.userID)
	if charged.UsedCredits != "0.001560" || f.team().UsedCredits != "0.000000" {
		t.Fatalf("personal usage crossed accounts: %+v %+v", charged, f.team())
	}
	personalUsage, err := f.store.ListUsage(ctx, store.UsageFilter{TeamID: personal.ID})
	if err != nil || len(personalUsage) != 1 || personalUsage[0].TeamID != personal.ID || personalUsage[0].UserID != f.userID {
		t.Fatalf("personal usage identity: %+v %v", personalUsage, err)
	}
	if err := f.store.SetBillingMode(ctx, f.userID, "team"); err != nil {
		t.Fatal(err)
	}
	if rec := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5"}`); rec.Code != 200 {
		t.Fatalf("team request: %d %s", rec.Code, rec.Body.String())
	}
	charged, _ = f.store.GetPersonalAccount(ctx, f.userID)
	if charged.UsedCredits != "0.001560" || f.team().UsedCredits != "0.001560" || len(f.usageRecords()) != 1 {
		t.Fatalf("team usage crossed accounts: %+v %+v", charged, f.team())
	}
}

func TestPersonalGatewayDoesNotNeedMembershipOrBorrowTeamBalance(t *testing.T) {
	f := newGatewayFixture(t, nil)
	ctx := context.Background()
	personalAccountForGateway(t, f, "0")
	called := false
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.Header().Set("content-type", "application/json")
		fmt.Fprint(w, `{"usage":{"prompt_tokens":20,"completion_tokens":100}}`)
	})
	assertOpenAIError(t, f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5"}`), 429, "rate_limit_error")
	if called || f.team().UsedCredits != "0.000000" {
		t.Fatal("empty personal account borrowed team balance")
	}
	other, err := f.store.CreateUser(ctx, store.UserInput{Email: "replacement-owner@test.local", PasswordHash: "x", DisplayName: "Owner", Role: store.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddMember(ctx, f.teamID, other.ID, store.MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RemoveMember(ctx, f.teamID, f.userID); err != nil {
		t.Fatal(err)
	}
	personalAccountForGateway(t, f, "1")
	if memberships, err := f.store.ListTeamsForUser(ctx, f.userID); err != nil || len(memberships) != 0 {
		t.Fatalf("expected no organization membership: %+v %v", memberships, err)
	}
	if rec := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5"}`); rec.Code != 200 {
		t.Fatalf("personal request without membership: %d %s", rec.Code, rec.Body.String())
	}
	if !called || f.team().UsedCredits != "0.000000" {
		t.Fatal("personal account must charge independently")
	}
}

func TestSwitchingBillingModeKeepsInflightReservationOwner(t *testing.T) {
	f := newGatewayFixture(t, nil)
	ctx := context.Background()
	personal := personalAccountForGateway(t, f, "1")
	if err := f.store.SetBillingMode(ctx, f.userID, "team"); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	finish := make(chan struct{})
	defer func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
	}()
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("content-type", "application/json")
		fmt.Fprint(w, `{"usage":{"prompt_tokens":20,"completion_tokens":100}}`)
	})
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5"}`) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not start")
	}
	if err := f.store.SetBillingMode(ctx, f.userID, "personal"); err != nil {
		t.Fatal(err)
	}
	close(finish)
	select {
	case rec := <-result:
		if rec.Code != 200 {
			t.Fatalf("request failed: %d %s", rec.Code, rec.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not finish")
	}
	personal, _ = f.store.GetTeam(ctx, personal.ID)
	if f.team().UsedCredits != "0.001560" || personal.UsedCredits != "0.000000" {
		t.Fatalf("inflight owner changed: %+v %+v", f.team(), personal)
	}
}
