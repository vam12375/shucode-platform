package gwapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/credits"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestUnboundedGenerationRejectedBeforeUpstream(t *testing.T) {
	for _, extra := range []string{
		`"background":true`, `"previous_response_id":"resp_hidden"`, `"conversation":"conv_hidden"`, `"prompt":{"id":"prompt_hidden"}`, `"mcp_servers":[{}]`,
		`"best_of":2`, `"input":[{"type":"item_reference","id":"hidden"}]`, `"tools":[{"type":"web_search"}]`,
		`"input":[{"type":"input_image","image_url":"https://example.invalid/a.png"}]`,
		`"input":[{"type":"input_file","file_id":"file_hidden"}]`,
		`"modalities":["text","audio"]`,
	} {
		t.Run(extra, func(t *testing.T) {
			f := newGatewayFixture(t, nil)
			rec := f.do("POST", "/v1/responses", f.token, `{"model":"claude-sonnet-4-5",`+extra+`}`)
			if rec.Code != 400 {
				t.Fatalf("不可约束的费用路径必须准入前拒绝: %d %s", rec.Code, rec.Body.String())
			}
			if r, _ := f.upstreamRequest(); r != nil {
				t.Fatal("不应调用上游")
			}
		})
	}
}

func TestBalanceFitsOutputLimitAndStopsAtZero(t *testing.T) {
	f := newGatewayFixture(t, nil)
	ctx := context.Background()
	_, err := f.store.UpdateTeam(ctx, f.teamID, store.UpdateTeamPatch{QuotaCredits: ptr("0.003000")})
	if err != nil {
		t.Fatal(err)
	}
	var outputLimit atomic.Int64
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		_, body := f.upstreamRequest()
		if !strings.Contains(string(body), `"max_tokens":`) {
			t.Error("必须写回输出上限")
		}
		// 请求的 4096 tokens 无法覆盖，余额应只放行可支付的输出上限。
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		outputLimit.Store(int64(payload["max_tokens"].(float64)))
		fmt.Fprint(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	})
	rec := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 || outputLimit.Load() < 1 || outputLimit.Load() >= 4096 {
		t.Fatalf("应缩小输出上限: %d limit=%d %s", rec.Code, outputLimit.Load(), rec.Body.String())
	}
	// used+reserved 达到 quota 即使零价请求也必须拒绝。
	team := f.team()
	used, _ := credits.ParseCreditsToMicro(team.UsedCredits)
	ok, err := f.store.TryReserveQuota(ctx, f.teamID, 3000-used)
	if err != nil || !ok {
		t.Fatalf("reserve rest: %v %v", ok, err)
	}
	rec = f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5","max_tokens":1}`)
	if rec.Code != 429 {
		t.Fatalf("余额0拒绝: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTransportUnknownSettlesReservationWithoutRetry(t *testing.T) {
	f := newGatewayFixture(t, nil)
	for i := 0; i < 3; i++ {
		provider, err := f.store.CreateUpstreamProvider(context.Background(), store.CreateUpstreamProviderInput{Name: fmt.Sprintf("unknown-%d", i), BaseURL: f.upstream.URL, Protocol: store.ProtocolOpenAIChatCompletions})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.CreateUpstreamRoute(context.Background(), store.CreateUpstreamRouteInput{ModelID: "claude-sonnet-4-5", ProviderID: provider.ID, Priority: int64(i), Weight: 1, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}

	var calls atomic.Int64
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	})
	rec := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5","max_tokens":10}`)
	if rec.Code != 502 {
		t.Fatalf("未知传输结果: %d", rec.Code)
	}
	records := f.usageRecords()
	if calls.Load() != 1 || len(records) != 1 || records[0].CreditsMicro <= 0 || records[0].PriceSnapshot == nil || !records[0].PriceSnapshot.Cost.UsageEstimated {
		t.Fatalf("未知成本不能免费或重试: calls=%d records=%+v", calls.Load(), records)
	}
}

func TestOutputAliasesCannotOverrideReservedLimit(t *testing.T) {
	for _, tc := range []struct {
		spec protocolSpec
		body map[string]any
		key  string
	}{
		{buildOpenAISpec(), map[string]any{"max_completion_tokens": float64(1), "max_tokens": float64(256000)}, "max_completion_tokens"},
		{buildOpenAIResponsesSpec(), map[string]any{"max_output_tokens": float64(1), "max_tokens": float64(256000)}, "max_output_tokens"},
	} {
		tc.spec.applyOutputLimit(tc.body, 1)
		if tc.body[tc.key] != float64(1) {
			t.Fatal("有效上限未写回")
		}
		if _, exists := tc.body["max_tokens"]; exists {
			t.Fatal("别名不能保留另一套更高输出上限")
		}
	}
	if got := readMaxTokensFrom(float64(1e30)); got != maxMaxTokens {
		t.Fatalf("巨大上限必须夹紧不能整型溢出: %d", got)
	}
}
