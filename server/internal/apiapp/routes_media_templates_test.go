package apiapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"testing"
)

func TestTemplateCatalogAndReviewsAreRootOnly(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	for _, path := range []string{"/console/api/media/templates", "/console/api/media/templates/reviews"} {
		if response := f.do("GET", path, cookie, ""); response.Code != 200 {
			t.Fatalf("root GET %s: %d %s", path, response.Code, response.Body.String())
		}
		if response := f.do("GET", path, "", ""); response.Code != 401 {
			t.Fatal("anonymous review access")
		}
	}
	const path = "/console/api/media/templates/reviews"
	if response := f.do("POST", path, cookie, `{"templateId":"fake","templateVersion":"1","projectTeamId":"fake","projectId":"fake","score":99,"approved":true,"note":"fake","modelId":"client-model"}`); response.Code != 400 {
		t.Fatalf("self reported model accepted: %d", response.Code)
	}
	if response := f.do("POST", path, "", `{}`); response.Code != 401 {
		t.Fatal("anonymous review write")
	}
}

func TestTemplateOverviewVerificationRespectsTeamModelWhitelist(t *testing.T) {
	f, token, team := newUserConfigPolicyFixture(t)
	ctx := context.Background()
	templates, err := media.VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	models := []media.Model{}
	reviews := []media.TemplateReview{}
	for _, id := range []string{"allowed-video", "private-video"} {
		m := media.Model{ID: id, Name: id, Kind: "video", Adapter: "ark", ProviderID: "fixture", UpstreamModel: "fixture", Resolutions: []string{"720p"}, MinDuration: 4, MaxDuration: 15, PriceMicro: 100000, PriceVersion: "fixture"}
		models = append(models, m)
		raw, _ := json.Marshal(m)
		sum := sha256.Sum256(raw)
		reviews = append(reviews, media.TemplateReview{TemplateReviewInput: media.TemplateReviewInput{TemplateID: templates[0].ID, TemplateVersion: templates[0].Version, Score: 90, Approved: true}, ModelID: id, ModelSignature: hex.EncodeToString(sum[:])})
	}
	raw, _ := json.Marshal(models)
	if err = f.store.SetConfigValue(ctx, media.ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(reviews)
	if err = f.store.SetConfigValue(ctx, media.TemplateReviewsKey, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.SaveTeamPolicy(ctx, store.TeamPolicyRecord{TeamID: team, AllowedModelIDs: []string{"allowed-video"}}); err != nil {
		t.Fatal(err)
	}
	response := sourceRequest(f, token, "GET", "/api/v1/media/overview", nil)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	data := dataOf(t, response)
	template := data["videoTemplates"].([]any)[0].(map[string]any)
	verified := template["verifiedModelIds"].([]any)
	if len(verified) != 1 || verified[0] != "allowed-video" {
		t.Fatalf("verification leaked model permission scope: %+v", verified)
	}
}

func TestTemplateReviewRejectsNonRootAndOverviewPublishesOnlyCatalog(t *testing.T) {
	tenants := newTenantFixture(t)
	for _, cookie := range []string{tenants.adminCookie, tenants.memberCookie, tenants.otherCookie, tenants.unassignedCookie} {
		for _, method := range []string{"GET", "POST"} {
			if response := tenants.do(method, "/console/api/media/templates/reviews", cookie, `{}`); response.Code != 403 {
				t.Fatalf("non-root review access: %d", response.Code)
			}
		}
	}
	f, token, _ := newUserConfigPolicyFixture(t)
	response := sourceRequest(f, token, "GET", "/api/v1/media/overview", nil)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	data := dataOf(t, response)
	caps := data["capabilities"].(map[string]any)
	templates := data["videoTemplates"].([]any)
	if caps["adaptiveResearch"] != true || len(templates) < 32 {
		t.Fatal("adaptive catalog missing")
	}
	for _, item := range templates {
		template := item.(map[string]any)
		if _, ok := template["projectTeamId"]; ok {
			t.Fatal("cross-team review leaked")
		}
		if len(template["verifiedModelIds"].([]any)) != 0 {
			t.Fatal("unreviewed seeds claim verification")
		}
	}
}
