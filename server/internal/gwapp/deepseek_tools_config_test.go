package gwapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/contracts"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/ratelimit"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"github.com/vam12375/shucode-platform/server/internal/webtools"
)

func newConfiguredWebToolsFixture(t *testing.T) (*gatewayFixture, *crypto.SecretBox, *store.UpstreamProviderRecord, webtools.Config, Deps) {
	t.Helper()
	f := newGatewayFixture(t, func(env *Env, f *gatewayFixture) {
		// 旧环境仍可用时，后台配置必须保持唯一优先级。
		env.DeepSeekToolsBaseURL, env.DeepSeekToolsAPIKey = f.upstream.URL, "legacy-key"
		env.DeepSeekSearchFeeMicro, env.DeepSeekFetchFeeMicro = 999, 999
	})
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		handler := f.handler
		f.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(tls.Close)
	box, err := crypto.NewSecretBox(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("w", 32))))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := box.Encrypt("configured-key")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := f.store.CreateUpstreamProvider(context.Background(), store.CreateUpstreamProviderInput{
		Name: "Web tools provider", BaseURL: tls.URL + "/v1/", Protocol: store.ProtocolOpenAIChatCompletions,
		APIKeyCiphertext: ciphertext, TimeoutMs: ptr(int64(5_000)), Enabled: ptr(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := webtools.Save(context.Background(), f.store, webtools.MissingVersion, webtools.Config{
		Enabled: true, ProviderID: provider.ID, SearchEnabled: true, FetchEnabled: true,
		SearchSaleMicro: 7_000, SearchCostMicro: 3_000, FetchSaleMicro: 8_000, FetchCostMicro: 4_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := tls.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return fmt.Errorf("redirect blocked") }
	deps := Deps{Store: f.store, Env: f.env, Now: time.Now, SecretBox: box, HTTPClient: client,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Trusted: NewTrustedProxies(nil),
		RateLimiter: ratelimit.NewGatewayLimiter(nil)}
	f.engine = New(deps)
	return f, box, provider, config, deps
}

func TestDeepSeekToolsCapabilitiesAreAuthenticatedAndNonSensitive(t *testing.T) {
	f, _, _, config, _ := newConfiguredWebToolsFixture(t)
	if r := f.do("GET", contracts.WebToolsCapabilitiesPath, "", ""); r.Code != 401 {
		t.Fatalf("missing auth: %d", r.Code)
	}
	r := f.do("GET", contracts.WebToolsCapabilitiesPath, f.token, "")
	value, err := contracts.ParseWebToolsCapabilities(r.Body.Bytes())
	if r.Code != 200 || err != nil || !value.Enabled || !value.SearchEnabled || !value.FetchEnabled || value.SearchSaleMicro == nil || *value.SearchSaleMicro != 7_000 {
		t.Fatalf("capabilities: %d %s %v", r.Code, r.Body.String(), err)
	}
	for _, secret := range []string{"cost", "configured-key", "providerId", `"version"`, "baseUrl"} {
		if strings.Contains(strings.ToLower(r.Body.String()), strings.ToLower(secret)) {
			t.Fatalf("capability leaked %q: %s", secret, r.Body.String())
		}
	}
	config.SearchEnabled = false
	if _, err = webtools.Save(context.Background(), f.store, config.Version, config); err != nil {
		t.Fatal(err)
	}
	r = f.do("GET", contracts.WebToolsCapabilitiesPath, f.token, "")
	value, err = contracts.ParseWebToolsCapabilities(r.Body.Bytes())
	if err != nil || value.SearchEnabled || value.SearchSaleMicro != nil || !value.FetchEnabled || value.FetchSaleMicro == nil {
		t.Fatalf("disabled subtool: %s %v", r.Body.String(), err)
	}
	if r = f.do("POST", deepSeekSearchPath, f.token, `{"query":"off"}`); r.Code != 503 || !strings.Contains(r.Body.String(), "web_tools_unavailable") {
		t.Fatalf("disabled search: %d %s", r.Code, r.Body.String())
	}
}

func TestDeepSeekToolsConfiguredFreeSaleKeepsSupplierCost(t *testing.T) {
	f, _, provider, config, _ := newConfiguredWebToolsFixture(t)
	config.SearchSaleMicro = 0
	config, err := webtools.Save(context.Background(), f.store, config.Version, config)
	if err != nil {
		t.Fatal(err)
	}
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != deepSeekSearchPath || r.Header.Get("authorization") != "Bearer configured-key" {
			t.Errorf("configured target: %s %s", r.URL.Path, r.Header.Get("authorization"))
		}
		_, _ = w.Write([]byte(`{"results":[{"title":"result"}]}`))
	})
	r := f.do("POST", deepSeekSearchPath, f.token, `{"query":"free search","count":50}`)
	rows := f.usageRecords()
	if r.Code != 200 || len(rows) != 1 || rows[0].Status != store.UsageStatusOk || rows[0].CreditsMicro != 0 || rows[0].ProviderID != provider.ID || f.team().UsedCredits != "0.000000" {
		t.Fatalf("free sale usage: %d %+v", r.Code, rows)
	}
	s := rows[0].PriceSnapshot
	if s == nil || s.Sale.PerRequest != "0.000000" || s.Cost.PricingVersion != config.Version || s.Cost.Rates.PerRequest != "0.003000" || s.Cost.EstimatedMicro == nil || *s.Cost.EstimatedMicro != 3_000 {
		t.Fatalf("free sale discarded cost: %+v", s)
	}
	if r.Header().Get("x-request-id") == "" {
		t.Fatal("tool response omitted request id")
	}
}

func TestDeepSeekToolsConfiguredRequestIDsAreTeamScopedAcrossTools(t *testing.T) {
	f, _, _, _, _ := newConfiguredWebToolsFixture(t)
	var calls atomic.Int64
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = w.Write([]byte(`{"results":[{}]}`)) })
	for i, tc := range []struct {
		path, body string
		status     int
	}{{deepSeekSearchPath, `{"query":"first"}`, 200}, {deepSeekFetchPath, `{"url":"https://example.test"}`, 409}} {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		req.Header.Set("authorization", "Bearer "+f.token)
		req.Header.Set("x-request-id", "one-team-request")
		r := httptest.NewRecorder()
		f.engine.ServeHTTP(r, req)
		if r.Code != tc.status || r.Header().Get("x-request-id") != "one-team-request" {
			t.Fatalf("attempt%d: %d %s", i, r.Code, r.Body.String())
		}
		if tc.status == 409 && !strings.Contains(r.Body.String(), `"code":"conflict_error"`) {
			t.Fatalf("duplicate code: %s", r.Body.String())
		}
	}
	if calls.Load() != 1 || len(f.usageRecords()) != 1 || f.team().UsedCredits != "0.007000" {
		t.Fatalf("team request identity changed: calls%d rows%d used%s", calls.Load(), len(f.usageRecords()), f.team().UsedCredits)
	}
}

