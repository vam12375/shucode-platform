package apiapp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/media"
)

func TestNarrationReconciliationUsesExistingRootBoundaryAndVersionCAS(t *testing.T) {
	f := newTenantFixture(t)
	ctx := context.Background()
	p := &media.Project{ID: uuid.NewString(), TeamID: f.team.ID, UserID: f.member.ID, Version: 1, Status: "failed", Brief: media.Brief{Mode: "auto", Product: "fixture", Finishing: &media.FinishingOptions{Voiceover: true, Subtitles: "none"}}, Shots: []media.Shot{}, Narration: &media.NarrationState{Status: "unknown", RequestID: uuid.NewString(), Signature: "fixture", Text: "fixture narration"}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := f.deps.Media.Repo.Save(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.deps.Media.Repo.Index(ctx, media.ProjectKey(p.TeamID, p.ID)); err != nil {
		t.Fatal(err)
	}
	path := "/console/api/media/projects/" + p.TeamID + "/" + p.ID + "/reconcile"
	body := `{"version":1,"shot":-1,"outcome":"narration_not_accepted","value":"","reason":"已核实供应商没有受理"}`
	for _, cookie := range []string{f.adminCookie, f.memberCookie, f.otherCookie} {
		if response := f.do("POST", path, cookie, body); response.Code != 403 {
			t.Fatalf("non-root recovered narration: %d %s", response.Code, response.Body.String())
		}
	}
	list := f.do("GET", "/console/api/media/projects", f.rootCookie, "")
	if list.Code != 200 || !strings.Contains(list.Body.String(), p.Narration.RequestID) {
		t.Fatalf("unknown narration missing from root list: %d %s", list.Code, list.Body.String())
	}
	response := f.do("POST", path, f.rootCookie, body)
	if response.Code != 200 {
		t.Fatalf("root recovery: %d %s", response.Code, response.Body.String())
	}
	next, err := f.deps.Media.Repo.Get(ctx, p.TeamID, p.ID)
	if err != nil || next.Narration.Status != "failed" || next.Status != "failed" {
		t.Fatalf("state=%+v err=%v", next, err)
	}
	if response = f.do("POST", path, f.rootCookie, body); response.Code != 409 {
		t.Fatalf("stale recovery=%d %s", response.Code, response.Body.String())
	}
	if response = f.do("POST", path, f.rootCookie, fmt.Sprintf(`{"version":%d,"shot":-1,"outcome":"narration_result","value":"file:///tmp/fixture.wav","reason":"已核实供应商音频结果"}`, next.Version)); response.Code != 400 {
		t.Fatalf("local file import=%d %s", response.Code, response.Body.String())
	}
}
