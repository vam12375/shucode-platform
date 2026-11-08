package apiapp

import (
	"context"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"testing"
)

func TestPlanningResultRoutePersistsCompletedOutput(t *testing.T) {
	f, token, p := analysisConfigFixture(t)
	p, err := f.deps.Media.Mutate(context.Background(), p.TeamID, p.ID, p.Version, func(v *media.Project) error {
		v.Brief.Mode, v.Brief.ReferenceAssetID, v.Brief.SourceOnly = "auto", "", false
		v.Status, v.Plan, v.Shots = "analysis_failed", nil, []media.Shot{}
		v.Analysis = &media.Analysis{Kind: "planning", TaskID: "task", InputID: "input", WorkspacePath: "/fixture"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/media/projects/" + p.ID
	body := map[string]any{"version": p.Version, "inputId": "input", "planningResult": media.PlanningResult{Text: "完整方案，暂缺生成段时长。", Issues: []media.PlanningIssue{{Path: "shots.0.duration", Code: "invalid_type"}}}}
	if r := sourceRequest(f, "", "POST", path+"/planning-result", body); r.Code != 401 {
		t.Fatal("missing auth", r.Code)
	}
	r := sourceRequest(f, token, "POST", path+"/planning-result", body)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	data := dataOf(t, r)
	if data["status"] != "analysis_ready" || data["planningResult"] == nil || data["plan"] != nil {
		t.Fatal("lost result")
	}
	replay := sourceRequest(f, token, "POST", path+"/planning-result", body)
	if replay.Code != 200 || dataOf(t, replay)["version"] != data["version"] {
		t.Fatal("non-idempotent replay")
	}
	get := sourceRequest(f, token, "GET", path, nil)
	if get.Code != 200 || dataOf(t, get)["planningResult"] == nil {
		t.Fatal("reload lost result")
	}
	body["inputId"] = "stale"
	if r := sourceRequest(f, token, "POST", path+"/planning-result", body); r.Code != 409 {
		t.Fatal("stale input", r.Code)
	}
}
