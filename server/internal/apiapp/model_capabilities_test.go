package apiapp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

const capabilityTestRelease = `{
  "schemaVersion": 1,
  "revision": 77,
  "config": {
    "modelConfigRules": {
      "modelRules": [
        {
          "modelMatch": ".*",
          "config": {
            "properties": {
              "contextWindow": 128000,
              "inputFormat": {
                "supportsText": true,
                "supportsImage": false,
                "supportsVideo": false,
                "supportsAudio": false,
                "supportsPdf": false
              },
              "outputFormat": {"supportsText": true},
              "supportsToolCall": true,
              "supportsJsonSchemaOutput": false,
              "supportsNativeWebSearch": false,
              "supportsMidConversationSystem": false,
              "requiresMfjsToolSchema": false
            },
            "optionSpecs": {"maxOutputTokens": {"max": 32000}}
          }
        },
        {
          "modelMatch": "vision-.*",
          "config": {
            "properties": {
              "contextWindow": 200000,
              "inputFormat": {"supportsImage": true, "supportsPdf": true}
            },
            "optionSpecs": {"maxOutputTokens": {"max": 64000}}
          }
        }
      ],
      "modelApiRules": [],
      "providerSiteRules": []
    }
  }
}`

const manualCapabilityConfig = `{
  "properties": {
    "contextWindow": 64000,
    "inputFormat": {
      "supportsText": true,
      "supportsImage": true,
      "supportsVideo": false,
      "supportsAudio": false,
      "supportsPdf": false
    },
    "supportsToolCall": true,
    "supportsJsonSchemaOutput": true,
    "supportsNativeWebSearch": false,
    "supportsMidConversationSystem": false
  },
  "optionSpecs": {"maxOutputTokens": {"max": 8000}}
}`