func TestDeepSeekToolsNeverFallBackFromSavedConfiguration(t *testing.T) {
	for _, mode := range []string{"disabled", "malformed", "provider-disabled", "missing-key", "decrypt-failed", "missing-secret-box", "missing-provider"} {
		t.Run(mode, func(t *testing.T) {
			f, _, provider, config, deps := newConfiguredWebToolsFixture(t)
			var calls atomic.Int64
			f.setHandler(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = w.Write([]byte(`{"results":[{}]}`)) })
			ctx := context.Background()
			switch mode {
			case "disabled":
				config.Enabled = false
				_, err := webtools.Save(ctx, f.store, config.Version, config)
				if err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := f.store.SetConfigValue(ctx, webtools.ConfigKey, []byte(`{"enabled":true}`)); err != nil {
					t.Fatal(err)
				}
			case "provider-disabled":
				if _, err := f.store.UpdateUpstreamProvider(ctx, provider.ID, store.UpdateUpstreamProviderPatch{Enabled: ptr(false)}); err != nil {
					t.Fatal(err)
				}
			case "missing-key":
				if _, err := f.store.UpdateUpstreamProvider(ctx, provider.ID, store.UpdateUpstreamProviderPatch{ProvideAPIKey: true}); err != nil {
					t.Fatal(err)
				}
			case "decrypt-failed":
				if _, err := f.store.UpdateUpstreamProvider(ctx, provider.ID, store.UpdateUpstreamProviderPatch{ProvideAPIKey: true, APIKeyCiphertext: "invalid"}); err != nil {
					t.Fatal(err)
				}
			case "missing-secret-box":
				deps.SecretBox = nil
				f.engine = New(deps)
			case "missing-provider":
				config.ProviderID = "00000000-0000-4000-8000-000000000099"
				if _, err := webtools.Save(ctx, f.store, config.Version, config); err != nil {
					t.Fatal(err)
				}
			}
			r := f.do("POST", deepSeekSearchPath, f.token, `{"query":"must stay unavailable"}`)
			if r.Code != 503 || !strings.Contains(r.Body.String(), `"code":"web_tools_unavailable"`) || calls.Load() != 0 || len(f.usageRecords()) != 0 {
				t.Fatalf("saved %s fell back: %d %s calls=%d", mode, r.Code, r.Body.String(), calls.Load())
			}
			r = f.do("GET", contracts.WebToolsCapabilitiesPath, f.token, "")
			capabilities, err := contracts.ParseWebToolsCapabilities(r.Body.Bytes())
			if r.Code != 200 || err != nil || capabilities.Enabled || capabilities.SearchSaleMicro != nil || capabilities.FetchSaleMicro != nil {
				t.Fatalf("unavailable capabilities: %d %s", r.Code, r.Body.String())
			}
		})
	}
}

