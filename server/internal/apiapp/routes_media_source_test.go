package apiapp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func sourceRequest(f *apiFixture, token, method, path string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	request := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	f.engine.ServeHTTP(response, request)
	return response
}

func TestReferenceAnalysisCreatesSourceOnlyWithoutGenerationPrerequisites(t *testing.T) {
	f, token, team := newUserConfigPolicyFixture(t)
	t.Setenv("PATH", t.TempDir())
	source := media.Asset{ID: uuid.NewString(), Name: "fixture.mp4", Kind: "video", Mime: "video/mp4", DurationSeconds: 1.3, Checksum: strings.Repeat("a", 64), CreatedAt: time.Now()}
	if err := f.deps.Media.Repo.AddAsset(context.Background(), team, source); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	response := sourceRequest(f, token, "POST", "/api/v1/media/reference-analyses", map[string]any{"id": id, "assetId": source.ID})
	if response.Code != 200 {
		t.Fatalf("source analysis should not require generation configuration: %d %s", response.Code, response.Body.String())
	}
	data := dataOf(t, response)
	brief := data["brief"].(map[string]any)
	if data["status"] != "draft" || brief["sourceOnly"] != true || brief["videoModel"] != "" || brief["product"] != source.Name || brief["referenceDurationSeconds"] != 1.3 {
		t.Fatalf("wrong source-only projection: %s", response.Body.String())
	}
	replay := sourceRequest(f, token, "POST", "/api/v1/media/reference-analyses", map[string]any{"id": id, "assetId": source.ID})
	if replay.Code != 200 || dataOf(t, replay)["version"] != data["version"] {
		t.Fatalf("same source request must replay: %s", replay.Body.String())
	}
	missing := sourceRequest(f, token, "POST", "/api/v1/media/reference-analyses", map[string]any{"id": uuid.NewString(), "assetId": "other-team-source"})
	if missing.Code == 200 {
		t.Fatal("missing or cross-team source accepted")
	}
	unauthenticated := sourceRequest(f, "", "POST", "/api/v1/media/reference-analyses", map[string]any{"id": uuid.NewString(), "assetId": source.ID})
	if unauthenticated.Code != 401 {
		t.Fatalf("source analysis auth: %d", unauthenticated.Code)
	}
}

func TestAnalysisModelUsesExistingAllowedDeepSeekCapabilities(t *testing.T) {
	f, token, team := newUserConfigPolicyFixture(t)
	ctx := context.Background()
	ciphertext, err := f.deps.SecretBox.Encrypt("fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{Name: "fixture", BaseURL: f.upstream.URL, Protocol: store.ProtocolOpenAIChatCompletions, APIKeyCiphertext: ciphertext, Enabled: boolPointer(true)})
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	for i, id := range []string{"deepseek-text", "deepseek-vision"} {
		cfg, _ := json.Marshal(map[string]any{"properties": map[string]any{"contextWindow": 128000, "inputFormat": map[string]any{"supportsText": true, "supportsImage": i == 1}, "outputFormat": map[string]any{"supportsText": true}}, "optionSpecs": map[string]any{"reasoningLevel": map[string]any{"values": []string{"low", "high"}, "map": "({ reasoning_effort: reasoningLevel })"}, "maxOutputTokens": map[string]any{"max": 32000}}})
		if _, err = f.store.UpsertModel(ctx, store.UpsertModelInput{ModelID: id, DisplayName: id, UpstreamProviderID: provider.ID, UpstreamModel: id, InputPricePerMtok: "1", OutputPricePerMtok: "1", ProvideUpstreamProviderID: true, ProvideUpstreamModel: true, Enabled: &enabled, SortOrder: int64Pointer(int64(i))}); err != nil {
			t.Fatal(err)
		}
		if _, err = f.store.SetModelConfig(ctx, id, "manual", cfg); err != nil {
			t.Fatal(err)
		}
	}
	response := sourceRequest(f, token, "GET", "/api/v1/media/analysis-model?vision=1", nil)
	if response.Code != 200 {
		t.Fatalf("vision DeepSeek: %d %s", response.Code, response.Body.String())
	}
	envelope := dataOf(t, response)["envelope"].(map[string]any)
	personal := envelope["personalConfig"].(map[string]any)
	if personal["defaultModelSelection"].(map[string]any)["modelId"] != "deepseek-vision" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("wrong vision model: %s", response.Body.String())
	}
	if personal["defaultModelSelection"].(map[string]any)["options"].(map[string]any)["reasoningLevel"] != "high" {
		t.Fatalf("source analysis must explicitly select high: %s", response.Body.String())
	}
	response = sourceRequest(f, token, "GET", "/api/v1/media/analysis-model?vision=0", nil)
	if response.Code != 200 || dataOf(t, response)["envelope"].(map[string]any)["personalConfig"].(map[string]any)["defaultModelSelection"].(map[string]any)["modelId"] != "deepseek-text" {
		t.Fatalf("text DeepSeek: %d %s", response.Code, response.Body.String())
	}
	if _, err = f.store.SaveTeamPolicy(ctx, store.TeamPolicyRecord{TeamID: team, AllowedModelIDs: []string{"model-a"}}); err != nil {
		t.Fatal(err)
	}
	response = sourceRequest(f, token, "GET", "/api/v1/media/analysis-model?vision=1", nil)
	if response.Code != 503 || strings.Contains(response.Body.String(), `"envelope"`) {
		t.Fatalf("should not fall back to other provider: %d %s", response.Code, response.Body.String())
	}
}

