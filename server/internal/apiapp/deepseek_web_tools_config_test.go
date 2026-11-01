package apiapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"github.com/vam12375/shucode-platform/server/internal/webtools"
)

const webToolsConfigPath = "/console/api/deepseek-web-tools"

func webToolsBody(config webtools.Config, expected string) string {
	body := map[string]any{
		"expectedVersion": expected, "enabled": config.Enabled, "providerId": config.ProviderID,
		"searchEnabled": config.SearchEnabled, "fetchEnabled": config.FetchEnabled,
		"searchSaleMicro": config.SearchSaleMicro, "searchCostMicro": config.SearchCostMicro,
		"fetchSaleMicro": config.FetchSaleMicro, "fetchCostMicro": config.FetchCostMicro,
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

func createWebToolsProvider(t *testing.T, f *apiFixture) *store.UpstreamProviderRecord {
	t.Helper()
	encrypted, err := f.deps.SecretBox.Encrypt("sk-private-web-tool-provider")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := f.store.CreateUpstreamProvider(context.Background(), store.CreateUpstreamProviderInput{
		Name: "Explicit web provider", BaseURL: "https://web-tools.example.test/v1/", APIKeyCiphertext: encrypted,
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestWebToolsConfigAllManagementEndpointsAreRootOnly(t *testing.T) {
	f := newTenantFixture(t)
	for _, endpoint := range []struct{ method, suffix, body string }{
		{"GET", "", ""}, {"PUT", "", "{}"}, {"DELETE", "", "{}"}, {"POST", "/test", "{}"},
	} {
		for _, cookie := range []string{f.adminCookie, f.memberCookie, f.otherCookie, f.unassignedCookie} {
			rec := f.do(endpoint.method, webToolsConfigPath+endpoint.suffix, cookie, endpoint.body)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s%s non-root: %d %s", endpoint.method, endpoint.suffix, rec.Code, rec.Body.String())
			}
		}
		if rec := f.do(endpoint.method, webToolsConfigPath+endpoint.suffix, "", endpoint.body); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s%s unauthenticated: %d", endpoint.method, endpoint.suffix, rec.Code)
		}
	}
}

func TestWebToolsConfigVersionedLifecycleStoresTombstoneWithoutKeys(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	ctx := context.Background()
	provider := createWebToolsProvider(t, f)
	initial := f.do("GET", webToolsConfigPath, cookie, "")
	if initial.Code != 200 {
		t.Fatalf("initial status: %d %s", initial.Code, initial.Body.String())
	}
	initialState := dataOf(t, initial)
	if initialState["version"] != webtools.MissingVersion || initialState["source"] != "none" || initialState["configured"] != false {
		t.Fatalf("missing configuration state: %v", initialState)
	}
	config := webtools.Defaults()
	config.Enabled, config.ProviderID = true, provider.ID
	config.SearchSaleMicro, config.SearchCostMicro = 0, 1234
	config.FetchSaleMicro, config.FetchCostMicro = webtools.MaxPriceMicro, 0
	saved := f.do("PUT", webToolsConfigPath, cookie, webToolsBody(config, webtools.MissingVersion))
	if saved.Code != 200 {
		t.Fatalf("save: %d %s", saved.Code, saved.Body.String())
	}
	state := dataOf(t, saved)
	version := state["version"].(string)
	if version == webtools.MissingVersion || state["configured"] != true || state["source"] != "console" || state["providerId"] != provider.ID || state["hasKey"] != true || state["fetchSaleMicro"] != float64(webtools.MaxPriceMicro) || state["searchSaleMicro"] != float64(0) {
		t.Fatalf("saved state: %v", state)
	}
	for _, rec := range []string{saved.Body.String(), f.do("GET", webToolsConfigPath, cookie, "").Body.String()} {
		for _, secret := range []string{"sk-private-web-tool-provider", provider.APIKeyCiphertext, "apiKey", "Ciphertext"} {
			if strings.Contains(rec, secret) {
				t.Fatalf("configuration response exposes credential material %q", secret)
			}
		}
	}
	raw, err := f.store.GetConfigValue(ctx, webtools.ConfigKey)
	if err != nil || strings.Contains(string(raw), "sk-private-web-tool-provider") || strings.Contains(string(raw), provider.APIKeyCiphertext) {
		t.Fatal("tool configuration must reference a provider without copying its credential")
	}
	if rec := f.do("PUT", webToolsConfigPath, cookie, webToolsBody(config, webtools.MissingVersion)); rec.Code != 409 {
		t.Fatalf("stale PUT: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("DELETE", webToolsConfigPath, cookie, fmt.Sprintf("{\"expectedVersion\":%q}", webtools.MissingVersion)); rec.Code != 409 {
		t.Fatalf("stale DELETE: %d", rec.Code)
	}
	// API 缺少加密材料也必须能执行停用，不能让失效密钥阻止管理员关闭能力。
	f.deps.SecretBox = nil
	cleared := f.do("DELETE", webToolsConfigPath, cookie, fmt.Sprintf("{\"expectedVersion\":%q}", version))
	if cleared.Code != 200 {
		t.Fatalf("clear: %d %s", cleared.Code, cleared.Body.String())
	}
	clearedState := dataOf(t, cleared)
	if clearedState["enabled"] != false || clearedState["configured"] != false || clearedState["source"] != "console" || clearedState["providerId"] != "" || clearedState["version"] == version {
		t.Fatalf("clear did not persist disabled authority: %v", clearedState)
	}
	config, found, err := webtools.Load(ctx, f.store)
	if err != nil || !found || config.Enabled || config.ProviderID != "" {
		t.Fatalf("disabled tombstone missing: %+v %v %v", config, found, err)
	}
	existing, err := f.store.GetUpstreamProvider(ctx, provider.ID)
	if err != nil || existing == nil || existing.APIKeyCiphertext != provider.APIKeyCiphertext {
		t.Fatal("clearing tools must preserve provider and key")
	}
	logs, err := f.store.ListAuditLogs(ctx, store.AuditLogFilter{})
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]int{}
	for _, entry := range logs {
		actions[entry.Action]++
		if strings.Contains(string(entry.Detail), "sk-private-web-tool-provider") || strings.Contains(string(entry.Detail), provider.APIKeyCiphertext) {
			t.Fatal("audit contains credential material")
		}
	}
	if actions["web-tools.update"] != 1 || actions["web-tools.clear"] != 1 {
		t.Fatalf("write audit count: %v", actions)
	}
}

func TestWebToolsConfigRejectsInvalidPricesFieldsAndProvider(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	provider := createWebToolsProvider(t, f)
	config := webtools.Defaults()
	config.Enabled, config.ProviderID = true, provider.ID
	var valid map[string]any
	_ = json.Unmarshal([]byte(webToolsBody(config, webtools.MissingVersion)), &valid)
	for _, tc := range []struct {
		field string
		value any
	}{
		{"searchSaleMicro", -1}, {"searchCostMicro", 1.5}, {"fetchSaleMicro", int64(9_007_199_254_740_992)},
		{"fetchCostMicro", "5000"}, {"enabled", nil}, {"searchEnabled", 1}, {"fetchEnabled", "true"},
		{"providerId", ""}, {"providerId", "not-a-uuid"}, {"providerId", uuid.NewString()}, {"expectedVersion", ""},
		{"apiKey", "must-not-be-accepted"},
	} {
		body := map[string]any{}
		for key, value := range valid {
			body[key] = value
		}
		body[tc.field] = tc.value
		raw, _ := json.Marshal(body)
		if rec := f.do("PUT", webToolsConfigPath, cookie, string(raw)); rec.Code != 400 {
			t.Errorf("accepted invalid %s: %d %s", tc.field, rec.Code, rec.Body.String())
		}
	}
	delete(valid, "fetchCostMicro")
	raw, _ := json.Marshal(valid)
	if rec := f.do("PUT", webToolsConfigPath, cookie, string(raw)); rec.Code != 400 {
		t.Fatalf("missing required field: %d", rec.Code)
	}
	disabled := false
	_, _ = f.store.UpdateUpstreamProvider(context.Background(), provider.ID, store.UpdateUpstreamProviderPatch{Enabled: &disabled})
	if rec := f.do("PUT", webToolsConfigPath, cookie, webToolsBody(config, webtools.MissingVersion)); rec.Code != 400 {
		t.Fatalf("disabled provider: %d", rec.Code)
	}
	enabled := true
	insecure := "http://tools.example.test"
	_, _ = f.store.UpdateUpstreamProvider(context.Background(), provider.ID, store.UpdateUpstreamProviderPatch{Enabled: &enabled, BaseURL: &insecure})
	if rec := f.do("PUT", webToolsConfigPath, cookie, webToolsBody(config, webtools.MissingVersion)); rec.Code != 400 {
		t.Fatalf("insecure tool provider: %d", rec.Code)
	}
	secure := "https://tools.example.test"
	_, _ = f.store.UpdateUpstreamProvider(context.Background(), provider.ID, store.UpdateUpstreamProviderPatch{BaseURL: &secure, ProvideAPIKey: true})
	if rec := f.do("PUT", webToolsConfigPath, cookie, webToolsBody(config, webtools.MissingVersion)); rec.Code != 400 {
		t.Fatalf("missing provider key: %d", rec.Code)
	}
	for _, body := range []string{"{}", "{\"expectedVersion\":null}", "{\"expectedVersion\":\"none\",\"extra\":true}"} {
		if rec := f.do("DELETE", webToolsConfigPath, cookie, body); rec.Code != 400 {
			t.Errorf("invalid clear payload accepted: %d", rec.Code)
		}
	}
}

type webToolsProbeTransport struct {
	status                     int
	calls                      int
	method, url, authorization string
}

func (tr *webToolsProbeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr.calls++
	tr.method, tr.url, tr.authorization = req.Method, req.URL.String(), req.Header.Get("Authorization")
	return &http.Response{StatusCode: tr.status, Header: http.Header{"Location": []string{"https://redirect.example.test"}}, Body: io.NopCloser(strings.NewReader("private-upstream-response")), Request: req}, nil
}

func TestWebToolsConnectivityProbeIsReadOnlyRedactedAndNeverFollowsRedirect(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	provider := createWebToolsProvider(t, f)
	transport := &webToolsProbeTransport{status: 200}
	f.deps.HTTPClient = &http.Client{Transport: transport}
	body := fmt.Sprintf("{\"providerId\":%q}", provider.ID)
	tested := f.do("POST", webToolsConfigPath+"/test", cookie, body)
	if tested.Code != 200 {
		t.Fatalf("probe: %d %s", tested.Code, tested.Body.String())
	}
	result := dataOf(t, tested)
	if result["ok"] != true || result["scope"] != "connectivity_auth_only" || transport.calls != 1 || transport.method != "GET" || transport.url != "https://web-tools.example.test/v1/models" || transport.authorization != "Bearer sk-private-web-tool-provider" {
		t.Fatalf("probe behavior: %v %s %s", result, transport.method, transport.url)
	}
	if strings.Contains(tested.Body.String(), "private-upstream-response") || strings.Contains(tested.Body.String(), "sk-private-web-tool-provider") {
		t.Fatal("probe response exposed upstream body or key")
	}
	transport.status = http.StatusFound
	tested = f.do("POST", webToolsConfigPath+"/test", cookie, body)
	if result := dataOf(t, tested); result["ok"] != false || result["status"] != float64(http.StatusFound) || transport.calls != 2 {
		t.Fatalf("probe followed redirect: %v calls=%d", result, transport.calls)
	}
	usages, err := f.store.ListUsage(context.Background(), store.UsageFilter{})
	if err != nil || len(usages) != 0 {
		t.Fatalf("connectivity probe must not bill or write usage: %v count=%d", err, len(usages))
	}
	_, found, err := webtools.Load(context.Background(), f.store)
	if err != nil || found {
		t.Fatal("probe must not save configuration")
	}
	logs, _ := f.store.ListAuditLogs(context.Background(), store.AuditLogFilter{})
	for _, entry := range logs {
		if strings.Contains(string(entry.Detail), "private-upstream-response") || strings.Contains(string(entry.Detail), "sk-private-web-tool-provider") || strings.Contains(string(entry.Detail), provider.APIKeyCiphertext) {
			t.Fatal("probe audit exposed upstream body or key")
		}
	}
}
