package gwapp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestProviderErrorsNeverExposeTransportDetails(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses"} {
		for _, status := range []int{400, 401, 429, 502} {
			f := newGatewayFixture(t, nil)
			f.handler = func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("content-type", "text/html")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`ToAPIs https://supplier.example.test/private upstream-key trace-secret`))
			}
			rec := f.do("POST", path, f.token, `{"model":"claude-sonnet-4-5","messages":[],"input":"test","max_tokens":1}`)
			if rec.Code != status || !strings.Contains(rec.Body.String(), publicProviderFailure) {
				t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
			}
			for _, hidden := range []string{"ToAPIs", "supplier.example", "upstream-key", "trace-secret"} {
				if strings.Contains(rec.Body.String(), hidden) {
					t.Fatal("leaked provider detail", hidden)
				}
			}
			if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
				t.Fatal("upstream HTML content-type escaped")
			}
		}
	}
}

func TestProviderSSEErrorsAreFilteredAcrossEveryByteBoundary(t *testing.T) {
	good := "data: {\"choices\":[{\"delta\":{\"content\":\"正常内容\"}}]}\n\n"
	bad := "event: error\r\ndata: {\"type\":\"error\",\"error\":{\"message\":\"ToAPIs secret-key\"},\"trace\":\"https://supplier.test\"}\r\n\r\n"
	end := "data: [DONE]\n\n"
	input := []byte(good + bad + end)
	for split := 0; split <= len(input); split++ {
		filter := &providerEventFilter{}
		first, err := filter.push(input[:split])
		if err != nil {
			t.Fatal(err)
		}
		second, err := filter.push(input[split:])
		if err != nil {
			t.Fatal(err)
		}
		result := string(append(first, second...))
		if !strings.HasPrefix(result, good) || !strings.HasSuffix(result, end) || !strings.Contains(result, publicProviderFailure) {
			t.Fatalf("split %d corrupted stream: %s", split, result)
		}
		for _, hidden := range []string{"ToAPIs", "secret-key", "supplier.test"} {
			if strings.Contains(result, hidden) {
				t.Fatal("SSE leaked", hidden)
			}
		}
	}
	filter := &providerEventFilter{}
	comment, err := filter.push([]byte(": ToAPIs gateway\n\n"))
	if err != nil || string(comment) != ": keep-alive\n\n" {
		t.Fatal("supplier comment escaped", string(comment), err)
	}
	if _, err := filter.push(bytes.Repeat([]byte("x"), maxSSEBufferChars+1)); err == nil {
		t.Fatal("unbounded SSE buffer")
	}
}

func TestResponsesErrorsKeepUsageAndSuccessPayloads(t *testing.T) {
	success := []byte(`{"id":"r1","output":[],"usage":{"input_tokens":3,"output_tokens":5},"error":null}`)
	if !bytes.Equal(success, publicProviderPayload(success, false)) {
		t.Fatal("success payload changed")
	}
	for _, input := range []string{
		`{"type":"response.failed","sequence_number":3,"response":{"id":"r1","status":"failed","usage":{"input_tokens":3,"output_tokens":5},"error":{"message":"ToAPIs"},"debug":"supplier.test"}}`,
		`{"object":"response","id":"r1","status":"failed","usage":{"input_tokens":3,"output_tokens":5},"error":{"message":"ToAPIs"},"debug":"supplier.test"}`,
	} {
		out := publicProviderPayload([]byte(input), false)
		if strings.Contains(string(out), "ToAPIs") || strings.Contains(string(out), "supplier.test") {
			t.Fatal("Responses error leaked", string(out))
		}
		var body map[string]any
		if err := json.Unmarshal(out, &body); err != nil {
			t.Fatal(err)
		}
		if nested, ok := body["response"].(map[string]any); ok {
			body = nested
		}
		usage := body["usage"].(map[string]any)
		if usage["input_tokens"] != float64(3) || usage["output_tokens"] != float64(5) || body["id"] != "r1" {
			t.Fatal("Responses usage lost", body)
		}
	}
}
