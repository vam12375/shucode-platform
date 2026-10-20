package gwapp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

type recordingAttemptRecorder struct {
	mu      sync.Mutex
	records []store.GatewayAttemptRecord
}

func (r *recordingAttemptRecorder) InsertGatewayAttempt(_ context.Context, record store.GatewayAttemptRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, record)
	return nil
}

func (r *recordingAttemptRecorder) snapshot() []store.GatewayAttemptRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]store.GatewayAttemptRecord(nil), r.records...)
}

const openAISuccessBody = `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

func TestUpstreamRouteFailover(t *testing.T) {
	f := newGatewayFixture(t, nil)
	var calls atomic.Int32
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "upstream boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(openAISuccessBody))
	})
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())
	first, err := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
		Name: "failover-a-" + suffix, BaseURL: f.upstream.URL, Protocol: store.ProtocolOpenAIChatCompletions,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
		Name: "failover-b-" + suffix, BaseURL: f.upstream.URL, Protocol: store.ProtocolOpenAIChatCompletions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateUpstreamRoute(ctx, store.CreateUpstreamRouteInput{
		ModelID: "claude-sonnet-4-5", ProviderID: first.ID, Priority: 10, Weight: 1, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateUpstreamRoute(ctx, store.CreateUpstreamRouteInput{
		ModelID: "claude-sonnet-4-5", ProviderID: second.ID, Priority: 20, Weight: 1, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	rec := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5","messages":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("故障转移后应 200: %d %s", rec.Code, rec.Body.String())
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("应重试一次，实际上游调用 %d 次", got)
	}
	records := f.usageRecords()
	if len(records) != 1 || records[0].ProviderID != second.ID || records[0].Status != store.UsageStatusOk {
		t.Fatalf("用量应归属第二个供应商且成功: %+v", records)
	}
}

func TestFailoverRecordsEachAttempt(t *testing.T) {
	recorder := &recordingAttemptRecorder{}
	f := newGatewayFixture(t, func(_ *Env, f *gatewayFixture) {
		f.attemptRecorder = recorder
	})
	var calls atomic.Int32
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("request-id", "upstream-req-1")
			http.Error(w, "upstream boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("content-type", "application/json")
		w.Header().Set("x-request-id", "upstream-req-2")
		_, _ = w.Write([]byte(openAISuccessBody))
	})
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())
	first, err := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
		Name: "attempt-a-" + suffix, BaseURL: f.upstream.URL, Protocol: store.ProtocolOpenAIChatCompletions,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
		Name: "attempt-b-" + suffix, BaseURL: f.upstream.URL, Protocol: store.ProtocolOpenAIChatCompletions,
	})
	if err != nil {
		t.Fatal(err)
	}
	for priority, providerID := range map[int64]string{10: first.ID, 20: second.ID} {
		if _, err := f.store.CreateUpstreamRoute(ctx, store.CreateUpstreamRouteInput{
			ModelID: "claude-sonnet-4-5", ProviderID: providerID, Priority: priority, Weight: 1, Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	rec := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5","messages":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("故障转移后应 200: %d %s", rec.Code, rec.Body.String())
	}
	attempts := recorder.snapshot()
	if len(attempts) != 2 {
		t.Fatalf("应记录 2 次上游尝试: %+v", attempts)
	}
	if attempts[0].Attempt != 1 || attempts[0].Final || attempts[0].ProviderID != first.ID ||
		attempts[0].StatusCode == nil || *attempts[0].StatusCode != 500 ||
		attempts[0].ErrorType != "upstream_500" || attempts[0].ProviderRequestID != "upstream-req-1" {
		t.Fatalf("失败尝试轨迹不符: %+v", attempts[0])
	}
	if attempts[1].Attempt != 2 || !attempts[1].Final || attempts[1].ProviderID != second.ID ||
		attempts[1].StatusCode == nil || *attempts[1].StatusCode != 200 ||
		attempts[1].ErrorType != "" || attempts[1].ProviderRequestID != "upstream-req-2" {
		t.Fatalf("成功尝试轨迹不符: %+v", attempts[1])
	}
	if attempts[0].BaseHost == "" || attempts[0].UpstreamModel != "claude-sonnet-4-5" {
		t.Fatalf("尝试轨迹缺少上游/模型信息: %+v", attempts[0])
	}
}

func TestGatewayRateLimitRPM(t *testing.T) {
	f := newGatewayFixture(t, nil)
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(openAISuccessBody))
	})
	rpm := int64(1)
	if _, err := f.store.UpdateTeam(context.Background(), f.teamID, store.UpdateTeamPatch{RPMLimit: &rpm}); err != nil {
		t.Fatal(err)
	}
	first := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5","messages":[]}`)
	if first.Code != http.StatusOK {
		t.Fatalf("首个请求应成功: %d %s", first.Code, first.Body.String())
	}
	second := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5","messages":[]}`)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("超过 RPM 应 429: %d %s", second.Code, second.Body.String())
	}
	if !strings.Contains(second.Body.String(), "RPM") {
		t.Fatalf("429 文案应说明 RPM: %s", second.Body.String())
	}
}

func TestGatewayDuplicateRequestID(t *testing.T) {
	f := newGatewayFixture(t, nil)
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(openAISuccessBody))
	})
	requestID := "dup-" + fmt.Sprint(time.Now().UnixNano())
	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[]}`))
		req.Header.Set("authorization", "Bearer "+f.token)
		req.Header.Set("content-type", "application/json")
		req.Header.Set("x-request-id", requestID)
		rec := httptest.NewRecorder()
		f.engine.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(); rec.Code != http.StatusOK {
		t.Fatalf("首个请求应成功: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(); rec.Code != http.StatusConflict {
		t.Fatalf("重复 request_id 应 409: %d %s", rec.Code, rec.Body.String())
	}
	if records := f.usageRecords(); len(records) != 1 {
		t.Fatalf("重复 request_id 不应重复记账: %d", len(records))
	}
}
