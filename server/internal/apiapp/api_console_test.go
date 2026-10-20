package apiapp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestProviderConnectivityTest(t *testing.T) {
	f := newAPIFixture(t)
	adminCookie := f.loginAdmin()

	createRec := f.do("POST", "/console/api/providers", adminCookie,
		`{"name":"DeepSeek","baseUrl":"`+f.upstream.URL+`","apiKey":"sk-test-1234","protocol":"openai-chat-completions"}`)
	if createRec.Code != 200 {
		t.Fatalf("创建供应商: %d %s", createRec.Code, createRec.Body.String())
	}
	created := dataOf(t, createRec)["provider"].(map[string]any)
	providerID := created["id"].(string)
	if created["hasKey"] != true || created["apiKeyHint"] != "1234" ||
		strings.Contains(createRec.Body.String(), "sk-test-1234") {
		t.Fatalf("密钥掩码/防泄漏不符: %s", createRec.Body.String())
	}

	// 1. 带模型 = 协议最小推理(POST + Bearer, max_tokens=1)。
	f.upstreamFn = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pong"}}]}`))
	}
	testRec := f.do("POST", "/console/api/providers/"+providerID+"/test", adminCookie, `{"model":"deepseek-chat"}`)
	if testRec.Code != 200 {
		t.Fatalf("供应商测试: %d %s", testRec.Code, testRec.Body.String())
	}
	result := dataOf(t, testRec)
	if result["ok"] != true || result["kind"] != "ok" {
		t.Fatalf("测试结果不符: %v", result)
	}
	hit := f.lastUpstreamHit()
	if hit.method != "POST" || hit.url != "/v1/chat/completions" {
		t.Fatalf("探测请求不符: %s %s", hit.method, hit.url)
	}
	if hit.headers.Get("authorization") != "Bearer sk-test-1234" {
		t.Fatalf("鉴权头不符: %q", hit.headers.Get("authorization"))
	}
	var sent map[string]any
	_ = json.Unmarshal(hit.body, &sent)
	if sent["model"] != "deepseek-chat" || sent["max_tokens"] != float64(1) || sent["stream"] != false {
		t.Fatalf("探测体不符: %v", sent)
	}

	// 2. 401 → auth,结果落库,明文密钥不外泄。
	f.upstreamFn = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}
	authRec := f.do("POST", "/console/api/providers/"+providerID+"/test", adminCookie, `{}`)
	authResult := dataOf(t, authRec)
	if authResult["ok"] != false || authResult["kind"] != "auth" || authResult["status"] != float64(401) {
		t.Fatalf("失败归类不符: %v", authResult)
	}
	if strings.Contains(authRec.Body.String(), "sk-test-1234") {
		t.Fatal("响应不得包含明文密钥")
	}
	stored, err := f.store.GetUpstreamProvider(context.Background(), providerID)
	if err != nil || stored == nil || stored.LastTestOk == nil || *stored.LastTestOk ||
		stored.LastTestKind != "auth" || stored.LastTestStatus == nil || *stored.LastTestStatus != 401 {
		t.Fatalf("测试结果未落库: %+v", stored)
	}

	// 3. 不带模型 = GET /v1/models。
	f.upstreamFn = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}
	f.do("POST", "/console/api/providers/"+providerID+"/test", adminCookie, `{}`)
	hit = f.lastUpstreamHit()
	if hit.method != "GET" || hit.url != "/v1/models" {
		t.Fatalf("连通探测不符: %s %s", hit.method, hit.url)
	}

	// 4. anthropic 协议:POST /v1/messages + x-api-key + anthropic-version。
	anthropicCreate := f.do("POST", "/console/api/providers", adminCookie,
		`{"name":"AnthropicHost","baseUrl":"`+f.upstream.URL+`","protocol":"anthropic-messages","anthropicVersion":"2023-06-01","apiKey":"sk-ant-test"}`)
	anthropicID := dataOf(t, anthropicCreate)["provider"].(map[string]any)["id"].(string)
	f.do("POST", "/console/api/providers/"+anthropicID+"/test", adminCookie, `{"model":"claude-sonnet-4-5"}`)
	hit = f.lastUpstreamHit()
	if hit.url != "/v1/messages" || hit.headers.Get("x-api-key") != "sk-ant-test" ||
		hit.headers.Get("anthropic-version") != "2023-06-01" {
		t.Fatalf("anthropic 探测头/路径不符: %s %q", hit.url, hit.headers)
	}

	// 5. 不存在 404;未登录 401。
	if rec := f.do("POST", "/console/api/providers/ffffffff-ffff-ffff-ffff-ffffffffffff/test", adminCookie, `{}`); rec.Code != 404 {
		t.Fatal("不存在供应商应 404")
	}
	if rec := f.do("POST", "/console/api/providers/"+providerID+"/test", "", `{}`); rec.Code != 401 {
		t.Fatal("未登录应 401")
	}
}

