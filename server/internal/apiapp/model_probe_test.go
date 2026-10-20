package apiapp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func seedProbeModel(t *testing.T, f *tenantFixture) string {
	t.Helper()
	ctx := context.Background()
	encrypted, err := f.deps.SecretBox.Encrypt("sk-probe-secret")
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	provider, err := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
		Name: "probe-provider", BaseURL: f.upstream.URL, Protocol: store.ProtocolOpenAIChatCompletions,
		APIKeyCiphertext: encrypted, APIKeyHint: "cret", Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpsertModel(ctx, store.UpsertModelInput{
		ModelID: "probe-model", DisplayName: "Probe Model",
		InputPricePerMtok: "1", OutputPricePerMtok: "2", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateUpstreamRoute(ctx, store.CreateUpstreamRouteInput{
		ModelID: "probe-model", ProviderID: provider.ID, UpstreamModel: "upstream-probe",
		Priority: 100, Weight: 1, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	return provider.ID
}

func TestModelProbeStreamsFirstTokenForRoot(t *testing.T) {
	f := newTenantFixture(t)
	seedProbeModel(t, f)
	f.upstreamFn = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"p\"}}]}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}

	rec := f.do("POST", "/console/api/models/probe-model/test", f.rootCookie, "")
	if rec.Code != 200 {
		t.Fatalf("探测失败: %d %s", rec.Code, rec.Body.String())
	}
	result := dataOf(t, rec)
	if result["ok"] != true || result["kind"] != "ok" || result["status"] != float64(200) {
		t.Fatalf("探测结果不符: %v", result)
	}
	if result["firstTokenMs"] == nil || result["totalMs"].(float64) < 0 || result["testedAt"] == "" {
		t.Fatalf("延迟字段缺失: %v", result)
	}
	hit := f.lastUpstreamHit()
	if hit.method != "POST" || hit.url != "/v1/chat/completions" {
		t.Fatalf("探测请求不符: %s %s", hit.method, hit.url)
	}
	if hit.headers.Get("authorization") != "Bearer sk-probe-secret" {
		t.Fatalf("鉴权头不符: %q", hit.headers.Get("authorization"))
	}
	var sent map[string]any
	_ = json.Unmarshal(hit.body, &sent)
	if sent["model"] != "upstream-probe" || sent["stream"] != true || sent["max_tokens"] != float64(1) {
		t.Fatalf("探测体不符: %v", sent)
	}

	// 普通用户目录不再下发售价。
	listRec := f.do("GET", "/console/api/models", f.memberCookie, "")
	rows := dataOf(t, listRec)["models"].([]any)
	if len(rows) != 1 {
		t.Fatalf("模型列表数量不符: %v", rows)
	}
	row := rows[0].(map[string]any)
	if _, exists := row["inputPricePerMtok"]; exists {
		t.Fatalf("普通用户不应收到售价: %v", row)
	}
	if row["displayName"] != "Probe Model" {
		t.Fatalf("模型展示字段缺失: %v", row)
	}
}

func TestModelProbeFailsOverToNextRoute(t *testing.T) {
	f := newTenantFixture(t)
	ctx := context.Background()
	encrypted, err := f.deps.SecretBox.Encrypt("sk-probe-secret")
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	broken, err := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
		Name: "broken-provider", BaseURL: "http://127.0.0.1:1", Protocol: store.ProtocolOpenAIChatCompletions,
		APIKeyCiphertext: encrypted, APIKeyHint: "cret", Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	working, err := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
		Name: "working-provider", BaseURL: f.upstream.URL, Protocol: store.ProtocolOpenAIChatCompletions,
		APIKeyCiphertext: encrypted, APIKeyHint: "cret", Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpsertModel(ctx, store.UpsertModelInput{
		ModelID: "failover-model", DisplayName: "Failover", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct {
		provider string
		priority int64
	}{{broken.ID, 10}, {working.ID, 20}} {
		if _, err := f.store.CreateUpstreamRoute(ctx, store.CreateUpstreamRouteInput{
			ModelID: "failover-model", ProviderID: route.provider, UpstreamModel: "upstream-failover",
			Priority: route.priority, Weight: 1, Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	f.upstreamFn = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"p\"}}]}\n\n"))
	}

	rec := f.do("POST", "/console/api/models/failover-model/test", f.rootCookie, "")
	if rec.Code != 200 {
		t.Fatalf("探测失败: %d %s", rec.Code, rec.Body.String())
	}
	result := dataOf(t, rec)
	if result["ok"] != true || result["kind"] != "ok" {
		t.Fatalf("应切换到可用渠道: %v", result)
	}
	if hit := f.lastUpstreamHit(); hit.url != "/v1/chat/completions" {
		t.Fatalf("可用渠道未被调用: %s", hit.url)
	}
}

func TestModelProbeClassifiesFailuresAndRespectsPolicy(t *testing.T) {
	f := newTenantFixture(t)
	seedProbeModel(t, f)
	f.upstreamFn = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key sk-probe-secret"}`))
	}
	rec := f.do("POST", "/console/api/models/probe-model/test", f.rootCookie, "")
	if rec.Code != 200 {
		t.Fatalf("探测应返回 200 结果: %d %s", rec.Code, rec.Body.String())
	}
	result := dataOf(t, rec)
	if result["ok"] != false || result["kind"] != "auth" || result["status"] != float64(401) {
		t.Fatalf("失败归类不符: %v", result)
	}
	if strings.Contains(rec.Body.String(), "sk-probe-secret") {
		t.Fatal("响应不得包含明文密钥")
	}

	// 未配置任何上游的模型：给出可展示的 not_configured，而不是 500。
	enabled := true
	if _, err := f.store.UpsertModel(context.Background(), store.UpsertModelInput{
		ModelID: "unrouted-model", DisplayName: "Unrouted", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	unrouted := dataOf(t, f.do("POST", "/console/api/models/unrouted-model/test", f.rootCookie, ""))
	if unrouted["ok"] != false || unrouted["kind"] != "not_configured" {
		t.Fatalf("未配置上游结果不符: %v", unrouted)
	}

}

func TestModelProbeCannotBypassTeamCredit(t *testing.T) {
	f := newTenantFixture(t)
	seedProbeModel(t, f)
	for _, cookie := range []string{f.memberCookie, f.adminCookie, f.unassignedCookie} {
		rec := f.do("POST", "/console/api/models/probe-model/test", cookie, "")
		if rec.Code != 403 {
			t.Fatalf("普通成员/团队管理员不能绕过计费: %d %s", rec.Code, rec.Body.String())
		}
	}
	f.upstreamMu.Lock()
	defer f.upstreamMu.Unlock()
	if len(f.upstreamHits) != 0 {
		t.Fatalf("未授权诊断不能触达上游: %+v", f.upstreamHits)
	}
}
