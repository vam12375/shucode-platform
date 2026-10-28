package gwapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestDeepSeekSearchBillsOnlyNonEmptyResults(t *testing.T) {
	f := newGatewayFixture(t, func(env *Env, f *gatewayFixture) {
		env.DeepSeekToolsBaseURL = f.upstream.URL
		env.DeepSeekToolsAPIKey = "deepseek-secret"
		env.DeepSeekSearchFeeMicro = 5_000
		env.DeepSeekFetchFeeMicro = 5_000
	})
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != deepSeekSearchPath || r.Header.Get("authorization") != "Bearer deepseek-secret" {
			t.Fatalf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"results":[{"title":"result","url":"https://example.test"}]}`))
	})
	rec := f.do("POST", deepSeekSearchPath, f.token, `{"query":"DeepSeek","count":50}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"results"`) {
		t.Fatalf("search response: %d %s", rec.Code, rec.Body.String())
	}
	rows := f.usageRecords()
	if len(rows) != 1 || rows[0].Model != deepSeekSearchModel || rows[0].CreditsMicro != 5_000 {
		t.Fatalf("search usage: %+v", rows)
	}
	if team := f.team(); team.UsedCredits != "0.005000" {
		t.Fatalf("search charge: %+v", team)
	}
	if snapshot := rows[0].PriceSnapshot; snapshot == nil || snapshot.Sale.Unit != "request" || snapshot.Sale.PerRequest != "0.005000" || snapshot.Cost.EstimatedMicro == nil || *snapshot.Cost.EstimatedMicro != 5_000 {
		t.Fatalf("request price snapshot missing: %+v", snapshot)
	}

	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[]}`))
	})
	noResult := f.do("POST", deepSeekSearchPath, f.token, `{"query":"no result"}`)
	rows = f.usageRecords()
	if noResult.Code != 200 || len(rows) != 2 || rows[0].CreditsMicro != 0 || rows[0].Status != store.UsageStatusError || f.team().UsedCredits != "0.005000" {
		t.Fatalf("empty search must not charge: status=%d usage=%d team=%s", noResult.Code, len(f.usageRecords()), f.team().UsedCredits)
	}
}

func TestDeepSeekFetchBillsContentAndReleasesOnFailure(t *testing.T) {
	f := newGatewayFixture(t, func(env *Env, f *gatewayFixture) {
		env.DeepSeekToolsBaseURL = f.upstream.URL
		env.DeepSeekToolsAPIKey = "deepseek-secret"
		env.DeepSeekSearchFeeMicro = 5_000
		env.DeepSeekFetchFeeMicro = 5_000
	})
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != deepSeekFetchPath {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"title":"Article","content":"# Body"}`))
	})
	rec := f.do("POST", deepSeekFetchPath, f.token, `{"url":"https://example.com/article"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"content":"# Body"`) {
		t.Fatalf("fetch response: %d %s", rec.Code, rec.Body.String())
	}
	rows := f.usageRecords()
	if len(rows) != 1 || rows[0].Model != deepSeekFetchModel || rows[0].CreditsMicro != 5_000 || f.team().UsedCredits != "0.005000" {
		t.Fatalf("fetch usage: rows=%+v team=%+v", rows, f.team())
	}

	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found"}}`))
	})
	failed := f.do("POST", deepSeekFetchPath, f.token, `{"url":"https://example.com/missing"}`)
	rows = f.usageRecords()
	if failed.Code != 404 || len(rows) != 2 || rows[0].CreditsMicro != 0 || rows[0].Status != store.UsageStatusError || f.team().UsedCredits != "0.005000" {
		t.Fatalf("failed fetch must not charge: status=%d usage=%d team=%s", failed.Code, len(f.usageRecords()), f.team().UsedCredits)
	}
}