func TestProviderModelsAndBinding(t *testing.T) {
	f := newAPIFixture(t)
	adminCookie := f.loginAdmin()

	f.upstreamFn = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-v4-pro"},{"id":"deepseek-flash"}]}`))
	}
	createRec := f.do("POST", "/console/api/providers", adminCookie,
		`{"name":"DeepSeek","baseUrl":"`+f.upstream.URL+`/v1/","apiKey":"sk-test-1234","timeoutMs":30000}`)
	if createRec.Code != 200 {
		t.Fatalf("创建供应商: %d", createRec.Code)
	}
	provider := dataOf(t, createRec)["provider"].(map[string]any)
	if provider["baseUrl"] != f.upstream.URL {
		t.Fatalf("base_url 应剥掉尾部 /v1/: %v", provider["baseUrl"])
	}
	if provider["protocol"] != "openai-chat-completions" || provider["timeoutMs"] != float64(30000) {
		t.Fatalf("默认协议/超时不符: %v", provider)
	}

	listRec := f.do("GET", "/console/api/providers/"+provider["id"].(string)+"/models", adminCookie, "")
	if listRec.Code != 200 {
		t.Fatalf("模型列表: %d %s", listRec.Code, listRec.Body.String())
	}
	models := dataOf(t, listRec)["models"].([]any)
	if len(models) != 2 || models[0] != "deepseek-flash" || models[1] != "deepseek-v4-pro" {
		t.Fatalf("模型列表应升序: %v", models)
	}
	hit := f.lastUpstreamHit()
	if hit.url != "/v1/models" || hit.headers.Get("authorization") != "Bearer sk-test-1234" {
		t.Fatalf("代理请求不符: %s %q", hit.url, hit.headers.Get("authorization"))
	}

	// 绑定模型(显式 upstreamProviderId)。
	putRec := f.do("PUT", "/console/api/models/demo-model", adminCookie,
		`{"displayName":"Demo","inputPricePerMtok":"1","outputPricePerMtok":"2","upstreamProviderId":"`+provider["id"].(string)+`"}`)
	if putRec.Code != 200 {
		t.Fatalf("绑模型: %d %s", putRec.Code, putRec.Body.String())
	}
	model := dataOf(t, putRec)["model"].(map[string]any)
	if model["upstreamProviderId"] != provider["id"] {
		t.Fatalf("绑定失败: %v", model)
	}
	if rec := f.do("GET", "/console/api/providers", "", ""); rec.Code != 401 {
		t.Fatal("未登录读供应商应 401")
	}
}

func TestModelImport(t *testing.T) {
	f := newAPIFixture(t)
	adminCookie := f.loginAdmin()
	ctx := context.Background()
	provider, err := f.store.CreateUpstreamProvider(ctx, storeProviderInput("导入源"))
	if err != nil {
		t.Fatal(err)
	}
	res := f.do("POST", "/console/api/models/import", adminCookie,
		`{"providerId":"`+provider.ID+`","modelIds":["claude-sonnet-4-5","fresh-model-a","fresh-model-a","fresh-model-b"]}`)
	if res.Code != 200 {
		t.Fatalf("导入: %d %s", res.Code, res.Body.String())
	}
	data := dataOf(t, res)
	created := data["created"].([]any)
	if len(created) != 2 {
		t.Fatalf("created 不符: %v", created)
	}
	skipped := data["skipped"].([]any)
	if len(skipped) != 1 || skipped[0] != "claude-sonnet-4-5" {
		t.Fatalf("skipped 不符: %v", skipped)
	}
	fresh, _ := f.store.GetModel(ctx, "fresh-model-a")
	if fresh == nil || fresh.UpstreamModel != "fresh-model-a" || fresh.UpstreamProviderID != provider.ID ||
		fresh.Enabled || fresh.InputPricePerMtok != "0.000000" {
		t.Fatalf("导出的模型字段不符: %+v", fresh)
	}
	untouched, _ := f.store.GetModel(ctx, "claude-sonnet-4-5")
	if untouched == nil || untouched.UpstreamProviderID != "" {
		t.Fatalf("已存在模型不应被覆盖: %+v", untouched)
	}
	if rec := f.do("POST", "/console/api/models/import", adminCookie,
		`{"providerId":"ffffffff-ffff-ffff-ffff-ffffffffffff","modelIds":["x"]}`); rec.Code != 404 {
		t.Fatal("供应商不存在应 404")
	}
}

