package gwapp

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestGatewaySamePriorityRoutesRotateByWeight(t *testing.T) {
	for _, weights := range [][2]int64{{1, 1}, {3, 1}} {
		t.Run(fmt.Sprint(weights), func(t *testing.T) {
			recorder := &recordingAttemptRecorder{}
			f := newGatewayFixture(t, func(_ *Env, f *gatewayFixture) { f.attemptRecorder = recorder })
			f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("content-type", "application/json")
				_, _ = w.Write([]byte(openAISuccessBody))
			})
			ctx := context.Background()
			ids := make([]string, 2)
			for i, weight := range weights {
				provider, err := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
					Name: fmt.Sprint("rotation-", i), BaseURL: f.upstream.URL, Protocol: store.ProtocolOpenAIChatCompletions,
				})
				if err != nil {
					t.Fatal(err)
				}
				ids[i] = provider.ID
				if _, err := f.store.CreateUpstreamRoute(ctx, store.CreateUpstreamRouteInput{
					ModelID: "claude-sonnet-4-5", ProviderID: provider.ID, Priority: 100, Weight: weight, Enabled: true,
				}); err != nil {
					t.Fatal(err)
				}
			}
			for range 8 {
				if rec := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5","messages":[]}`); rec.Code != 200 {
					t.Fatalf("request failed: %d %s", rec.Code, rec.Body.String())
				}
			}
			attempts := recorder.snapshot()
			if len(attempts) != 8 || len(f.usageRecords()) != 8 {
				t.Fatalf("every successful request must be called and billed exactly once: %d", len(attempts))
			}
			counts := map[string]int{}
			for i, attempt := range attempts {
				counts[attempt.ProviderID]++
				if weights[0] == weights[1] && i > 0 && attempt.ProviderID == attempts[i-1].ProviderID {
					t.Fatal("equal-weight requests must alternate providers")
				}
			}
			for i, id := range ids {
				want := int(weights[i] * 8 / (weights[0] + weights[1]))
				if counts[id] != want {
					t.Fatalf("provider %d: got %d requests, want %d", i, counts[id], want)
				}
			}
		})
	}
}

func TestGatewayDisabledPoolNeverFallsBackToEnv(t *testing.T) {
	f := newGatewayFixture(t, nil)
	f.setHandler(func(http.ResponseWriter, *http.Request) { t.Fatal("disabled pool must never call env upstream") })
	ctx := context.Background()
	provider, err := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
		Name: "disabled-route", BaseURL: f.upstream.URL, Protocol: store.ProtocolOpenAIChatCompletions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateUpstreamRoute(ctx, store.CreateUpstreamRouteInput{
		ModelID: "claude-sonnet-4-5", ProviderID: provider.ID, Priority: 100, Weight: 1, Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	if rec := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5","messages":[]}`); rec.Code != 404 {
		t.Fatalf("disabled pool should fail closed: %d %s", rec.Code, rec.Body.String())
	}
	if records := f.usageRecords(); len(records) != 0 {
		t.Fatalf("unrouted request must not create usage: %+v", records)
	}
}
