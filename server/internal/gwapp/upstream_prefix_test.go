package gwapp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestGatewayVersionedAPIPrefix(t *testing.T) {
	for _, tc := range []struct {
		protocol     store.UpstreamProtocol
		path, result string
	}{
		{store.ProtocolOpenAIChatCompletions, "/chat/completions", openAISuccessBody},
		{store.ProtocolOpenAIResponses, "/responses", `{"usage":{"input_tokens":10,"output_tokens":5}}`},
		{store.ProtocolAnthropicMessages, "/messages", `{"usage":{"input_tokens":10,"output_tokens":5}}`},
	} {
		t.Run(string(tc.protocol), func(t *testing.T) {
			for _, useRoute := range []bool{false, true} {
				t.Run(fmt.Sprintf("route=%t", useRoute), func(t *testing.T) {
					f := newGatewayFixture(t, nil)
					ctx := context.Background()
					provider, err := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
						Name: "versioned-provider", BaseURL: f.upstream.URL + "/api/v3/", Protocol: tc.protocol,
					})
					if err != nil {
						t.Fatal(err)
					}
					if useRoute {
						_, err = f.store.CreateUpstreamRoute(ctx, store.CreateUpstreamRouteInput{
							ModelID: "claude-sonnet-4-5", ProviderID: provider.ID, UpstreamModel: "ark-model",
							Priority: 100, Weight: 1, Enabled: true,
						})
					} else {
						_, err = f.store.UpsertModel(ctx, store.UpsertModelInput{
							ModelID: "claude-sonnet-4-5", DisplayName: "Test Model",
							InputPricePerMtok: "3", OutputPricePerMtok: "15",
							ProvideUpstreamProviderID: true, UpstreamProviderID: provider.ID,
							ProvideUpstreamModel: true, UpstreamModel: "ark-model",
						})
					}
					if err != nil {
						t.Fatal(err)
					}
					f.setHandler(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path != "/api/v3"+tc.path {
							http.NotFound(w, r)
							return
						}
						w.Header().Set("content-type", "application/json")
						fmt.Fprint(w, tc.result)
					})
					rec := f.do("POST", "/v1"+tc.path, f.token, `{"model":"claude-sonnet-4-5"}`)
					if rec.Code != 200 {
						t.Fatalf("版本前缀转发失败: %d %s", rec.Code, rec.Body.String())
					}
					if req, body := f.upstreamRequest(); req.URL.Path != "/api/v3"+tc.path || !strings.Contains(string(body), `"model":"ark-model"`) {
						t.Fatalf("路径/模型映射错误: %s %s", req.URL.Path, body)
					}
					if records := f.usageRecords(); len(records) != 1 || records[0].ProviderID != provider.ID || records[0].Status != store.UsageStatusOk {
						t.Fatalf("前缀转发仍须正常计量: %+v", records)
					}
				})
			}
		})
	}
}

func TestProviderRecoveryVersionedAPIPrefix(t *testing.T) {
	f := newGatewayFixture(t, nil)
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/models" || r.Header.Get("authorization") != "Bearer test-key" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"data":[]}`)
	})
	provider := store.UpstreamProviderRecord{BaseURL: f.upstream.URL + "/api/v3", Protocol: store.ProtocolOpenAIChatCompletions}
	if err := probeProvider(context.Background(), backgroundTestDeps(f), provider, "test-key"); err != nil {
		t.Fatalf("自动恢复探测应保留版本前缀: %v", err)
	}
}

func TestBackgroundGenerationVersionedAPIPrefix(t *testing.T) {
	f := newGatewayFixture(t, func(env *Env, f *gatewayFixture) {
		env.UpstreamBaseURL = f.upstream.URL + "/api/v3/"
	})
	var cancelled atomic.Bool
	var polls atomic.Int64
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/responses":
			fmt.Fprint(w, `{"id":"resp_prefix","status":"queued"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/responses/resp_prefix":
			polls.Add(1)
			status := "in_progress"
			if cancelled.Load() {
				status = "cancelled"
			}
			fmt.Fprintf(w, `{"id":"resp_prefix","status":%q,"usage":{"input_tokens":10,"output_tokens":5}}`, status)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/responses/resp_prefix/cancel":
			cancelled.Store(true)
			fmt.Fprint(w, `{"id":"resp_prefix","status":"cancelled"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v3/responses/resp_prefix":
			fmt.Fprint(w, `{"id":"resp_prefix","deleted":true}`)
		default:
			http.NotFound(w, r)
		}
	})
	if rec := f.do("POST", "/v1/responses", f.token, `{"model":"claude-sonnet-4-5","background":true}`); rec.Code != 200 {
		t.Fatalf("创建后台任务: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("GET", "/v1/responses/resp_prefix", f.token, ""); rec.Code != 200 {
		t.Fatalf("查询后台任务: %d %s", rec.Code, rec.Body.String())
	}
	deps := backgroundTestDeps(f)
	reconcileBackgroundGenerations(context.Background(), deps)
	if polls.Load() != 2 || f.team().ReservedCredits != "100.000000" {
		t.Fatalf("后台轮询须使用正确前缀并保持额度预留: polls=%d team=%+v", polls.Load(), f.team())
	}
	if rec := f.do("POST", "/v1/responses/resp_prefix/cancel", f.token, ""); rec.Code != 200 || !cancelled.Load() {
		t.Fatalf("取消后台任务: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("DELETE", "/v1/responses/resp_prefix", f.token, ""); rec.Code != 200 {
		t.Fatalf("删除后台任务: %d %s", rec.Code, rec.Body.String())
	}
	if f.team().ReservedCredits != "0.000000" || len(f.usageRecords()) != 1 {
		t.Fatalf("后台终态应正常结算: team=%+v usage=%+v", f.team(), f.usageRecords())
	}
}
