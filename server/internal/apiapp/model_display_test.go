package apiapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func installDisplayModels(t *testing.T, f *apiFixture) []media.Model {
	t.Helper()
	ctx := context.Background()
	if _, err := f.store.UpsertModel(ctx, store.UpsertModelInput{ModelID: "deepseek-flash", DisplayName: "DeepSeek Flash", UpstreamModel: "private-flash-route", ProvideUpstreamModel: true, InputPricePerMtok: "1.250000", OutputPricePerMtok: "2.500000"}); err != nil {
		t.Fatal(err)
	}
	models := make([]media.Model, 0, 3)
	for _, resolution := range []string{"1K", "2K", "4K"} {
		models = append(models, media.Model{ID: "toapis-gpt-image-2-" + strings.ToLower(resolution), Name: "GPT Image 2 " + resolution, Kind: "image", ProviderID: "internal-provider", Adapter: "toapis", UpstreamModel: "gpt-image-2", Resolutions: []string{resolution}, PriceMicro: 100000, PriceVersion: "v1"})
	}
	raw, _ := json.Marshal(models)
	if err := f.store.SetConfigValue(ctx, media.ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	return models
}

func displayEntries(t *testing.T, f *tenantFixture) []any {
	t.Helper()
	rec := f.do("GET", "/console/api/model-display", f.rootCookie, "")
	if rec.Code != 200 {
		t.Fatalf("display entries: %d %s", rec.Code, rec.Body.String())
	}
	return dataOf(t, rec)["entries"].([]any)
}

func TestModelDisplayRootEditingUsesOriginalOwnersAndCAS(t *testing.T) {
	f := newTenantFixture(t)
	models := installDisplayModels(t, f.apiFixture)
	for _, cookie := range []string{f.adminCookie, f.memberCookie, f.otherCookie} {
		for _, method := range []string{"GET", "PUT"} {
			if rec := f.do(method, "/console/api/model-display", cookie, `{}`); rec.Code != 403 {
				t.Fatalf("non-root %s: %d", method, rec.Code)
			}
		}
	}
	entries := displayEntries(t, f)
	if len(entries) != 2 {
		t.Fatalf("one text and one logical image: %+v", entries)
	}
	textEntry, imageEntry := entries[0].(map[string]any), entries[1].(map[string]any)
	if textEntry["id"] != "text:deepseek-flash" || imageEntry["displayName"] != "GPT Image 2" || len(imageEntry["modelIds"].([]any)) != 3 {
		t.Fatalf("entries %+v", entries)
	}
	body := fmt.Sprintf(`{"id":%q,"displayName":" 门道对话 ","expectedVersion":%q}`, textEntry["id"], textEntry["expectedVersion"])
	rec := f.do("PUT", "/console/api/model-display", f.rootCookie, body)
	if rec.Code != 200 || dataOf(t, rec)["entry"].(map[string]any)["displayName"] != "门道对话" {
		t.Fatalf("text save: %d %s", rec.Code, rec.Body.String())
	}
	after, _ := f.store.GetModel(context.Background(), "deepseek-flash")
	if after.DisplayName != "门道对话" || after.UpstreamModel != "private-flash-route" || after.InputPricePerMtok != "1.250000" || after.OutputPricePerMtok != "2.500000" {
		t.Fatalf("unrelated text fields changed: %+v", after)
	}
	if stale := f.do("PUT", "/console/api/model-display", f.rootCookie, body); stale.Code != 409 {
		t.Fatalf("stale text overwrite: %d", stale.Code)
	}
	for _, bad := range []string{`{"id":"text:deepseek-flash","displayName":" ","expectedVersion":"x"}`, `{"id":"text:deepseek-flash","displayName":"x","expectedVersion":"x","priceMicro":0}`} {
		if rec := f.do("PUT", "/console/api/model-display", f.rootCookie, bad); rec.Code != 400 {
			t.Fatalf("invalid write: %d %s", rec.Code, rec.Body.String())
		}
	}
	body = fmt.Sprintf(`{"id":%q,"displayName":"门道绘图","expectedVersion":%q}`, imageEntry["id"], imageEntry["expectedVersion"])
	rec = f.do("PUT", "/console/api/model-display", f.rootCookie, body)
	if rec.Code != 200 {
		t.Fatalf("media save: %d %s", rec.Code, rec.Body.String())
	}
	changed, _ := f.deps.Media.Repo.Models(context.Background())
	for i, m := range publicMediaModels(changed) {
		if m["groupName"] != "门道绘图" || m["name"] != "门道绘图" || changed[i].ID != models[i].ID || changed[i].PriceMicro != models[i].PriceMicro {
			t.Fatalf("public media %+v original %+v", m, changed[i])
		}
	}
	// 目录中的价格变化也必须使展示页的旧版本失效，不能覆盖其他编辑者。
	entry := dataOf(t, rec)["entry"].(map[string]any)
	changed[0].PriceMicro++
	raw, _ := json.Marshal(changed)
	f.store.SetConfigValue(context.Background(), media.ConfigKey, raw)
	body = fmt.Sprintf(`{"id":%q,"displayName":"不能覆盖","expectedVersion":%q}`, entry["id"], entry["expectedVersion"])
	if rec := f.do("PUT", "/console/api/model-display", f.rootCookie, body); rec.Code != 409 {
		t.Fatalf("catalog stale write %d", rec.Code)
	}
}

func TestModelDisplayUsageProjectionHidesInternalModelsAndGroupsTiers(t *testing.T) {
	f := newTenantFixture(t)
	models := installDisplayModels(t, f.apiFixture)
	ctx := context.Background()
	for _, user := range []struct{ team, user string }{{f.team.ID, f.member.ID}, {f.otherTeam.ID, f.other.ID}} {
		for _, model := range []string{"deepseek-flash", "media:" + models[0].ID, "media:" + models[1].ID, "media:deleted-private-supplier"} {
			if err := f.store.InsertUsage(ctx, store.UsageRecord{ID: uuid.NewString(), RequestID: uuid.NewString(), TeamID: user.team, UserID: user.user, Model: model, UpstreamModel: "private-upstream", CreditsMicro: 1000, InputTokens: 10, Status: store.UsageStatusOk, CreatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, cookie := range []string{f.adminCookie, f.memberCookie} {
		rec := f.do("GET", "/console/api/usage", cookie, "")
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "toapis") || strings.Contains(rec.Body.String(), "private-") {
			t.Fatalf("private usage leaked: %d %s", rec.Code, rec.Body.String())
		}
		data := dataOf(t, rec)
		aggregates := data["aggregates"].([]any)
		if len(aggregates) != 3 {
			t.Fatalf("tiers not grouped: %+v", aggregates)
		}
		imageKey := ""
		for _, raw := range aggregates {
			r := raw.(map[string]any)
			if r["modelDisplayName"] == "GPT Image 2" {
				if r["requests"] != float64(2) {
					t.Fatal(r)
				}
				imageKey = r["model"].(string)
			}
		}
		if imageKey == "" {
			t.Fatal("missing image projection")
		}
		filtered := dataOf(t, f.do("GET", "/console/api/usage?model="+imageKey, cookie, ""))
		if len(filtered["records"].([]any)) != 2 {
			t.Fatal("group filter lost tier or crossed team")
		}
		csv := f.do("GET", "/console/api/usage/export", cookie, "")
		if csv.Code != 200 || strings.Contains(csv.Body.String(), "toapis") || strings.Contains(csv.Body.String(), "private-") || !strings.Contains(csv.Body.String(), "GPT Image 2") {
			t.Fatalf("CSV projection: %d %s", csv.Code, csv.Body.String())
		}
		team := f.do("GET", "/console/api/teams/"+f.team.ID, cookie, "")
		if team.Code != 200 || strings.Contains(team.Body.String(), "toapis") || strings.Contains(team.Body.String(), "private-") {
			t.Fatalf("team detail leaked %d %s", team.Code, team.Body.String())
		}
	}
	root := f.do("GET", "/console/api/usage?teamId="+f.team.ID, f.rootCookie, "")
	if root.Code != 200 || !strings.Contains(root.Body.String(), "media:toapis-") || !strings.Contains(root.Body.String(), "modelDisplayName") {
		t.Fatalf("root traceability %d %s", root.Code, root.Body.String())
	}
}

func TestModelDisplayLegacyNamesAndExplicitOverrides(t *testing.T) {
	for id, want := range map[string]string{"deepseek-flash": "DeepSeek Flash", "deepseek-v4-pro": "DeepSeek V4 Pro", "glm-5-3-flash-260828": "GLM 5.3 Flash"} {
		for _, configured := range []string{"", "  ", id} {
			if got := store.ModelDisplayName(id, configured); got != want {
				t.Fatalf("legacy name %q %q: %q", id, configured, got)
			}
			input := BuildUserConfigInput{Models: []userConfigModel{{ModelID: id, DisplayName: configured}}}
			envelope := buildUserConfigEnvelope(input)
			rule := envelope["personalConfig"].(map[string]any)["modelConfigRules"].(map[string]any)["providerModelRules"].([]any)[0].(map[string]any)["config"].(map[string]any)
			if rule["displayName"] != want {
				t.Fatalf("envelope name: %+v", rule)
			}
			input.Models[0].DisplayName = "自定义模型"
			updated := buildUserConfigEnvelope(input)
			if updated["syncId"] == envelope["syncId"] {
				t.Fatal("renaming must invalidate config syncId")
			}
		}
		if got := store.ModelDisplayName(id, "自定义模型"); got != "自定义模型" {
			t.Fatalf("explicit name replaced: %q", got)
		}
	}
}
