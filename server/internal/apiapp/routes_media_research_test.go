package apiapp

import (
	"reflect"
	"testing"
)

func TestMediaOverviewResearchPlatformsAreExplicitAndAuthenticated(t *testing.T) {
	f, token, _ := newUserConfigPolicyFixture(t)
	response := sourceRequest(f, token, "GET", "/api/v1/media/overview", nil)
	if response.Code != 200 {
		t.Fatalf("overview=%d %s", response.Code, response.Body.String())
	}
	capabilities, ok := dataOf(t, response)["capabilities"].(map[string]any)
	if !ok || !reflect.DeepEqual(capabilities["researchPlatforms"], []any{"douyin", "xiaohongshu", "bilibili"}) || capabilities["creativeBrief"] != true || capabilities["imageReferenceMode"] != true {
		t.Fatalf("capability wire=%s", response.Body.String())
	}
	if response = sourceRequest(f, "", "GET", "/api/v1/media/overview", nil); response.Code != 401 {
		t.Fatalf("unauthenticated overview=%d", response.Code)
	}
}
