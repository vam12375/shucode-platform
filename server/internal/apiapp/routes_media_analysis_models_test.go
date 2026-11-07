package apiapp

import (
	"context"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"testing"
)

func TestVideoAnalysisModelCatalogAndExplicitSelection(t *testing.T) {
	f, token, p := analysisConfigFixture(t)
	_, err := f.store.UpsertModel(context.Background(), store.UpsertModelInput{ModelID: "model-a", DisplayName: "Vision Creator", ProvideUpstreamModel: true, UpstreamModel: "vision-creator", InputPricePerMtok: "1", OutputPricePerMtok: "1"})
	if err != nil {
		t.Fatal(err)
	}
	catalog := sourceRequest(f, token, "GET", "/api/v1/media/analysis-models", nil)
	if catalog.Code != 200 {
		t.Fatalf("catalog: %d %s", catalog.Code, catalog.Body.String())
	}
	data := dataOf(t, catalog)
	if data["defaultModelId"] != "model-a" {
		t.Fatalf("single model not default: %v", data)
	}
	model := data["models"].([]any)[0].(map[string]any)
	if model["available"] != true || model["name"] != "Vision Creator" {
		t.Fatal(model)
	}
	selected := sourceRequest(f, token, "GET", "/api/v1/media/analysis-model?vision=1&modelId=model-a", nil)
	if selected.Code != 200 {
		t.Fatalf("non-Flash selection: %s", selected.Body.String())
	}
	missing := sourceRequest(f, token, "GET", "/api/v1/media/analysis-model?vision=1&modelId=removed", nil)
	if missing.Code != 503 {
		t.Fatalf("explicit selection silently changed: %s", missing.Body.String())
	}
	scoped := requestAnalysisConfig(f, token, p.ID, "model-a")
	if scoped.Code != 200 {
		t.Fatalf("non-Flash scope: %s", scoped.Body.String())
	}
}
