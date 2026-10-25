package apiapp

import (
	"net/http"
	"testing"
)

func TestProviderModelDiscoveryForwardsExplicitType(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	f.upstreamFn = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxy/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("type") == "video" {
			_, _ = w.Write([]byte(`{"data":[{"id":"seedance-2-mini"},{"id":"seedance-2"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"text-fixture"}]}`))
	}
	created := f.do("POST", "/console/api/providers", cookie,
		`{"name":"ToAPIs fixture","baseUrl":"`+f.upstream.URL+`/proxy/v1","apiKey":"fixture-key","protocol":"anthropic-messages"}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	providerID := dataOf(t, created)["provider"].(map[string]any)["id"].(string)
	path := "/console/api/providers/" + providerID + "/models"
	for _, query := range []string{"?type=video&adapter=toapis", "?base=prefix&type=video&adapter=toapis"} {
		f.upstreamMu.Lock()
		before := len(f.upstreamHits)
		f.upstreamMu.Unlock()
		response := f.do("GET", path+query, cookie, "")
		if response.Code != http.StatusOK {
			t.Fatalf("discovery: %d %s", response.Code, response.Body.String())
		}
		models := dataOf(t, response)["models"].([]any)
		if len(models) != 2 || models[0] != "seedance-2" || models[1] != "seedance-2-mini" {
			t.Fatalf("video catalog: %v", models)
		}
		hit := f.lastUpstreamHit()
		if hit.url != "/proxy/v1/models?type=video" || hit.method != "GET" || hit.headers.Get("authorization") != "Bearer fixture-key" || hit.headers.Get("x-api-key") != "" {
			t.Fatalf("discovery request: %+v", hit)
		}
		f.upstreamMu.Lock()
		count := len(f.upstreamHits) - before
		f.upstreamMu.Unlock()
		if count != 1 {
			t.Fatalf("ToAPIs discovery must only request its versioned API, requests=%d", count)
		}
	}
	response := f.do("GET", path, cookie, "")
	models := dataOf(t, response)["models"].([]any)
	if len(models) != 1 || models[0] != "text-fixture" || f.lastUpstreamHit().url != "/proxy/v1/models" {
		t.Fatalf("default discovery changed: %s", response.Body.String())
	}
	if f.lastUpstreamHit().headers.Get("x-api-key") != "fixture-key" || f.lastUpstreamHit().headers.Get("authorization") != "" {
		t.Fatal("default discovery must retain the text protocol authentication")
	}

	f.upstreamMu.Lock()
	before := len(f.upstreamHits)
	f.upstreamMu.Unlock()
	for _, query := range []string{"?type=unknown", "?type=video%26other%3Dvalue", "?type=video&type=text", "?type=video&adapter=unknown", "?type=video&adapter=toapis&adapter=ark"} {
		response = f.do("GET", path+query, cookie, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid type accepted: %s %d", query, response.Code)
		}
	}
	f.upstreamMu.Lock()
	after := len(f.upstreamHits)
	f.upstreamMu.Unlock()
	if before != after {
		t.Fatal("invalid discovery types must not contact upstream")
	}
}
