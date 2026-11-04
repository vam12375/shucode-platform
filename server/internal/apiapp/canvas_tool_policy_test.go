package apiapp

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCanvasToolPolicyTeamScopeValidationAndCAS(t *testing.T) {
	f := newTenantFixture(t)
	path := "/console/api/teams/" + f.team.ID + "/canvas-tool-policy"
	write := func(body, etag string) *httptest.ResponseRecorder {
		return f.doForge("PUT", path, f.adminCookie, body, map[string]string{
			"Origin":         f.deps.Env.PublicAPIOrigin,
			"Sec-Fetch-Site": "same-origin",
			"If-Match":       etag,
		})
	}

	defaultPolicy := f.do("GET", path, f.adminCookie, "")
	if defaultPolicy.Code != 200 || defaultPolicy.Header().Get("ETag") != `"canvas-tool-0"` {
		t.Fatalf("default policy response: %d %s", defaultPolicy.Code, defaultPolicy.Body.String())
	}
	data := dataOf(t, defaultPolicy)
	if data["enabled"] != false || data["requireStructureConfirmation"] != true || data["requireMediaConfirmation"] != true {
		t.Fatalf("unsafe default canvas policy: %+v", data)
	}
	if len(data["allowedOperations"].([]any)) != 0 {
		t.Fatalf("default operation allowlist must be empty: %+v", data)
	}
	if rec := f.do("GET", path, f.memberCookie, ""); rec.Code != 403 {
		t.Fatalf("ordinary member cannot read management policy: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("PUT", path, f.adminCookie, `{}`); rec.Code != 428 {
		t.Fatalf("If-Match is required: %d %s", rec.Code, rec.Body.String())
	}
	if rec := write(`{}`, `"wrong"`); rec.Code != 400 {
		t.Fatalf("invalid If-Match: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("PUT", "/console/api/teams/"+f.otherTeam.ID+"/canvas-tool-policy", f.adminCookie, `{}`); rec.Code != 403 {
		t.Fatalf("cross-team write must be denied: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("PUT", path, f.memberCookie, `{}`); rec.Code != 403 {
		t.Fatalf("ordinary member cannot write policy: %d %s", rec.Code, rec.Body.String())
	}

	invalid := `{"enabled":true,"allowedOperations":["unknown"],"maxMediaQuoteMicroPerOperation":null,"maxMediaOperationsPerDay":null,"maxConcurrentMediaOperations":null}`
	if rec := write(invalid, defaultPolicy.Header().Get("ETag")); rec.Code != 400 {
		t.Fatalf("unknown operation must be rejected: %d %s", rec.Code, rec.Body.String())
	}
	invalidConfirmation := `{"enabled":true,"allowedOperations":[],"maxMediaQuoteMicroPerOperation":null,"maxMediaOperationsPerDay":null,"maxConcurrentMediaOperations":null,"requireMediaConfirmation":false}`
	if rec := write(invalidConfirmation, defaultPolicy.Header().Get("ETag")); rec.Code != 400 {
		t.Fatalf("confirmation policy cannot be overridden: %d %s", rec.Code, rec.Body.String())
	}

	body := `{"enabled":true,"allowedOperations":["add_node","generate_media"],"maxMediaQuoteMicroPerOperation":250000,"maxMediaOperationsPerDay":12,"maxConcurrentMediaOperations":null}`
	updated := write(body, defaultPolicy.Header().Get("ETag"))
	if updated.Code != 200 || updated.Header().Get("ETag") != `"canvas-tool-1"` {
		t.Fatalf("canvas policy update: %d %s", updated.Code, updated.Body.String())
	}
	updatedData := dataOf(t, updated)
	if updatedData["enabled"] != true || updatedData["requireStructureConfirmation"] != true || updatedData["requireMediaConfirmation"] != true {
		t.Fatalf("confirmation requirements changed: %+v", updatedData)
	}
	conflict := write(body, defaultPolicy.Header().Get("ETag"))
	if conflict.Code != 409 || conflict.Header().Get("ETag") != `"canvas-tool-1"` || !strings.Contains(conflict.Body.String(), `"revision":1`) {
		t.Fatalf("stale canvas policy write must return current revision: %d %s", conflict.Code, conflict.Body.String())
	}

	if rec := f.do("PUT", "/console/api/teams/"+f.team.ID+"/policy", f.adminCookie, `{"dailyBudgetCredits":"10"}`); rec.Code != 200 {
		t.Fatalf("legacy budget policy update: %d %s", rec.Code, rec.Body.String())
	}
	retained := dataOf(t, f.do("GET", path, f.adminCookie, ""))
	if retained["revision"] != float64(1) || retained["enabled"] != true {
		t.Fatalf("legacy policy write overwrote canvas policy: %+v", retained)
	}

	audit := dataOf(t, f.do("GET", "/console/api/audit", f.adminCookie, ""))["logs"].([]any)
	found := false
	for _, raw := range audit {
		if strings.Contains(fmt.Sprint(raw), "team.canvas_tool_policy.update") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("canvas policy update must be present in team audit")
	}
}
