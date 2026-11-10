package apiapp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLegacyVideoPromptsRemainEditableAndPublished(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	response := f.do("GET", "/console/api/template-library", cookie, "")
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	entries := dataOf(t, response)["entries"].([]any)
	count := 0
	for _, raw := range entries {
		entry := raw.(map[string]any)
		id := entry["id"].(string)
		if !strings.HasPrefix(id, "dineops:video-template:") {
			continue
		}
		count++
		if entry["kind"] != "video" {
			t.Fatalf("wrong category: %s", id)
		}
		fields := map[string]any{}
		for _, key := range []string{"name", "nameEn", "description", "descriptionEn", "content", "contentEn", "tags", "scene", "cronExpr"} {
			fields[key] = entry[key]
		}
		fields["content"] = entry["content"].(string) + "\n管理员校对完成。"
		body, _ := json.Marshal(map[string]any{"expectedVersion": entry["expectedVersion"], "enabled": true, "fields": fields})
		response = f.do("PUT", "/console/api/template-library/"+id, cookie, string(body))
		if response.Code != 200 {
			t.Fatalf("edit failed: %s", response.Body.String())
		}
		response = f.do("GET", "/api/v1/client/templates", "", "")
		if response.Code != 200 || !strings.Contains(response.Body.String(), id) || !strings.Contains(response.Body.String(), "管理员校对完成") {
			t.Fatal("enabled video prompt not delivered")
		}
	}
	if count != 8 {
		t.Fatalf("missing originals: %d", count)
	}
}
