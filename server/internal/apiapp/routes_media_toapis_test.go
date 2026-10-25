package apiapp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/media"
)

func TestToAPIsMediaFactoryUsesEncryptedProviderAndNormalizedPrefixes(t *testing.T) {
	for _, prefix := range []string{"", "/v1", "/proxy/v1"} {
		t.Run(prefix, func(t *testing.T) {
			f := newTenantFixture(t)
			f.deps.Env.AllowInsecureUpstreamURLs = true
			body, _ := json.Marshal(map[string]any{"name": "ToAPIs fixture", "baseUrl": f.upstream.URL + prefix, "apiKey": "sk-toapis-fixture", "enabled": true})
			rec := f.do("POST", "/console/api/providers", f.rootCookie, string(body))
			if rec.Code != 200 {
				t.Fatalf("create provider: %d %s", rec.Code, rec.Body.String())
			}
			providerID := dataOf(t, rec)["provider"].(map[string]any)["id"].(string)
			stored, err := f.store.GetUpstreamProvider(context.Background(), providerID)
			if err != nil || stored == nil || stored.APIKeyCiphertext == "" || strings.Contains(stored.APIKeyCiphertext, "sk-toapis-fixture") {
				t.Fatal("provider key was not encrypted")
			}
			wantPrefix := "/v1"
			if prefix == "/proxy/v1" {
				wantPrefix = "/proxy/v1"
			}
			f.upstreamFn = func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer sk-toapis-fixture" {
					t.Error("factory did not resolve the encrypted key")
				}
				if r.Method == "POST" {
					if r.URL.Path != wantPrefix+"/videos/generations" {
						t.Errorf("submit prefix %s", r.URL.Path)
					}
					_, _ = w.Write([]byte(`{"id":"tsk_vid_fixture","object":"generation.task","status":"queued"}`))
					return
				}
				if r.URL.Path != wantPrefix+"/videos/generations/tsk_vid_fixture" {
					t.Errorf("query prefix %s", r.URL.Path)
				}
				_, _ = w.Write([]byte(`{"id":"tsk_vid_fixture","object":"generation.task","status":"in_progress"}`))
			}
			m := media.Model{ProviderID: providerID, Adapter: "toapis", Kind: "video", UpstreamModel: "seedance-2-mini", Resolutions: []string{"720p"}, MinDuration: 4, MaxDuration: 15}
			p, err := NewMediaService(f.deps).Provider(context.Background(), m)
			if err != nil || p.Adapter != "toapis" {
				t.Fatalf("factory adapter=%s err=%v", p.Adapter, err)
			}
			id, err := p.CanvasVideo(context.Background(), m, media.CanvasRequest{Prompt: "fixture", Duration: 5, Resolution: "720p", AspectRatio: "16:9"}, nil)
			if err != nil || id != "tsk_vid_fixture" {
				t.Fatalf("submit id=%s err=%v", id, err)
			}
			result, err := p.Query(context.Background(), id)
			if err != nil || result.Status != "running" {
				t.Fatalf("query=%+v err=%v", result, err)
			}
		})
	}
}