func TestDeepSeekToolsValidateRequestBeforeAdmission(t *testing.T) {
	f := newGatewayFixture(t, func(env *Env, f *gatewayFixture) {
		env.DeepSeekToolsBaseURL = f.upstream.URL
		env.DeepSeekToolsAPIKey = "deepseek-secret"
		env.DeepSeekSearchFeeMicro = 5_000
		env.DeepSeekFetchFeeMicro = 5_000
	})
	for _, tc := range []struct {
		path string
		body string
	}{
		{deepSeekSearchPath, `{"query":""}`},
		{deepSeekSearchPath, `{"query":"ok","count":51}`},
		{deepSeekFetchPath, `{"url":"file:///tmp/secret"}`},
		{deepSeekFetchPath, `{"url":"https://user:password@example.com"}`},
	} {
		rec := f.do("POST", tc.path, f.token, tc.body)
		if rec.Code != 400 {
			t.Errorf("%s %s: status=%d body=%s", tc.path, tc.body, rec.Code, rec.Body.String())
		}
	}
	if rows, err := f.store.ListUsage(context.Background(), store.UsageFilter{TeamID: f.teamID}); err != nil || len(rows) != 0 {
		t.Fatalf("invalid requests must not create usage: %d %v", len(rows), err)
	}
}

func TestDeepSeekToolsNeverBillOtherSuccessStatuses(t *testing.T) {
	f := newGatewayFixture(t, func(env *Env, f *gatewayFixture) {
		env.DeepSeekToolsBaseURL = f.upstream.URL
		env.DeepSeekToolsAPIKey = "deepseek-secret"
		env.DeepSeekSearchFeeMicro = 5_000
	})
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"results":[{"title":"result"}]}`))
	})
	rec := f.do("POST", deepSeekSearchPath, f.token, `{"query":"DeepSeek"}`)
	rows := f.usageRecords()
	if rec.Code != http.StatusBadGateway || len(rows) != 1 || rows[0].CreditsMicro != 0 || f.team().UsedCredits != "0.000000" {
		t.Fatalf("only HTTP 200 is billable: status=%d rows=%+v used=%s", rec.Code, rows, f.team().UsedCredits)
	}
}

func TestDeepSeekToolsDuplicateRequestIDDoesNotCallOrBillTwice(t *testing.T) {
	f := newGatewayFixture(t, func(env *Env, f *gatewayFixture) {
		env.DeepSeekToolsBaseURL = f.upstream.URL
		env.DeepSeekToolsAPIKey = "deepseek-secret"
		env.DeepSeekSearchFeeMicro = 5_000
	})
	calls := 0
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"results":[{"title":"result"}]}`))
	})
	for i, expected := range []int{http.StatusOK, http.StatusConflict} {
		req := httptest.NewRequest(http.MethodPost, deepSeekSearchPath, strings.NewReader(`{"query":"DeepSeek"}`))
		req.Header.Set("authorization", "Bearer "+f.token)
		req.Header.Set("x-request-id", "same-tool-request")
		response := httptest.NewRecorder()
		f.engine.ServeHTTP(response, req)
		if response.Code != expected {
			t.Fatalf("attempt %d: status=%d body=%s", i, response.Code, response.Body.String())
		}
	}
	if calls != 1 || len(f.usageRecords()) != 1 || f.team().UsedCredits != "0.005000" {
		t.Fatalf("duplicate changed upstream/usage: calls=%d rows=%d used=%s", calls, len(f.usageRecords()), f.team().UsedCredits)
	}
}

func TestDeepSeekFetchBlankBodyAndTimeoutAreFree(t *testing.T) {
	for _, mode := range []string{"blank", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			f := newGatewayFixture(t, func(env *Env, f *gatewayFixture) {
				env.DeepSeekToolsBaseURL = f.upstream.URL
				env.DeepSeekToolsAPIKey = "deepseek-secret"
				env.DeepSeekFetchFeeMicro = 5_000
				env.UpstreamTimeoutMs = 20
			})
			f.setHandler(func(w http.ResponseWriter, r *http.Request) {
				if mode == "timeout" {
					select {
					case <-r.Context().Done():
					case <-time.After(time.Second):
					}
					return
				}
				_, _ = w.Write([]byte(`{"title":"no content","content":"   "}`))
			})
			response := f.do("POST", deepSeekFetchPath, f.token, `{"url":"https://example.com/article"}`)
			rows := f.usageRecords()
			if len(rows) != 1 || rows[0].CreditsMicro != 0 || rows[0].PriceSnapshot.Cost.EstimatedMicro == nil || *rows[0].PriceSnapshot.Cost.EstimatedMicro != 0 || f.team().UsedCredits != "0.000000" {
				t.Fatalf("%s must be free: status=%d rows=%+v team=%s", mode, response.Code, rows, f.team().UsedCredits)
			}
		})
	}
}