func TestPricingLibrary(t *testing.T) {
	f := newAPIFixture(t)
	adminCookie := f.loginAdmin()

	listRec := f.do("GET", "/console/api/model-pricing", adminCookie, "")
	if listRec.Code != 200 {
		t.Fatalf("定价库: %d", listRec.Code)
	}
	listData := dataOf(t, listRec)
	entries := listData["entries"].([]any)
	version := listData["version"].(string)
	if len(entries) < 50 || !strings.HasPrefix(version, "prc_") {
		t.Fatalf("默认定价库不符: entries=%d version=%s", len(entries), version)
	}
	var deepseekInput any
	for _, raw := range entries {
		entry := raw.(map[string]any)
		if entry["modelId"] == "deepseek-flash" {
			deepseekInput = entry["inputCostPerMillion"]
		}
	}
	if deepseekInput != "0.150000" {
		t.Fatalf("deepseek-flash 入口价不符: %v", deepseekInput)
	}

	// PUT 归一 + 乐观锁。
	putRec := f.do("PUT", "/console/api/model-pricing", adminCookie,
		`{"expectedVersion":"`+version+`","entries":[{"modelId":"auto-price-model","displayName":"Auto Model","inputCostPerMillion":"1.5","outputCostPerMillion":3}]}`)
	if putRec.Code != 200 {
		t.Fatalf("保存定价: %d %s", putRec.Code, putRec.Body.String())
	}
	putData := dataOf(t, putRec)
	if putData["version"] == version {
		t.Fatal("保存后版本应变化")
	}
	savedEntries := putData["entries"].([]any)
	if savedEntries[0].(map[string]any)["cacheReadCostPerMillion"] != "0.000000" {
		t.Fatalf("缺省缓存字段应补 0: %v", savedEntries[0])
	}
	stale := f.do("PUT", "/console/api/model-pricing", adminCookie,
		`{"expectedVersion":"`+version+`","entries":[{"modelId":"auto-price-model","displayName":"Stale","inputCostPerMillion":"9","outputCostPerMillion":"9"}]}`)
	if stale.Code != 409 {
		t.Fatalf("陈旧版本应 409: %d", stale.Code)
	}
	if preserved, _ := f.store.GetModel(context.Background(), "auto-price-model"); preserved != nil {
		t.Fatal("409 不应写入任何东西")
	}

	// 导入命中定价库自动带价。
	provider, err := f.store.CreateUpstreamProvider(context.Background(), storeProviderInput("带价导入源"))
	if err != nil {
		t.Fatal(err)
	}
	importRec := f.do("POST", "/console/api/models/import", adminCookie,
		`{"providerId":"`+provider.ID+`","modelIds":["auto-price-model"]}`)
	if importRec.Code != 200 {
		t.Fatalf("带价导入: %d", importRec.Code)
	}
	model, _ := f.store.GetModel(context.Background(), "auto-price-model")
	if model == nil || model.DisplayName != "Auto Model" || model.InputPricePerMtok != "1.500000" ||
		model.OutputPricePerMtok != "3.000000" || model.Enabled {
		t.Fatalf("带价导入字段不符: %+v", model)
	}
	if rec := f.do("GET", "/console/api/model-pricing", "", ""); rec.Code != 401 {
		t.Fatal("未登录应 401")
	}
}