func TestDeepSeekToolsFreezeProviderKeyBaseAndPricesWhileInFlight(t *testing.T) {
	f, box, provider, config, _ := newConfiguredWebToolsFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	releaseAll := sync.OnceFunc(func() { close(release) })
	defer releaseAll()
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer configured-key" {
			t.Errorf("in-flight key changed")
		}
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"results":[{"title":"old provider"}]}`))
	})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.do("POST", deepSeekSearchPath, f.token, `{"query":"frozen request"}`) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not start")
	}
	ciphertext, err := box.Encrypt("new-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.UpdateUpstreamProvider(context.Background(), provider.ID, store.UpdateUpstreamProviderPatch{BaseURL: ptr("https://changed.example.test"), ProvideAPIKey: true, APIKeyCiphertext: ciphertext, Enabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	config.SearchSaleMicro, config.SearchCostMicro = 25_000, 15_000
	if _, err = webtools.Save(context.Background(), f.store, config.Version, config); err != nil {
		t.Fatal(err)
	}
	releaseAll()
	r := <-done
	rows := f.usageRecords()
	if r.Code != 200 || len(rows) != 1 || rows[0].CreditsMicro != 7_000 || rows[0].ProviderID != provider.ID || rows[0].PriceSnapshot.Cost.PricingVersion != config.Version || rows[0].PriceSnapshot.Sale.PerRequest != "0.007000" || rows[0].PriceSnapshot.Cost.EstimatedMicro == nil || *rows[0].PriceSnapshot.Cost.EstimatedMicro != 3_000 {
		t.Fatalf("in-flight snapshot changed: %d %+v", r.Code, rows)
	}
	if r = f.do("POST", deepSeekSearchPath, f.token, `{"query":"next request"}`); r.Code != 503 {
		t.Fatalf("next request ignored provider disable: %d %s", r.Code, r.Body.String())
	}
}

func TestDeepSeekToolsShareEffectiveRPMAndConcurrencyWithoutTPM(t *testing.T) {
	t.Run("rpm", func(t *testing.T) {
		f, _, _, _, _ := newConfiguredWebToolsFixture(t)
		if _, err := f.store.UpdateTeam(context.Background(), f.teamID, store.UpdateTeamPatch{RPMLimit: ptr(int64(10)), TPMLimit: ptr(int64(1))}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.SaveTeamPolicy(context.Background(), store.TeamPolicyRecord{TeamID: f.teamID, RPMLimit: 2, TPMLimit: 1}); err != nil {
			t.Fatal(err)
		}
		f.setHandler(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"results":[{}]}`)) })
		for i := 0; i < 2; i++ {
			if r := f.do("POST", deepSeekSearchPath, f.token, `{"query":"rpm"}`); r.Code != 200 {
				t.Fatalf("tool consumed TPM: %d %s", r.Code, r.Body.String())
			}
		}
		r := f.do("POST", deepSeekFetchPath, f.token, `{"url":"https://example.test"}`)
		if r.Code != 429 || r.Header().Get("retry-after") != "60" || !strings.Contains(r.Body.String(), `"code":"rate_limit_error"`) || len(f.usageRecords()) != 2 {
			t.Fatalf("effective RPM ignored: %d %s", r.Code, r.Body.String())
		}
	})
	t.Run("concurrency", func(t *testing.T) {
		f, _, _, _, _ := newConfiguredWebToolsFixture(t)
		if _, err := f.store.UpdateTeam(context.Background(), f.teamID, store.UpdateTeamPatch{ConcurrencyLimit: ptr(int64(10))}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.SaveTeamPolicy(context.Background(), store.TeamPolicyRecord{TeamID: f.teamID, ConcurrencyLimit: 1}); err != nil {
			t.Fatal(err)
		}
		started, release := make(chan struct{}, 1), make(chan struct{})
		releaseAll := sync.OnceFunc(func() { close(release) })
		defer releaseAll()
		f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
			started <- struct{}{}
			<-release
			_, _ = w.Write([]byte(`{"results":[{}]}`))
		})
		done := make(chan int, 1)
		go func() { done <- f.do("POST", deepSeekSearchPath, f.token, `{"query":"in flight"}`).Code }()
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("first tool did not start")
		}
		r := f.do("POST", deepSeekFetchPath, f.token, `{"url":"https://example.test"}`)
		if r.Code != 429 || r.Header().Get("retry-after") != "1" {
			t.Fatalf("effective concurrency ignored: %d %s", r.Code, r.Body.String())
		}
		releaseAll()
		if code := <-done; code != 200 {
			t.Fatalf("inflight tool failed: %d", code)
		}
	})
}

