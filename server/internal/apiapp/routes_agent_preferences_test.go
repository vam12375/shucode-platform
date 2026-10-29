package apiapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func agentPreferencesRequest(f *apiFixture, token, method, etag, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, agentMediaPreferencesPath, strings.NewReader(body))
	if token != "" {
		req.Header.Set("authorization", "Bearer "+token)
	}
	if etag != "" {
		req.Header.Set("if-match", etag)
	}
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	return rec
}

func TestAgentMediaPreferencesVersionedAccountAPI(t *testing.T) {
	f, token, _ := newUserConfigPolicyFixture(t)
	get := agentPreferencesRequest(f, token, http.MethodGet, "", "")
	if get.Code != http.StatusOK || get.Header().Get("etag") != `"agent-media-0"` || !strings.Contains(get.Body.String(), `"preferences":{}`) {
		t.Fatalf("initial GET: %d %s etag=%q", get.Code, get.Body.String(), get.Header().Get("etag"))
	}
	unchangedReq := httptest.NewRequest(http.MethodGet, agentMediaPreferencesPath, nil)
	unchangedReq.Header.Set("authorization", "Bearer "+token)
	unchangedReq.Header.Set("if-none-match", `"agent-media-0"`)
	unchanged := httptest.NewRecorder()
	f.engine.ServeHTTP(unchanged, unchangedReq)
	if unchanged.Code != http.StatusNotModified {
		t.Fatalf("matching ETag should return 304: %d %s", unchanged.Code, unchanged.Body.String())
	}
	created := agentPreferencesRequest(f, token, http.MethodPut, `"agent-media-0"`, `{"preferences":{"imageModelId":"sku-2k","imageResolution":"2K","imageAspectRatio":"16:9"}}`)
	if created.Code != http.StatusOK || created.Header().Get("etag") != `"agent-media-1"` {
		t.Fatalf("conditional create: %d %s etag=%q", created.Code, created.Body.String(), created.Header().Get("etag"))
	}
	conflict := agentPreferencesRequest(f, token, http.MethodPut, `"agent-media-0"`, `{"preferences":{"imageModelId":"stale"}}`)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), `"imageModelId":"sku-2k"`) || conflict.Header().Get("etag") != `"agent-media-1"` {
		t.Fatalf("stale write should return authoritative value: %d %s", conflict.Code, conflict.Body.String())
	}
	for _, invalid := range []string{
		`{"preferences":{"prompt":"not allowed"}}`,
		`{"preferences":{"videoResolution":2}}`,
		`{"preferences":[]}`,
		`{"preferences":{},"unexpected":true}`,
	} {
		if rec := agentPreferencesRequest(f, token, http.MethodPut, `"agent-media-1"`, invalid); rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid preference must be rejected: %d %s", rec.Code, rec.Body.String())
		}
	}
	if unauthenticated := agentPreferencesRequest(f, "", http.MethodGet, "", ""); unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET: %d %s", unauthenticated.Code, unauthenticated.Body.String())
	}
	device, err := f.store.FindDeviceTokenByHash(context.Background(), crypto.HashToken(token))
	if err != nil || device == nil {
		t.Fatalf("device lookup: %v", err)
	}
	other, err := f.store.CreateUser(context.Background(), userInputForPreferencesTest())
	if err != nil {
		t.Fatal(err)
	}
	otherToken := createAgentRouteDevice(t, f, other.ID)
	otherPrefs := agentPreferencesRequest(f, otherToken, http.MethodGet, "", "")
	if otherPrefs.Code != http.StatusOK || strings.Contains(otherPrefs.Body.String(), "sku-2k") {
		t.Fatalf("preferences leaked across accounts: %d %s", otherPrefs.Code, otherPrefs.Body.String())
	}
}

func userInputForPreferencesTest() store.UserInput {
	return store.UserInput{Email: "prefs-isolation@test.local", PasswordHash: "x", DisplayName: "other", Role: store.RoleUser}
}
