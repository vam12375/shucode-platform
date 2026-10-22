package apiapp

import (
	"context"
	"net/http/httptest"
	"strings"
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
	for _, secretField := range []string{"inputPricePerMtok", "outputPricePerMtok", "cacheReadPricePerMtok", "cacheWritePricePerMtok", "costMicro", "priceMicro", "priceVersion"} {
		if strings.Contains(rec.Body.String(), secretField) {
			t.Fatalf("客户端 user-config 不得包含价格字段 %q: %s", secretField, rec.Body.String())
		}
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

func TestUserConfigSyncIDDoesNotEncodeModelPrices(t *testing.T) {
	f, token, _ := newUserConfigPolicyFixture(t)
	first := requestUserConfig(f, token, "")
	if first.Code != 200 {
		t.Fatalf("首次 user-config: %d %s", first.Code, first.Body.String())
	}
	firstEnvelope := dataOf(t, first)["envelope"].(map[string]any)
	firstSyncID := firstEnvelope["syncId"]
	if _, err := f.store.UpsertModel(context.Background(), store.UpsertModelInput{
		ModelID: "model-a", DisplayName: "model-a", InputPricePerMtok: "987.654321", OutputPricePerMtok: "456.123456",
	}); err != nil {
		t.Fatal(err)
	}
	second := requestUserConfig(f, token, "")
	if second.Code != 200 {
		t.Fatalf("更新价格后 user-config: %d %s", second.Code, second.Body.String())
	}
	secondEnvelope := dataOf(t, second)["envelope"].(map[string]any)
	if secondEnvelope["syncId"] != firstSyncID {
		t.Fatalf("客户端同步标识不得编码未下发的价格: %v != %v", secondEnvelope["syncId"], firstSyncID)
	}
}

func TestUserConfigSyncsCatalogDisplayNameWithoutChangingModelIdentityOrCapabilities(t *testing.T) {
	for _, mode := range []string{modelConfigRecommended, modelConfigManual} {
		t.Run(mode, func(t *testing.T) {
			f, token, _ := newUserConfigPolicyFixture(t)
			ctx := context.Background()
			if _, err := f.store.UpsertModel(ctx, store.UpsertModelInput{
				ModelID: "model-a", DisplayName: "DeepSeek Flash", InputPricePerMtok: "1", OutputPricePerMtok: "2",
				ProvideUpstreamModel: true, UpstreamModel: "deepseek-flash",
			}); err != nil {
				t.Fatal(err)
			}
			if mode == modelConfigManual {
				if _, err := f.store.SetModelConfig(ctx, "model-a", mode, []byte(manualCapabilityConfig)); err != nil {
					t.Fatal(err)
				}
			}
			first := requestUserConfig(f, token, "")
			if first.Code != 200 {
				t.Fatalf("首次 user-config: %d %s", first.Code, first.Body.String())
			}
			firstEnvelope := dataOf(t, first)["envelope"].(map[string]any)
			etag := first.Header().Get("etag")
			if cached := requestUserConfig(f, token, etag); cached.Code != 304 {
				t.Fatalf("相同展示配置应命中缓存: %d %s", cached.Code, cached.Body.String())
			}
			if _, err := f.store.UpsertModel(ctx, store.UpsertModelInput{
				ModelID: "model-a", DisplayName: "  DeepSeek V4.1 Flash  ", InputPricePerMtok: "1", OutputPricePerMtok: "2",
			}); err != nil {
				t.Fatal(err)
			}
			second := requestUserConfig(f, token, etag)
			if second.Code != 200 || second.Header().Get("etag") == etag {
				t.Fatalf("仅修改显示名必须使旧 ETag 失效: %d %s", second.Code, second.Body.String())
			}
			secondEnvelope := dataOf(t, second)["envelope"].(map[string]any)
			if secondEnvelope["syncId"] == firstEnvelope["syncId"] {
				t.Fatal("仅修改显示名必须改变 syncId，避免客户端跳过应用")
			}
			personal := secondEnvelope["personalConfig"].(map[string]any)
			rules := personal["modelConfigRules"].(map[string]any)["providerModelRules"].([]any)
			var config map[string]any
			for _, value := range rules {
				rule := value.(map[string]any)
				if rule["modelId"] == "model-a" && rule["providerId"] == platformProviderID {
					config = rule["config"].(map[string]any)
				}
			}
			if config == nil || config["displayName"] != "DeepSeek V4.1 Flash" || config["enabled"] != true {
				t.Fatalf("显示名必须作为原身份规则的展示叶子下发: %v", rules)
			}
			if mode == modelConfigRecommended && len(config) != 2 {
				t.Fatalf("recommended 只下发启用与显示名，能力继续继承内置配置: %v", config)
			}
			if mode == modelConfigManual {
				if config["properties"].(map[string]any)["contextWindow"] != float64(64000) || config["optionSpecs"].(map[string]any)["maxOutputTokens"].(map[string]any)["max"] != float64(8000) {
					t.Fatalf("显示名不得改变手动能力: %v", config)
				}
			}
			model, err := f.store.GetModel(ctx, "model-a")
			if err != nil || model == nil || model.UpstreamModel != "deepseek-flash" {
				t.Fatalf("更新显示名不得改变上游路由: %+v %v", model, err)
			}
			if strings.Contains(second.Body.String(), "upstreamModel") || strings.Contains(second.Body.String(), "deepseek-flash") {
				t.Fatal("用户信封不得下发内部上游映射")
			}
		})
	}
}

func TestUserConfigDisplayNameIsOptionalSparseLeaf(t *testing.T) {
	input := BuildUserConfigInput{Models: []userConfigModel{{ModelID: "legacy-model"}}}
	unnamed := buildUserConfigEnvelope(input)
	personal := unnamed["personalConfig"].(map[string]any)
	rules := personal["modelConfigRules"].(map[string]any)["providerModelRules"].([]any)
	config := rules[0].(map[string]any)["config"].(map[string]any)
	if _, exists := config["displayName"]; exists {
		t.Fatal("旧目录缺省名称时必须省略叶子以保留客户端继承语义")
	}
	input.Models[0].DisplayName = "  "
	if computeUserConfigSyncID(input) != unnamed["syncId"] {
		t.Fatal("省略名称与空白名称的有效展示配置相同")
	}
	input.Models[0].DisplayName = "DeepSeek V4 Pro"
	named := computeUserConfigSyncID(input)
	if named == unnamed["syncId"] {
		t.Fatal("新增有效显示名必须改变同步身份")
	}
	input.Models[0].DisplayName = "  DeepSeek V4 Pro  "
	if computeUserConfigSyncID(input) != named {
		t.Fatal("名称序列化与同步身份必须采用相同的 trim 口径")
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
