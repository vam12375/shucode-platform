package apiapp

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnalysisFailureRouteSafeCodeAndLegacyBody(t *testing.T) {
	for _, kind := range []string{"safe-code", "legacy", "stale-input", "unknown-code", "missing-input", "missing-code"} {
		t.Run(kind, func(t *testing.T) {
			f, token, p := analysisConfigFixture(t)
			body := fmt.Sprintf(`{"version":%d,"inputId":%q,"error":"video_analysis_scope_budget_exhausted"}`, p.Version, p.Analysis.InputID)
			expected := 200
			switch kind {
			case "legacy":
				body = fmt.Sprintf(`{"version":%d}`, p.Version)
			case "stale-input":
				body = fmt.Sprintf(`{"version":%d,"inputId":"old","error":"video_analysis_scope_invalid"}`, p.Version)
				expected = 409
			case "unknown-code":
				body = fmt.Sprintf(`{"version":%d,"inputId":%q,"error":"raw provider data"}`, p.Version, p.Analysis.InputID)
				expected = 400
			case "missing-input":
				body = fmt.Sprintf(`{"version":%d,"error":"analysis_failed"}`, p.Version)
				expected = 400
			case "missing-code":
				body = fmt.Sprintf(`{"version":%d,"inputId":%q}`, p.Version, p.Analysis.InputID)
				expected = 400
			}
			req := httptest.NewRequest("POST", "/api/v1/media/projects/"+p.ID+"/analysis-failed", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			f.engine.ServeHTTP(w, req)
			if w.Code != expected {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			current, _ := f.deps.Media.Repo.Get(context.Background(), p.TeamID, p.ID)
			if expected == 200 {
				code := "video_analysis_scope_budget_exhausted"
				if kind == "legacy" {
					code = "analysis_failed"
				}
				if current.Error != code || current.Status != "analysis_failed" {
					t.Fatalf("lost failure code: %+v", current)
				}
			} else if current.Status != "analyzing" || current.Version != p.Version {
				t.Fatal("rejection changed current analysis")
			}
		})
	}
}