func TestPublicConfigsAndReleases(t *testing.T) {
	f := newAPIFixture(t)

	// http 环境不下发 builtin_provider_config_json。
	cfgRec := f.do("GET", "/api/v1/client/configs?app_version=1.0.0&platform=darwin", "", "")
	if cfgRec.Code != 200 {
		t.Fatalf("client configs: %d", cfgRec.Code)
	}
	configs := dataOf(t, cfgRec)["configs"].(map[string]any)
	if _, exists := configs["builtin_provider_config_json"]; exists {
		t.Fatal("http 环境不应下发内置配置地址")
	}
	if rec := f.do("GET", "/n/configs", "", ""); rec.Code != 200 {
		t.Fatal("/n/configs 别名应可用")
	}

	// 发布 Release → revision 从 1 起,内容原样。
	adminCookie := f.loginAdmin()
	releaseContent := `{"schemaVersion":1,"revision":100,"config":{"providerConfigRules":{"templateRules":[],"providerRules":[]},"modelConfigRules":{}}}`
	publishRec := f.do("POST", "/console/api/releases", adminCookie, `{"content":`+releaseContent+`}`)
	if publishRec.Code != 200 {
		t.Fatalf("发布: %d %s", publishRec.Code, publishRec.Body.String())
	}
	if dataOf(t, publishRec)["revision"] != float64(1) {
		t.Fatalf("revision 应为 1: %v", dataOf(t, publishRec))
	}
	latestRec := f.do("GET", "/api/v1/releases/builtin/latest.json", "", "")
	if latestRec.Code != 200 {
		t.Fatalf("latest.json: %d", latestRec.Code)
	}
	var latest map[string]any
	if err := json.Unmarshal(latestRec.Body.Bytes(), &latest); err != nil {
		t.Fatal(err)
	}
	if latest["revision"] != float64(100) {
		t.Fatalf("latest 内容不符: %v", latest)
	}
	// 指定 revision 下载。
	if rec := f.do("GET", "/api/v1/releases/builtin/1.json", "", ""); rec.Code != 200 {
		t.Fatalf("按 revision 下载: %d", rec.Code)
	}
	if rec := f.do("GET", "/api/v1/releases/builtin/99.json", "", ""); rec.Code != 404 {
		t.Fatal("不存在 revision 应 404")
	}
	// electron manifest stub。
	manifest := f.do("GET", "/api/v1/releases/electron/manifest", "", "")
	if manifest.Code != 200 || manifest.Body.String() != "version: 0.0.0\n" {
		t.Fatalf("manifest 不符: %d %q", manifest.Code, manifest.Body.String())
	}
}

func TestProviderModelsMediaPrefixFallback(t *testing.T) {
	f := newAPIFixture(t)
	adminCookie := f.loginAdmin()
	// 完整 API 前缀下没有 /models 时回退到 /v1/models，兼容代理型媒体供应商。
	f.upstreamFn = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"media-model-b"},{"id":"media-model-a"}]}`))
	}
	createRec := f.do("POST", "/console/api/providers", adminCookie,
		`{"name":"Media Ark","baseUrl":"`+f.upstream.URL+`/api/v3","apiKey":"sk-test-5678","protocol":"openai-chat-completions"}`)
	if createRec.Code != 200 {
		t.Fatalf("创建媒体供应商: %d %s", createRec.Code, createRec.Body.String())
	}
	providerID := dataOf(t, createRec)["provider"].(map[string]any)["id"].(string)
	listRec := f.do("GET", "/console/api/providers/"+providerID+"/models?base=prefix", adminCookie, "")
	if listRec.Code != 200 {
		t.Fatalf("前缀模式模型列表: %d %s", listRec.Code, listRec.Body.String())
	}
	models := dataOf(t, listRec)["models"].([]any)
	if len(models) != 2 || models[0] != "media-model-a" || models[1] != "media-model-b" {
		t.Fatalf("模型列表应升序: %v", models)
	}
	f.upstreamMu.Lock()
	hits := append([]upstreamHit(nil), f.upstreamHits...)
	f.upstreamMu.Unlock()
	if len(hits) != 2 || hits[0].url != "/api/v3/models" || hits[1].url != "/api/v3/v1/models" {
		t.Fatalf("前缀回退路径不符: %+v", hits)
	}
}

func storeProviderInput(name string) store.CreateUpstreamProviderInput {
	return store.CreateUpstreamProviderInput{
		Name: name, BaseURL: "https://import.test", Protocol: store.ProtocolOpenAIChatCompletions,
	}
}