func TestModelCapabilityAnalyzeAndConfigUpdate(t *testing.T) {
	f := newAPIFixture(t)
	adminCookie := f.loginAdmin()
	ctx := context.Background()
	admin, err := f.store.FindUserByEmail(ctx, "admin@mendao.chat")
	if err != nil || admin == nil {
		t.Fatal("管理员不存在")
	}
	if _, err := f.store.InsertRelease(ctx, store.InsertReleaseInput{
		Revision: 77, Content: []byte(capabilityTestRelease), PublishedBy: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ActivateRelease(ctx, 77); err != nil {
		t.Fatal(err)
	}

	analyzeRec := f.do("POST", "/console/api/models/analyze", adminCookie, `{"modelId":"vision-flash"}`)
	if analyzeRec.Code != 200 {
		t.Fatalf("能力分析失败: %d %s", analyzeRec.Code, analyzeRec.Body.String())
	}
	analysis := dataOf(t, analyzeRec)
	config := analysis["config"].(map[string]any)
	properties := config["properties"].(map[string]any)
	optionSpecs := config["optionSpecs"].(map[string]any)
	if properties["contextWindow"] != float64(200000) ||
		optionSpecs["maxOutputTokens"].(map[string]any)["max"] != float64(64000) ||
		analysis["matchedRules"] != float64(2) || analysis["complete"] != true {
		t.Fatalf("官方规则合并不符: %v", analysis)
	}
	inputFormat := properties["inputFormat"].(map[string]any)
	if inputFormat["supportsImage"] != true || inputFormat["supportsText"] != true {
		t.Fatalf("输入类型合并不符: %v", inputFormat)
	}

	if _, err := f.store.UpsertModel(ctx, store.UpsertModelInput{
		ModelID: "vision-flash", DisplayName: "Vision", InputPricePerMtok: "1", OutputPricePerMtok: "2",
	}); err != nil {
		t.Fatal(err)
	}
	putRec := f.do("PUT", "/console/api/models/vision-flash/config", adminCookie,
		`{"mode":"manual","config":`+manualCapabilityConfig+`}`)
	if putRec.Code != 200 {
		t.Fatalf("保存模型配置失败: %d %s", putRec.Code, putRec.Body.String())
	}
	updated := dataOf(t, putRec)["model"].(map[string]any)
	if updated["configMode"] != "manual" {
		t.Fatalf("配置模式不符: %v", updated)
	}
	updatedConfig := updated["config"].(map[string]any)["properties"].(map[string]any)
	if updatedConfig["contextWindow"] != float64(64000) || updatedConfig["supportsJsonSchemaOutput"] != true {
		t.Fatalf("配置写入不符: %v", updatedConfig)
	}
	if _, exists := updatedConfig["outputFormat"]; !exists {
		t.Fatalf("应补齐客户端需要的 outputFormat: %v", updatedConfig)
	}

	if invalid := f.do("PUT", "/console/api/models/vision-flash/config", adminCookie,
		`{"mode":"manual","config":{"properties":{"contextWindow":0}}}`); invalid.Code != 400 {
		t.Fatalf("非法配置应 400: %d %s", invalid.Code, invalid.Body.String())
	}
}

func TestUserConfigCarriesManualModelCapabilities(t *testing.T) {
	f, token, _ := newUserConfigPolicyFixture(t)
	ctx := context.Background()
	cfg, err := parseManualModelCapabilityConfig(json.RawMessage(manualCapabilityConfig))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := jsonMarshalNoHTML(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SetModelConfig(ctx, "model-a", "manual", encoded); err != nil {
		t.Fatal(err)
	}

	rec := requestUserConfig(f, token, "")
	if rec.Code != 200 {
		t.Fatalf("user-config: %d %s", rec.Code, rec.Body.String())
	}
	etag := rec.Header().Get("etag")
	personal := dataOf(t, rec)["envelope"].(map[string]any)["personalConfig"].(map[string]any)
	rules := personal["modelConfigRules"].(map[string]any)["providerModelRules"].([]any)
	var modelRule map[string]any
	for _, raw := range rules {
		rule := raw.(map[string]any)
		if rule["modelId"] == "model-a" {
			modelRule = rule
			break
		}
	}
	if modelRule == nil {
		t.Fatalf("信封缺少 model-a: %v", rules)
	}
	ruleConfig := modelRule["config"].(map[string]any)
	properties := ruleConfig["properties"].(map[string]any)
	optionSpecs := ruleConfig["optionSpecs"].(map[string]any)
	if properties["contextWindow"] != float64(64000) ||
		optionSpecs["maxOutputTokens"].(map[string]any)["max"] != float64(8000) {
		t.Fatalf("手动能力未进入信封: %v", ruleConfig)
	}

	if _, err := f.store.SetModelConfig(ctx, "model-a", "recommended", nil); err != nil {
		t.Fatal(err)
	}
	next := requestUserConfig(f, token, etag)
	if next.Code != 200 || next.Header().Get("etag") == etag {
		t.Fatalf("切换到官方规则必须改变 syncId/ETag: %d %s", next.Code, next.Body.String())
	}
	personal = dataOf(t, next)["envelope"].(map[string]any)["personalConfig"].(map[string]any)
	rules = personal["modelConfigRules"].(map[string]any)["providerModelRules"].([]any)
	found := false
	for _, raw := range rules {
		rule := raw.(map[string]any)
		if rule["modelId"] != "model-a" {
			continue
		}
		found = true
		config := rule["config"].(map[string]any)
		if _, exists := config["properties"]; exists {
			t.Fatalf("recommended 模式不应带 override: %v", config)
		}
		if config["enabled"] != true {
			t.Fatalf("recommended 规则仍应启用模型: %v", config)
		}
	}
	if !found {
		t.Fatalf("信封缺少 model-a: %v", rules)
	}
}

func TestModelCapabilityAnalysisWithoutReleaseIsIncomplete(t *testing.T) {
	f := newAPIEmptyFixture(t)
	ctx := context.Background()
	analysis, err := analyzeModelCapabilities(ctx, f.deps, "unknown-model")
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Complete || analysis.MatchedRules != 0 || analysis.ReleaseRevision != nil {
		t.Fatalf("无 Release 时应返回不完整结果: %+v", analysis)
	}
	if !analysis.Config.Properties.InputFormat.SupportsText {
		t.Fatalf("文本输入应始终可用: %+v", analysis.Config)
	}
	raw, _ := json.Marshal(analysis)
	if len(raw) == 0 {
		t.Fatal("分析结果应可序列化")
	}
}