func boolPointer(value bool) *bool    { return &value }
func int64Pointer(value int64) *int64 { return &value }

func installAnalysisModel(t *testing.T, f *apiFixture, id string, vision bool) {
	t.Helper()
	ctx := context.Background()
	ciphertext, err := f.deps.SecretBox.Encrypt("fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{Name: "fixture-" + id, BaseURL: f.upstream.URL, Protocol: store.ProtocolOpenAIChatCompletions, APIKeyCiphertext: ciphertext, Enabled: boolPointer(true)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.UpsertModel(ctx, store.UpsertModelInput{ModelID: id, DisplayName: id, UpstreamProviderID: provider.ID, UpstreamModel: "deepseek-fixture", ProvideUpstreamProviderID: true, ProvideUpstreamModel: true, InputPricePerMtok: "1", OutputPricePerMtok: "1", Enabled: boolPointer(true)}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"properties": map[string]any{"contextWindow": 128000, "inputFormat": map[string]any{"supportsText": true, "supportsImage": vision}, "outputFormat": map[string]any{"supportsText": true}}, "optionSpecs": map[string]any{"reasoningLevel": map[string]any{"values": []string{"low", "high"}, "map": "({ reasoning_effort: reasoningLevel })"}, "maxOutputTokens": map[string]any{"max": 32000}}})
	if _, err = f.store.SetModelConfig(ctx, id, "manual", cfg); err != nil {
		t.Fatal(err)
	}
}

func TestSourceFirstAPIPreservesReportAndChargesAdaptationOnce(t *testing.T) {
	f, token, team := newUserConfigPolicyFixture(t)
	installAnalysisModel(t, f, "model-a", true)
	ctx := context.Background()
	t.Setenv("PATH", t.TempDir())
	source := media.Asset{ID: uuid.NewString(), Name: "fixture.mp4", Kind: "video", Mime: "video/mp4", DurationSeconds: 1.3, Checksum: strings.Repeat("a", 64), CreatedAt: time.Now()}
	if err := f.deps.Media.Repo.AddAsset(ctx, team, source); err != nil {
		t.Fatal(err)
	}
	models := []media.Model{{ID: "v", Name: "video", Kind: "video", Resolutions: []string{"720p"}, Durations: []int{4}, PriceMicro: 100000, PriceVersion: "fixture"}, {ID: "im", Name: "image", Kind: "image", PriceMicro: 10000, PriceVersion: "fixture"}}
	raw, _ := json.Marshal(models)
	if err := f.store.SetConfigValue(ctx, media.ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	prefix := "/api/v1/media/projects/" + id
	response := sourceRequest(f, token, "POST", "/api/v1/media/reference-analyses", map[string]any{"id": id, "assetId": source.ID})
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	version := dataOf(t, response)["version"]
	response = sourceRequest(f, token, "POST", prefix+"/analysis", map[string]any{"version": version, "analysis": map[string]any{"kind": "source", "taskId": "task", "inputId": "source-input", "workspacePath": "/fixture"}})
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	version = dataOf(t, response)["version"]
	scoped := requestAnalysisConfig(f, token, id, "model-a")
	if scoped.Code != 200 {
		t.Fatal(scoped.Code, scoped.Body.String())
	}
	report := map[string]any{"sourceChecksum": source.Checksum, "summary": "真实原片报告", "references": []map[string]any{{"url": "", "title": "原片", "capturedAt": time.Now().UTC().Format(time.RFC3339Nano), "likes": nil, "evidence": "Read真实原片", "selectionReason": "用户导入"}}, "shots": []map[string]any{{"sourceUrl": "", "sourceStart": 0, "sourceEnd": 1.3, "framing": "全景", "camera": "推进", "action": "舞蹈", "purpose": "开场"}}}
	response = sourceRequest(f, token, "POST", prefix+"/source-analysis", map[string]any{"version": version, "inputId": "source-input", "sourceAnalysis": report})
	if response.Code != 200 || dataOf(t, response)["status"] != "source_ready" || dataOf(t, response)["sourceAnalysis"] == nil {
		t.Fatalf("source completion: %d %s", response.Code, response.Body.String())
	}
	if requestAnalysisConfig(f, token, id, "model-a").Code != 403 {
		t.Fatal("ended source scope issued")
	}
	version = dataOf(t, response)["version"]
	brief := media.Brief{Mode: "replicate", Product: "new subject", AssetIDs: []string{}, ReferenceURLs: []string{}, Platforms: []string{"douyin", "xiaohongshu"}, VideoModel: "v", ImageModel: "im", Resolution: "720p", AspectRatio: "9:16", Duration: 5, MaxCreditsMicro: 10000000, ReviewPlan: true, ReferenceAssetID: source.ID, ReferenceDurationSeconds: source.DurationSeconds}
	response = sourceRequest(f, token, "POST", prefix+"/configure", map[string]any{"version": version, "brief": brief})
	if response.Code != 200 || dataOf(t, response)["status"] != "draft" || dataOf(t, response)["sourceAnalysis"] == nil {
		t.Fatalf("configure should not need composition executable: %d %s", response.Code, response.Body.String())
	}
	configuredVersion := dataOf(t, response)["version"]
	response = sourceRequest(f, token, "POST", prefix+"/analysis", map[string]any{"version": configuredVersion, "analysis": map[string]any{"kind": "adaptation", "taskId": "adapt", "inputId": "adapt-input", "workspacePath": "/fixture"}})
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	version = dataOf(t, response)["version"]
	scoped = requestAnalysisConfig(f, token, id, "model-a")
	if scoped.Code != 200 {
		t.Fatalf("settled fee must support current adaptation: %d %s", scoped.Code, scoped.Body.String())
	}
	response = sourceRequest(f, token, "POST", prefix+"/configure", map[string]any{"version": configuredVersion, "brief": brief})
	if response.Code != 200 || dataOf(t, response)["analysis"].(map[string]any)["inputId"] != "adapt-input" {
		t.Fatal("configuration replay reset active input", response.Body.String())
	}
	shot := map[string]any{"sourceUrl": "", "sourceStart": 0, "sourceEnd": 1.3, "duration": 4, "framing": "全景", "camera": "推进", "action": "替换主体舞蹈", "purpose": "开场", "prompt": "改编视频", "imagePrompt": "新人物首帧", "assetId": "", "subtitle": ""}
	response = sourceRequest(f, token, "POST", prefix+"/plan", map[string]any{"version": version, "inputId": "adapt-input", "plan": map[string]any{"references": report["references"], "shots": []any{shot}}})
	if response.Code != 200 || dataOf(t, response)["status"] != "review" || dataOf(t, response)["analysisChargedMicro"] != float64(1000000) {
		t.Fatal("adaptation result", response.Code, response.Body.String())
	}
	balance, err := f.store.GetTeam(ctx, team)
	if err != nil || balance.UsedCredits != "1.000000" {
		t.Fatal("source/adaptation charged twice", balance, err)
	}
	if scoped = requestAnalysisConfig(f, token, id, "model-a"); scoped.Code != 403 {
		t.Fatal("review scope issued", scoped.Body.String())
	}
}

func TestAnalysisConfigRejectsNonDeepSeekAndMissingVision(t *testing.T) {
	for _, kind := range []string{"non-deepseek", "no-vision", "no-high", "mixed-routes", "disabled-provider"} {
		t.Run(kind, func(t *testing.T) {
			f, token, p := analysisConfigFixture(t)
			ctx := context.Background()
			model, err := f.store.GetModel(ctx, "model-a")
			if err != nil || model == nil {
				t.Fatal(err)
			}
			switch kind {
			case "non-deepseek":
				_, err = f.store.UpsertModel(ctx, store.UpsertModelInput{ModelID: model.ModelID, DisplayName: "fixture", ProvideUpstreamModel: true, UpstreamModel: "other-model", InputPricePerMtok: "1", OutputPricePerMtok: "1"})
			case "no-vision":
				cfg := map[string]any{"properties": map[string]any{"contextWindow": 128000, "inputFormat": map[string]any{"supportsText": true, "supportsImage": false}, "outputFormat": map[string]any{"supportsText": true}}, "optionSpecs": map[string]any{"maxOutputTokens": map[string]any{"max": 32000}}}
				raw, _ := json.Marshal(cfg)
				_, err = f.store.SetModelConfig(ctx, model.ModelID, "manual", raw)
			case "no-high":
				cfg := map[string]any{"properties": map[string]any{"contextWindow": 128000, "inputFormat": map[string]any{"supportsText": true, "supportsImage": true}, "outputFormat": map[string]any{"supportsText": true}}, "optionSpecs": map[string]any{"reasoningLevel": map[string]any{"values": []string{"low", "medium"}, "map": "({ reasoning_effort: reasoningLevel })"}, "maxOutputTokens": map[string]any{"max": 32000}}}
				raw, _ := json.Marshal(cfg)
				_, err = f.store.SetModelConfig(ctx, model.ModelID, "manual", raw)
			case "mixed-routes":
				_, err = f.store.CreateUpstreamRoute(ctx, store.CreateUpstreamRouteInput{ModelID: model.ModelID, ProviderID: model.UpstreamProviderID, UpstreamModel: "deepseek-fixture", Enabled: true, Weight: 1})
				if err == nil {
					var ciphertext string
					ciphertext, err = f.deps.SecretBox.Encrypt("fixture-key")
					if err == nil {
						var provider *store.UpstreamProviderRecord
						provider, err = f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{Name: "fixture-other", BaseURL: f.upstream.URL, Protocol: store.ProtocolOpenAIChatCompletions, APIKeyCiphertext: ciphertext, Enabled: boolPointer(true)})
						if err == nil {
							_, err = f.store.CreateUpstreamRoute(ctx, store.CreateUpstreamRouteInput{ModelID: model.ModelID, ProviderID: provider.ID, UpstreamModel: "other-model", Enabled: true, Weight: 1})
						}
					}
				}
			case "disabled-provider":
				disabled := false
				_, err = f.store.UpdateUpstreamProvider(ctx, model.UpstreamProviderID, store.UpdateUpstreamProviderPatch{Enabled: &disabled})
			}
			if err != nil {
				t.Fatal(err)
			}
			response := requestAnalysisConfig(f, token, p.ID, "model-a")
			if response.Code != 403 || strings.Contains(response.Body.String(), `"envelope"`) {
				t.Fatalf("invalid analysis model issued: %d %s", response.Code, response.Body.String())
			}
		})
	}
}
