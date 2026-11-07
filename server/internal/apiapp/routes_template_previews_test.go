package apiapp

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTemplatePreviewUploadAndImmutablePublicCache(t *testing.T) {
	f := newAPIFixture(t)
	f.deps.Media.Blobs.Root = t.TempDir()
	cookie := f.loginAdmin()
	raw, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jX1kAAAAASUVORK5CYII=")
	path := "/console/api/template-library/previews"
	if response := f.do("POST", path, "", string(raw)); response.Code != 401 {
		t.Fatalf("unauthenticated upload %d", response.Code)
	}
	if response := f.do("POST", path, cookie, "<svg></svg>"); response.Code != 400 {
		t.Fatal("active content accepted")
	}
	response := f.do("POST", path, cookie, string(raw))
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	name := dataOf(t, response)["name"].(string)
	second := f.do("POST", path, cookie, string(raw))
	if dataOf(t, second)["name"] != name {
		t.Fatal("same bytes changed immutable identity")
	}
	url := "/api/v1/media/template-assets/" + name
	get := f.do("GET", url, "", "")
	if get.Code != 200 || get.Body.String() != string(raw) || get.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("public cache failed: %d %+v", get.Code, get.Header())
	}
	head := f.do("HEAD", url, "", "")
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("ETag") != get.Header().Get("ETag") {
		t.Fatal("HEAD changed identity")
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=0-7")
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, req)
	if w.Code != 206 || w.Body.Len() != 8 {
		t.Fatalf("range failed: %d", w.Code)
	}
	list := f.do("GET", "/console/api/template-library", cookie, "")
	for _, value := range dataOf(t, list)["entries"].([]any) {
		e := value.(map[string]any)
		if e["kind"] != "video" {
			continue
		}
		body, _ := json.Marshal(map[string]any{"expectedVersion": e["expectedVersion"], "preview": map[string]string{"cover": name, "video": ""}})
		updated := f.do("PUT", "/console/api/template-library/"+e["id"].(string), cookie, string(body))
		if updated.Code != 200 {
			t.Fatal(updated.Body.String())
		}
		break
	}
	if bad := f.do("GET", "/api/v1/media/template-assets/"+strings.Repeat("0", 64)+".png", "", ""); bad.Code != 404 || bad.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing public asset cached")
	}
}
