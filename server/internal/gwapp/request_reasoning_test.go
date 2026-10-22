package gwapp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestRequestReasoningUsesActualProtocolParameters(t *testing.T) {
	for _, test := range []struct {
		name, route, fragment, effort string
		budget                        int64
	}{
		{"chat-high", "/v1/chat/completions", `"reasoning_effort":"high"`, "high", 0},
		{"chat-max", "/v1/chat/completions", `"reasoning_effort":"max"`, "max", 0},
		{"responses", "/v1/responses", `"reasoning":{"effort":"medium"}`, "medium", 0},
		{"thinking-budget", "/v1/messages", `"thinking":{"type":"enabled","budget_tokens":8192}`, "enabled", 8192},
		{"adaptive-effort", "/v1/messages", `"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}`, "max", 0},
		{"disabled", "/v1/messages", `"thinking":{"type":"disabled","budget_tokens":8192}`, "disabled", 0},
		{"missing", "/v1/chat/completions", `"temperature":0.1`, "", 0},
		{"null", "/v1/chat/completions", `"reasoning_effort":null`, "", 0},
		{"malformed", "/v1/chat/completions", `"reasoning_effort":{"value":"high"}`, "", 0},
		{"custom-actual", "/v1/chat/completions", `"reasoning_effort":"extended-v2"`, "extended-v2", 0},
		{"malformed-string", "/v1/chat/completions", `"reasoning_effort":" high "`, "", 0},
		{"wrong-protocol", "/v1/responses", `"reasoning_effort":"high"`, "", 0},
		{"responses-unrelated-thinking", "/v1/responses", `"thinking":{"type":"enabled","budget_tokens":8192}`, "", 0},
		{"messages-unrelated-chat-effort", "/v1/messages", `"reasoning_effort":"high"`, "", 0},
		{"zero-budget", "/v1/messages", `"thinking":{"type":"enabled","budget_tokens":0}`, "enabled", 0},
		{"invalid-budget", "/v1/messages", `"thinking":{"type":"enabled","budget_tokens":1.5}`, "enabled", 0},
		{"budget-alone", "/v1/messages", `"thinking":{"budget_tokens":8192}`, "", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newGatewayFixture(t, nil)
			f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("content-type", "application/json")
				_, _ = w.Write([]byte(`{"usage":{"input_tokens":2,"output_tokens":1,"prompt_tokens":2,"completion_tokens":1}}`))
			})
			body := fmt.Sprintf(`{"model":"claude-sonnet-4-5","max_tokens":100,"max_output_tokens":100,"messages":[],%s}`, test.fragment)
			response := f.do("POST", test.route, f.token, body)
			if response.Code != 200 {
				t.Fatal(response.Code, response.Body.String())
			}
			records := f.usageRecords()
			if len(records) != 1 || records[0].ReasoningEffort != test.effort {
				t.Fatalf("request effort was inferred or lost: %+v", records)
			}
			budget := records[0].ReasoningBudgetTokens
			if (test.budget == 0 && budget != nil) || (test.budget != 0 && (budget == nil || *budget != test.budget)) {
				t.Fatalf("actual thinking budget was inferred or lost: %v", budget)
			}
			_, sent := f.upstreamRequest()
			var actual map[string]any
			if json.Unmarshal(sent, &actual) != nil || actual["model"] == nil {
				t.Fatal("missing real upstream request")
			}
		})
	}
}

func TestRequestReasoningSurvivesStreamAndFailure(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint("failed-", failure), func(t *testing.T) {
			f := newGatewayFixture(t, nil)
			f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
				if failure {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, `{"error":{"type":"invalid_request_error","message":"fixture failure"}}`)
					return
				}
				w.Header().Set("content-type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
			})
			response := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5","stream":true,"reasoning_effort":"high"}`)
			if (!failure && response.Code != 200) || (failure && response.Code != 400) {
				t.Fatal(response.Code, response.Body.String())
			}
			records := f.usageRecords()
			if len(records) != 1 || records[0].ReasoningEffort != "high" || records[0].ReasoningBudgetTokens != nil || (failure && records[0].Status != "error") {
				t.Fatalf("terminal reasoning was lost: %+v", records)
			}
		})
	}
}
