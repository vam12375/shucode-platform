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

func TestMediaAndHostedToolsAdmitWithoutBalanceHold(t *testing.T) {
	for _, extra := range []string{
		`"previous_response_id":"resp_hidden"`, `"conversation":"conv_hidden"`, `"prompt":{"id":"prompt_hidden"}`, `"mcp_servers":[{}]`,
		`"best_of":2`,
		`"input":[{"type":"item_reference","id":"hidden"}]`, `"tools":[{"type":"web_search"}]`,
		`"tools":[{"type":"mcp","server_url":"https://example.invalid/mcp"}]`,
		`"input":[{"type":"input_image","image_url":"https://example.invalid/a.png"}]`,
		`"input":[{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]`,
		`"input":[{"type":"input_file","file_id":"file_hidden"}]`,
		`"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]`,
		`"modalities":["text","audio"]`,
	} {
		t.Run(extra, func(t *testing.T) {
			f := newGatewayFixture(t, nil)
			f.setHandler(func(w http.ResponseWriter, r *http.Request) {
				team := f.team()
				if team.ReservedCredits != "0.000000" || team.UsedCredits != "0.000000" {
					t.Errorf("发送上游前余额不能占用或实扣: %+v", team)
				}
				records, err := f.store.ListOpenReservations(context.Background(), 10)
				if err != nil || len(records) != 1 || records[0].ReservedMicro != 0 || !records[0].Postpaid {
					t.Errorf("应有持久零占用请求身份: %+v %v", records, err)
				}
				fmt.Fprint(w, `{"usage":{"input_tokens":100,"output_tokens":20}}`)
			})
			rec := f.do("POST", "/v1/responses", f.token, `{"model":"claude-sonnet-4-5",`+extra+`}`)
			if rec.Code != 200 {
				t.Fatalf("媒体/托管工具必须兼容: %d %s", rec.Code, rec.Body.String())
			}
			team := f.team()
			if team.ReservedCredits != "0.000000" || team.UsedCredits != "0.000600" {
				t.Fatalf("按真实用量结算: %+v", team)
			}
		})
	}
}

func TestFunctionToolSchemaAdmitsWithoutHold(t *testing.T) {
	for _, tools := range []string{
		`[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{"file_id":{"type":"string"},"image_url":{"type":"string"},"file_data":{"type":"string"},"background":{"type":"boolean"}}}}}]`,
		`[{"name":"read_file","input_schema":{"type":"object","properties":{"type":{"const":"input_image"},"input_audio":{"type":"string"}}}}]`,
	} {
		t.Run(tools, func(t *testing.T) {
			f := newGatewayFixture(t, nil)
			f.setHandler(func(w http.ResponseWriter, r *http.Request) {
				team := f.team()
				reserved, _ := credits.ParseCreditsToMicro(team.ReservedCredits)
				if reserved != 0 {
					t.Errorf("本地工具不能预扣余额: %+v", team)
				}
				fmt.Fprint(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
			})
			rec := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}],"tools":`+tools+`}`)
			if rec.Code != 200 {
				t.Fatalf("工具 schema 属性不能误拒绝: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestPositiveBalanceKeepsOutputLimitAndSettledExhaustionStopsNewRequests(t *testing.T) {
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
		// 正余额准入后保留请求输出上限，不按预估成本缩减。
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		outputLimit.Store(int64(payload["max_tokens"].(float64)))
		fmt.Fprint(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	})
	rec := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 || outputLimit.Load() != 4096 {
		t.Fatalf("应保留正常输出上限: %d limit=%d %s", rec.Code, outputLimit.Load(), rec.Body.String())
	}
	// 已结算余额达到 0 后拒绝新请求。
	team := f.team()
	if _, err := f.store.UpdateTeam(ctx, f.teamID, store.UpdateTeamPatch{QuotaCredits: &team.UsedCredits}); err != nil {
		t.Fatal(err)
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
	if calls.Load() != 1 || len(records) != 1 || records[0].CreditsMicro != 0 || records[0].PriceSnapshot == nil || records[0].PriceSnapshot.Cost.UsageEstimated {
		t.Fatalf("未知 usage 不估算扣款且不盲目重试: calls=%d records=%+v", calls.Load(), records)
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