func TestDeepSeekToolsSettlementFallbackRetainsFrozenCost(t *testing.T) {
	f, _, _, config, deps := newConfiguredWebToolsFixture(t)
	fallback := &recordingSettlementFallback{}
	deps.Store = &failingSettleStore{PlatformStore: f.store}
	deps.SettlementFallback = fallback
	f.engine = New(deps)
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"results":[{}]}`)) })
	r := f.do("POST", deepSeekSearchPath, f.token, `{"query":"fallback"}`)
	if r.Code != 200 {
		t.Fatalf("durable fallback failed: %d %s", r.Code, r.Body.String())
	}
	fallback.mu.Lock()
	defer fallback.mu.Unlock()
	if len(fallback.records) != 1 || fallback.records[0].CreditsMicro != 7_000 || fallback.records[0].PriceSnapshot.Cost.PricingVersion != config.Version || fallback.records[0].PriceSnapshot.Cost.EstimatedMicro == nil || *fallback.records[0].PriceSnapshot.Cost.EstimatedMicro != 3_000 {
		t.Fatalf("fallback lost frozen record: %+v", fallback.records)
	}
}

func TestDeepSeekToolsMalformedUpstreamKeepsUnknownCost(t *testing.T) {
	f, _, _, _, _ := newConfiguredWebToolsFixture(t)
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"results":"invalid"}`)) })
	r := f.do("POST", deepSeekSearchPath, f.token, `{"query":"malformed"}`)
	rows := f.usageRecords()
	if r.Code != 502 || len(rows) != 1 || rows[0].CreditsMicro != 0 || rows[0].PriceSnapshot.Cost.Status != "unknown" || rows[0].PriceSnapshot.Cost.EstimatedMicro != nil {
		t.Fatalf("invalid response invented cost: %d %+v", r.Code, rows)
	}
	var response map[string]any
	if json.Unmarshal(r.Body.Bytes(), &response) != nil || response["error"].(map[string]any)["code"] != "upstream_error" {
		t.Fatalf("unstable tool error: %s", r.Body.String())
	}
}

func TestDeepSeekFetchMissingOrInvalidContentKeepsUnknownCost(t *testing.T) {
	for _, body := range []string{`{}`, `{"title":"missing body"}`, `{"content":null}`, `{"content":42}`, `null`} {
		t.Run(body, func(t *testing.T) {
			f, _, _, _, _ := newConfiguredWebToolsFixture(t)
			f.setHandler(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
			r := f.do("POST", deepSeekFetchPath, f.token, `{"url":"https://example.test"}`)
			rows := f.usageRecords()
			if r.Code != 502 || len(rows) != 1 || rows[0].CreditsMicro != 0 || rows[0].PriceSnapshot.Cost.Status != "unknown" || rows[0].PriceSnapshot.Cost.EstimatedMicro != nil || rows[0].PriceSnapshot.Cost.Reason != "tool_outcome_unknown" {
				t.Fatalf("invalid content invented zero supplier cost: %d %+v", r.Code, rows)
			}
		})
	}
}
