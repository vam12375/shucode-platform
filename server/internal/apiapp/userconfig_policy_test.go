package apiapp

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

type userConfigTeamStore struct {
	store.PlatformStore
	memberships []store.TeamMembership
}

func (s *userConfigTeamStore) ListTeamsForUser(context.Context, string) ([]store.TeamMembership, error) {
	return s.memberships, nil
}

func newUserConfigPolicyFixture(t *testing.T) (*apiFixture, string, string) {
	t.Helper()
	f := newAPIEmptyFixture(t)
	ctx := context.Background()
	user, err := f.store.CreateUser(ctx, store.UserInput{Email: "config-policy@test.local", DisplayName: "策略测试", PasswordHash: "x", Role: store.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	team, err := f.store.CreateTeam(ctx, store.CreateTeamInput{Name: "配置策略团队", QuotaCredits: "100"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddMember(ctx, team.ID, user.ID, store.MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	token := crypto.GenerateDeviceToken()
	ciphertext, err := f.deps.SecretBox.Encrypt(token)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateDeviceToken(ctx, store.DeviceTokenRecord{
		ID: uuid.NewString(), UserID: user.ID, Name: "策略设备", TokenHash: crypto.HashToken(token), TokenCiphertext: ciphertext,
		TokenHint: crypto.DeviceTokenHint(token), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"model-a", "model-b"} {
		if _, err := f.store.UpsertModel(ctx, store.UpsertModelInput{ModelID: id, DisplayName: id, InputPricePerMtok: "1", OutputPricePerMtok: "2"}); err != nil {
			t.Fatal(err)
		}
	}
	return f, token, team.ID
}

func requestUserConfig(f *apiFixture, token, etag string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/api/v1/client/user-config", nil)
	req.Header.Set("authorization", "Bearer "+token)
	if etag != "" {
		req.Header.Set("if-none-match", etag)
	}
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	return rec
}

func TestUserConfigModelPolicyAndEmptyCatalog(t *testing.T) {
	f, token, teamID := newUserConfigPolicyFixture(t)
	ctx := context.Background()
	if _, err := f.store.SaveTeamPolicy(ctx, store.TeamPolicyRecord{TeamID: teamID, AllowedModelIDs: []string{"model-b"}}); err != nil {
		t.Fatal(err)
	}
	rec := requestUserConfig(f, token, "")
	if rec.Code != 200 {
		t.Fatalf("user-config: %d %s", rec.Code, rec.Body.String())
	}
	etag := rec.Header().Get("etag")
	personal := dataOf(t, rec)["envelope"].(map[string]any)["personalConfig"].(map[string]any)
	rule := personal["providerConfigRules"].(map[string]any)["providerRules"].([]any)[0].(map[string]any)
	ids := rule["config"].(map[string]any)["personalModelIds"].([]any)
	defaultModel := personal["defaultModelSelection"].(map[string]any)["modelId"]
	if rule["enabled"] != true || len(ids) != 1 || ids[0] != "model-b" || defaultModel != "model-b" {
		t.Fatalf("信封必须只包含授权模型: %s", rec.Body.String())
	}
	if _, err := f.store.SaveTeamPolicy(ctx, store.TeamPolicyRecord{TeamID: teamID, AllowedModelIDs: []string{"missing-model"}}); err != nil {
		t.Fatal(err)
	}
	rec = requestUserConfig(f, token, etag)
	if rec.Code != 200 || rec.Header().Get("etag") == etag {
		t.Fatalf("授权变更必须使旧 ETag 失效: %d %s", rec.Code, rec.Body.String())
	}
	personal = dataOf(t, rec)["envelope"].(map[string]any)["personalConfig"].(map[string]any)
	rule = personal["providerConfigRules"].(map[string]any)["providerRules"].([]any)[0].(map[string]any)
	ids = rule["config"].(map[string]any)["personalModelIds"].([]any)
	modelRules := personal["modelConfigRules"].(map[string]any)["providerModelRules"].([]any)
	if rule["enabled"] != false || len(ids) != 0 || len(modelRules) != 0 {
		t.Fatalf("空授权模型必须关闭平台 provider: %s", rec.Body.String())
	}
	if _, exists := personal["defaultModelSelection"]; exists {
		t.Fatalf("无授权模型不得下发默认选择: %s", rec.Body.String())
	}
}

func TestUserConfigRejectsInactiveOrAmbiguousMembership(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		setup  func(team store.TeamRecord) []store.TeamMembership
	}{
		{"no team", 403, func(store.TeamRecord) []store.TeamMembership { return nil }},
		{"disabled team", 403, func(team store.TeamRecord) []store.TeamMembership {
			now := time.Now()
			team.DisabledAt = &now
			return []store.TeamMembership{{Team: team}}
		}},
		{"disabled membership", 403, func(team store.TeamRecord) []store.TeamMembership {
			now := time.Now()
			return []store.TeamMembership{{Team: team, MemberDisabledAt: &now}}
		}},
		{"multiple teams", 409, func(team store.TeamRecord) []store.TeamMembership {
			other := team
			other.ID = "legacy-other-team"
			return []store.TeamMembership{{Team: team}, {Team: other}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, token, teamID := newUserConfigPolicyFixture(t)
			team, err := f.store.GetTeam(context.Background(), teamID)
			if err != nil {
				t.Fatal(err)
			}
			wrapped := &userConfigTeamStore{PlatformStore: f.store, memberships: test.setup(*team)}
			f.deps.Store = wrapped
			rec := requestUserConfig(f, token, "")
			if rec.Code != test.status {
				t.Fatalf("user-config 不得静默选择或使用已停用团队: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}
