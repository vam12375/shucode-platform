package apiapp

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func analysisConfigFixture(t *testing.T) (*apiFixture, string, *media.Project) {
	t.Helper()
	f, token, team := newUserConfigPolicyFixture(t)
	ctx := context.Background()
	device, err := f.store.FindDeviceTokenByHash(ctx, crypto.HashToken(token))
	if err != nil || device == nil {
		t.Fatal("missing fixture device")
	}
	fee := &media.FixedFee{ID: uuid.NewString(), RequestID: "test-analysis:" + uuid.NewString(), Model: "media:source-analysis", Micro: media.ReferenceAnalysisFeeMicro, CreatedAt: time.Now().UTC()}
	if result, err := f.store.AdmitUsage(ctx, store.ReserveQuotaInput{ID: fee.ID, TeamID: team, UserID: device.UserID, RequestID: fee.RequestID, Micro: fee.Micro}); err != nil || result != store.ReserveReserved {
		t.Fatalf("analysis admission: %s %v", result, err)
	}
	p := &media.Project{ID: uuid.NewString(), Version: 1, TeamID: team, UserID: device.UserID, DeviceID: device.ID, Status: "analyzing", Brief: media.Brief{ReferenceAssetID: "reference-video"}, Analysis: &media.Analysis{InputID: uuid.NewString()}, AnalysisFee: fee}
	if err = f.deps.Media.Repo.Save(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	return f, token, p
}

func requestAnalysisConfig(f *apiFixture, token, id, model string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/api/v1/media/projects/"+id+"/analysis-config?modelId="+model, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, req)
	return w
}

func TestAnalysisConfigIssuesSingleModelScopedProviderAndKeepsNormalConfig(t *testing.T) {
	f, token, p := analysisConfigFixture(t)
	w := requestAnalysisConfig(f, token, p.ID, "model-a")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("analysis config: %d %s", w.Code, w.Body.String())
	}
	data := dataOf(t, w)
	providerID := "platform:video-analysis:" + p.ID + ":" + p.Analysis.InputID
	if data["providerId"] != providerID {
		t.Fatalf("wrong scoped provider: %v", data["providerId"])
	}
	envelope := data["envelope"].(map[string]any)
	personal := envelope["personalConfig"].(map[string]any)
	rules := personal["providerConfigRules"].(map[string]any)["providerRules"].([]any)
	if len(rules) != 1 {
		t.Fatal("analysis config must contain one provider")
	}
	rule := rules[0].(map[string]any)
	config := rule["config"].(map[string]any)
	if rule["providerId"] != providerID || config["access"].(map[string]any)["apiKey"] != token {
		t.Fatal("analysis config did not retain normal device authentication")
	}
	models := config["personalModelIds"].([]any)
	if len(models) != 1 || models[0] != "model-a" {
		t.Fatalf("unexpected model set: %+v", models)
	}
	modelRules := personal["modelConfigRules"].(map[string]any)["providerModelRules"].([]any)
	if len(modelRules) != 1 || modelRules[0].(map[string]any)["providerId"] != providerID || personal["defaultModelSelection"].(map[string]any)["providerId"] != providerID {
		t.Fatal("scoped provider identities are inconsistent")
	}
	ticket := config["api"].(map[string]any)["headers"].(map[string]any)[media.AnalysisScopeHeader].(string)
	if err := media.ValidateAnalysisScope(context.Background(), f.deps.Media.Repo, f.deps.SecretBox, ticket, p.TeamID, p.UserID, p.DeviceID, "model-a", 1, f.deps.Now()); err != nil {
		t.Fatalf("issued scope cannot be validated: %v", err)
	}
	second := requestAnalysisConfig(f, token, p.ID, "model-a")
	if second.Code != 200 || dataOf(t, second)["envelope"].(map[string]any)["syncId"] == envelope["syncId"] {
		t.Fatal("refreshed scope must change config content identity")
	}
	normal := requestUserConfig(f, token, "")
	if normal.Code != 200 || strings.Contains(normal.Body.String(), media.AnalysisScopeHeader) || strings.Contains(normal.Body.String(), providerID) {
		t.Fatal("normal gateway config was affected by scoped analysis")
	}
}

func TestAnalysisConfigRejectsWrongIdentityModelAndEndedScope(t *testing.T) {
	for _, kind := range []string{"no-auth", "other-device", "other-user", "model-policy", "missing-model", "ended", "released", "legacy", "no-box"} {
		t.Run(kind, func(t *testing.T) {
			f, token, p := analysisConfigFixture(t)
			ctx := context.Background()
			model, status := "model-a", 403
			var err error
			switch kind {
			case "no-auth":
				token, status = "", 401
			case "other-device", "other-user":
				userID := p.UserID
				if kind == "other-user" {
					user, createErr := f.store.CreateUser(ctx, store.UserInput{Email: "other-analysis-config@test.local", DisplayName: "other", PasswordHash: "x", Role: store.RoleUser})
					if createErr != nil {
						t.Fatal(createErr)
					}
					userID = user.ID
					if err = f.store.AddMember(ctx, p.TeamID, userID, store.MemberRoleMember); err != nil {
						t.Fatal(err)
					}
				}
				token = crypto.GenerateDeviceToken()
				err = f.store.CreateDeviceToken(ctx, store.DeviceTokenRecord{ID: uuid.NewString(), UserID: userID, Name: "other", TokenHash: crypto.HashToken(token), TokenCiphertext: "v1.a.b.c", CreatedAt: time.Now()})
			case "model-policy":
				_, err = f.store.SaveTeamPolicy(ctx, store.TeamPolicyRecord{TeamID: p.TeamID, AllowedModelIDs: []string{"model-b"}})
			case "missing-model":
				model, status = "", 400
			case "ended", "legacy":
				next := *p
				if kind == "ended" {
					next.Status = "review"
				} else {
					next.Brief.ReferenceAssetID = ""
				}
				err = f.deps.Media.Repo.Save(ctx, &next, p)
			case "released":
				err = f.store.ReleaseReservationByID(ctx, p.AnalysisFee.ID, time.Now())
			case "no-box":
				f.deps.SecretBox, status = nil, 503
			}
			if err != nil {
				t.Fatal(err)
			}
			w := requestAnalysisConfig(f, token, p.ID, model)
			if w.Code != status || strings.Contains(w.Body.String(), `"envelope"`) || strings.Contains(w.Body.String(), media.AnalysisScopeHeader) {
				t.Fatalf("invalid analysis config was issued: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
