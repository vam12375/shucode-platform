package apiapp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/media"
)

func TestMediaFailureDiagnosticsRequireRootAndFilter(t *testing.T) {
	f := newTenantFixture(t)
	ctx := context.Background()
	repo := media.Repository{Store: f.store}
	for _, kind := range []string{"image", "video"} {
		p := &media.Project{ID: "failed-" + kind, TeamID: f.team.ID, Status: "failed", UpdatedAt: time.Now(), Canvas: &media.CanvasRequest{Kind: kind, ModelID: kind + "-model"}, Shots: []media.Shot{{Status: "failed", Attempt: 1, Error: "provider_failed", ProviderError: &media.ProviderError{Code: "RateLimitExceeded"}}}, ProviderDiagnostics: []media.ProviderDiagnostic{{Kind: kind, Message: "root diagnostic", HTTPStatus: 429}}}
		if err := repo.Save(ctx, p, nil); err != nil {
			t.Fatal(err)
		}
		if err := repo.Index(ctx, media.ProjectKey(p.TeamID, p.ID)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/console/api/ops/media-failures", "/console/api/ops/media-failures/" + f.team.ID + "/failed-video"} {
		for _, cookie := range []string{f.adminCookie, f.memberCookie, f.unassignedCookie} {
			if rec := f.do("GET", path, cookie, ""); rec.Code != 403 || strings.Contains(rec.Body.String(), "root diagnostic") {
				t.Fatalf("non-root diagnostics: %d %s", rec.Code, rec.Body.String())
			}
		}
	}
	rec := f.do("GET", "/console/api/ops/media-failures?kind=image&category=rate_limit&limit=1", f.rootCookie, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "failed-image") || strings.Contains(rec.Body.String(), "failed-video") || strings.Contains(rec.Body.String(), "root diagnostic") {
		t.Fatalf("filtered list: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.do("GET", "/console/api/ops/media-failures/"+f.team.ID+"/failed-video", f.rootCookie, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "root diagnostic") {
		t.Fatalf("root diagnostic missing: %d %s", rec.Code, rec.Body.String())
	}

	recovered, err := repo.Get(ctx, f.team.ID, "failed-image")
	if err != nil {
		t.Fatal(err)
	}
	previous := *recovered
	recovered.ProviderDiagnostics = append([]media.ProviderDiagnostic(nil), recovered.ProviderDiagnostics...)
	recovered.Status = "completed"
	recovered.Shots = []media.Shot{{Status: "succeeded"}}
	recovered.ProviderDiagnostics[0].Code = "RateLimitExceeded"
	if err := repo.Save(ctx, recovered, &previous); err != nil {
		t.Fatal(err)
	}
	rec = f.do("GET", "/console/api/ops/media-failures?status=completed&category=rate_limit", f.rootCookie, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "failed-image") {
		t.Fatalf("recovered diagnostic missing: %d %s", rec.Code, rec.Body.String())
	}
	for _, query := range []string{"kind=other", "category=other", "hours=-1", "limit=0", "offset=-1"} {
		if rec := f.do("GET", "/console/api/ops/media-failures?"+query, f.rootCookie, ""); rec.Code != 400 {
			t.Fatalf("invalid filter accepted %s: %d", query, rec.Code)
		}
	}
}
