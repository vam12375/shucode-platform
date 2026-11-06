package apiapp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLibraryPermissionsAndPublishing(t *testing.T) {
	f := newTenantFixture(t)
	for _, cookie := range []string{f.adminCookie, f.memberCookie, f.otherCookie, f.unassignedCookie} {
		for _, method := range []string{"GET", "POST"} {
			if response := f.do(method, "/console/api/template-library", cookie, `{}`); response.Code != 403 {
				t.Fatalf("%s unauthorized: %d", method, response.Code)
			}
		}
		for _, method := range []string{"PUT", "DELETE"} {
			if response := f.do(method, "/console/api/template-library/custom:fake", cookie, `{}`); response.Code != 403 {
				t.Fatal("non-root mutated library")
			}
		}
		if response := f.do("POST", "/console/api/template-library/packages", cookie, "bad"); response.Code != 403 {
			t.Fatal("non-root uploaded package")
		}
	}
	root := newAPIFixture(t)
	cookie := root.loginAdmin()
	response := root.do("GET", "/console/api/template-library", cookie, "")
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	entries := dataOf(t, response)["entries"].([]any)
	if len(entries) != 115 {
		t.Fatalf("incomplete catalog: %d", len(entries))
	}
	var selected map[string]any
	for _, raw := range entries {
		entry := raw.(map[string]any)
		if entry["kind"] == "skill" {
			selected = entry
			break
		}
	}
	id := selected["id"].(string)
	response = root.do("GET", "/api/v1/client/skills/"+id+"/package", "", "")
	if response.Code != 200 || response.Header().Get("Content-Type") != "application/zip" || !strings.HasPrefix(response.Body.String(), "PK") {
		t.Fatalf("skill package unavailable: %d", response.Code)
	}
	body, _ := json.Marshal(map[string]any{"expectedVersion": selected["expectedVersion"], "enabled": false})
	response = root.do("PUT", "/console/api/template-library/"+id, cookie, string(body))
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if response = root.do("PUT", "/console/api/template-library/"+id, cookie, string(body)); response.Code != 409 {
		t.Fatal("stale update accepted")
	}
	if response = root.do("GET", "/api/v1/client/skills/"+id+"/package", "", ""); response.Code != 404 {
		t.Fatal("disabled package published")
	}
	response = root.do("GET", "/api/v1/client/skills", "", "")
	if response.Code != 200 || strings.Contains(response.Body.String(), id) {
		t.Fatal("disabled skill in public list")
	}
	for _, path := range []string{"/api/v1/client/scenes", "/api/v1/client/templates"} {
		response = root.do("GET", path, "", "")
		if response.Code != 200 || strings.Contains(response.Body.String(), "expectedVersion") {
			t.Fatalf("bad public catalog: %s", path)
		}
		if strings.Contains(response.Body.String(), "dineops:video-template:") {
			t.Fatalf("unreviewed legacy original in public catalog: %s", path)
		}
	}
	for _, body := range []string{`{"enabled":false}`, `{"kind":"skill","enabled":true,"fields":{"name":"No package","content":"incomplete"}}`, `{"kind":"prompt","enabled":true,"fields":{"name":"Test","content":"Test","scene":"draft-suggestion"},"reviewerId":"fake"}`, `{} {}`} {
		if response = root.do("POST", "/console/api/template-library", cookie, body); response.Code != 400 {
			t.Fatalf("invalid body accepted: %d", response.Code)
		}
	}
}

func TestLibraryBuiltinDeletion(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	response := f.do("GET", "/console/api/template-library", cookie, "")
	entries := dataOf(t, response)["entries"].([]any)
	for _, kind := range []string{"template", "skill", "prompt", "automation", "video"} {
		t.Run(kind, func(t *testing.T) {
			var selected map[string]any
			for _, raw := range entries {
				entry := raw.(map[string]any)
				if entry["kind"] == kind {
					selected = entry
					break
				}
			}
			id := selected["id"].(string)
			path := "/console/api/template-library/" + id
			if response := f.do("DELETE", path, cookie, `{"expectedVersion":"stale"}`); response.Code != 409 {
				t.Fatalf("stale deletion: %d", response.Code)
			}
			body, _ := json.Marshal(map[string]any{"expectedVersion": selected["expectedVersion"]})
			if response := f.do("DELETE", path, cookie, string(body)); response.Code != 200 {
				t.Fatalf("deletion: %d %s", response.Code, response.Body.String())
			}
			if response := f.do("DELETE", path, cookie, string(body)); response.Code != 404 {
				t.Fatalf("deleted item accepted twice: %d", response.Code)
			}
			for _, catalog := range []string{"/console/api/template-library", "/api/v1/client/skills", "/api/v1/client/templates"} {
				response := f.do("GET", catalog, cookie, "")
				if response.Code != 200 || strings.Contains(response.Body.String(), id) {
					t.Fatalf("deleted item in %s", catalog)
				}
			}
			if kind == "skill" {
				for _, download := range []string{path + "/package", "/api/v1/client/skills/" + id + "/package"} {
					if response := f.do("GET", download, cookie, ""); response.Code != 404 {
						t.Fatalf("deleted skill still downloadable: %d", response.Code)
					}
				}
			}
		})
	}
}
