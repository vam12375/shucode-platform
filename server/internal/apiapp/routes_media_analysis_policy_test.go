package apiapp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestTextAnalysisPreflightRejectsMissingHighAndNonFlashUpstream(t *testing.T) {
	for _, kind := range []string{"no-high", "pro"} {
		t.Run(kind, func(t *testing.T) {
			f, token, _ := analysisConfigFixture(t)
			ctx := context.Background()
			var err error
			if kind == "no-high" {
				cfg, _ := json.Marshal(map[string]any{"properties": map[string]any{"contextWindow": 128000, "inputFormat": map[string]any{"supportsText": true}, "outputFormat": map[string]any{"supportsText": true}}, "optionSpecs": map[string]any{"reasoningLevel": map[string]any{"values": []string{"low", "max"}, "map": "({ reasoning_effort: reasoningLevel })"}, "maxOutputTokens": map[string]any{"max": 32000}}})
				_, err = f.store.SetModelConfig(ctx, "model-a", "manual", cfg)
			} else {
				_, err = f.store.UpsertModel(ctx, store.UpsertModelInput{ModelID: "model-a", DisplayName: "DeepSeek Flash", ProvideUpstreamModel: true, UpstreamModel: "deepseek-v4-pro", InputPricePerMtok: "1", OutputPricePerMtok: "1"})
			}
			if err != nil {
				t.Fatal(err)
			}
			response := sourceRequest(f, token, "GET", "/api/v1/media/analysis-model?vision=0", nil)
			if response.Code != 503 || strings.Contains(response.Body.String(), `"envelope"`) {
				t.Fatalf("adaptation preflight bypassed fixed model/high: %d %s", response.Code, response.Body.String())
			}
		})
	}
}
