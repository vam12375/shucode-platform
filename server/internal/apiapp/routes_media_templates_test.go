package apiapp

import (
	"context"
	"encoding/json"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"testing"
)

func TestTemplateReviewEndpointsRemoved(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	for _, method := range []string{"GET", "POST"} {
		for _, path := range []string{"/console/api/media/templates", "/console/api/media/templates/reviews"} {
			if response := f.do(method, path, cookie, `{}`); response.Code != 404 {
				t.Fatalf("removed %s %s returned %d", method, path, response.Code)
			}
		}
	}
}

func TestTemplateOverviewDefaultAvailabilityRespectsTeamModelWhitelist(t *testing.T) {
	f, token, team := newUserConfigPolicyFixture(t)
	ctx := context.Background()
	var err error
	models := []media.Model{}
	for _, id := range []string{"allowed-video", "private-video"} {
		m := media.Model{ID: id, Name: id, Kind: "video", Adapter: "ark", ProviderID: "fixture", UpstreamModel: "doubao-seedance-2-0-260128", Resolutions: []string{"720p"}, MinDuration: 4, MaxDuration: 15, PriceMicro: 100000, PriceVersion: "fixture"}
		models = append(models, m)
	}
	raw, _ := json.Marshal(models)
	if err = f.store.SetConfigValue(ctx, media.ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.SaveTeamPolicy(ctx, store.TeamPolicyRecord{TeamID: team, AllowedModelIDs: []string{"allowed-video"}}); err != nil {
		t.Fatal(err)
	}
	response := sourceRequest(f, token, "GET", "/api/v1/media/overview", nil)
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("overview must not cache mutable catalog metadata")
	}
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	data := dataOf(t, response)
	alias := publicMediaAliasForTest(t, f, "allowed-video")
	for key, count := range map[string]int{"videoTemplates": 66, "videoTestTemplates": 31} {
		items := data[key].([]any)
		if len(items) != count {
			t.Fatalf("%s: %d templates, want %d", key, len(items), count)
		}
		for _, item := range items {
			tpl := item.(map[string]any)
			ids := tpl["verifiedModelIds"].([]any)
			if len(ids) != 1 || ids[0] != alias {
				t.Fatalf("%s: model availability exceeded team permissions: %v", tpl["id"], ids)
			}
		}
	}
}
