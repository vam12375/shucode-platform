package gwapp

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

type tenantPolicyStore struct {
	store.PlatformStore
	memberships  []store.TeamMembership
	overrideTeam bool
	reservedUser string
}

func (s *tenantPolicyStore) ListTeamsForUser(ctx context.Context, userID string) ([]store.TeamMembership, error) {
	if s.overrideTeam {
		return s.memberships, nil
	}
	return s.PlatformStore.ListTeamsForUser(ctx, userID)
}

func (s *tenantPolicyStore) ReserveQuota(ctx context.Context, input store.ReserveQuotaInput) (store.ReserveResult, error) {
	s.reservedUser = input.UserID
	return s.PlatformStore.ReserveQuota(ctx, input)
}

func TestGatewayRequiresUnambiguousActiveMembership(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		setup  func(*gatewayFixture, *tenantPolicyStore)
	}{
		{"no team", 403, func(_ *gatewayFixture, s *tenantPolicyStore) { s.overrideTeam = true }},
		{"disabled team", 403, func(f *gatewayFixture, s *tenantPolicyStore) {
			now := time.Now()
			team := *f.team()
			team.DisabledAt = &now
			s.overrideTeam, s.memberships = true, []store.TeamMembership{{Team: team}}
		}},
		{"disabled membership", 403, func(f *gatewayFixture, s *tenantPolicyStore) {
			now := time.Now()
			s.overrideTeam, s.memberships = true, []store.TeamMembership{{Team: *f.team(), MemberDisabledAt: &now}}
		}},
		{"multiple teams", 409, func(f *gatewayFixture, s *tenantPolicyStore) {
			team := *f.team()
			other := team
			other.ID = "legacy-other-team"
			s.overrideTeam, s.memberships = true, []store.TeamMembership{{Team: team}, {Team: other}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newGatewayFixture(t, func(_ *Env, f *gatewayFixture) {
				wrapped := &tenantPolicyStore{PlatformStore: f.store}
				test.setup(f, wrapped)
				f.store = wrapped
			})
			assertOpenAIError(t, f.do("GET", "/v1/models", f.token, ""), test.status, "permission_error")
			for _, route := range []string{"/v1/chat/completions", "/v1/responses"} {
				assertOpenAIError(t, f.do("POST", route, f.token, `{"model":"claude-sonnet-4-5"}`), test.status, "permission_error")
			}
			assertAnthropicError(t, f.do("POST", "/v1/messages", f.token, `{"model":"claude-sonnet-4-5"}`), test.status, "permission_error")
			if req, _ := f.upstreamRequest(); req != nil || len(f.usageRecords()) != 0 {
				t.Fatal("无有效团队的请求不应触达上游或记录消费")
			}
		})
	}
}

func TestGatewayTeamModelPolicyCannotBeBypassed(t *testing.T) {
	f := newGatewayFixture(t, nil)
	ctx := context.Background()
	if _, err := f.store.UpsertModel(ctx, store.UpsertModelInput{
		ModelID: "allowed-model", DisplayName: "Allowed", InputPricePerMtok: "1", OutputPricePerMtok: "1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SaveTeamPolicy(ctx, store.TeamPolicyRecord{TeamID: f.teamID, AllowedModelIDs: []string{"allowed-model"}}); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/v1/chat/completions", "/v1/responses"} {
		assertOpenAIError(t, f.do("POST", route, f.token, `{"model":"claude-sonnet-4-5"}`), 403, "permission_error")
	}
	assertAnthropicError(t, f.do("POST", "/v1/messages", f.token, `{"model":"claude-sonnet-4-5"}`), 403, "permission_error")
	if req, _ := f.upstreamRequest(); req != nil || len(f.usageRecords()) != 0 {
		t.Fatal("未授权模型不应触达上游或记录消费")
	}
	rec := f.do("GET", "/v1/models", f.token, "")
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(body.Data) != 1 || body.Data[0].ID != "allowed-model" {
		t.Fatalf("模型列表必须遵循相同白名单: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := f.store.SaveTeamPolicy(ctx, store.TeamPolicyRecord{TeamID: f.teamID, AllowedModelIDs: []string{"missing-model"}}); err != nil {
		t.Fatal(err)
	}
	rec = f.do("GET", "/v1/models", f.token, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(body.Data) != 0 {
		t.Fatalf("空授权目录不能回退为所有模型: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGatewayReservationIncludesMemberIdentity(t *testing.T) {
	var wrapped *tenantPolicyStore
	f := newGatewayFixture(t, func(_ *Env, f *gatewayFixture) {
		wrapped = &tenantPolicyStore{PlatformStore: f.store}
		f.store = wrapped
	})
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	})
	rec := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5"}`)
	if rec.Code != 200 || wrapped.reservedUser != f.userID {
		t.Fatalf("额度准入必须携带成员身份: status=%d user=%q body=%s", rec.Code, wrapped.reservedUser, rec.Body.String())
	}
}

func TestGatewayEnforcesStoredBudgetsBeforeUpstream(t *testing.T) {
	for _, kind := range []string{"member monthly", "team daily", "team monthly"} {
		t.Run(kind, func(t *testing.T) {
			f := newGatewayFixture(t, nil)
			ctx := context.Background()
			budget := "0.000001"
			if kind == "member monthly" {
				if _, err := f.store.UpdateTeamMember(ctx, f.teamID, f.userID, store.TeamMemberPatch{MonthlyBudgetCredits: &budget}); err != nil {
					t.Fatal(err)
				}
			} else {
				policy := store.TeamPolicyRecord{TeamID: f.teamID}
				if kind == "team daily" {
					policy.DailyBudgetCredits = budget
				} else {
					policy.MonthlyBudgetCredits = budget
				}
				if _, err := f.store.SaveTeamPolicy(ctx, policy); err != nil {
					t.Fatal(err)
				}
			}
			assertOpenAIError(t, f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5"}`), 429, "rate_limit_error")
			if req, _ := f.upstreamRequest(); req != nil || len(f.usageRecords()) != 0 || f.team().ReservedCredits != "0.000000" {
				t.Fatal("预算准入失败不得触达上游、记录消费或留下预留")
			}
		})
	}
}
