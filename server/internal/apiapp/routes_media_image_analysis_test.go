package apiapp

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/media"
)

func TestImageAnalysisRoutePersistsSeparateObservation(t *testing.T) {
	f, token, p := analysisConfigFixture(t)
	raw, err := os.ReadFile("../media/testdata/video-staged-images.json")
	if err != nil {
		t.Fatal(err)
	}
	var wire media.ProjectView
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	p, err = f.deps.Media.Mutate(context.Background(), p.TeamID, p.ID, p.Version, func(v *media.Project) error {
		v.Brief, v.Analysis, v.Status = wire.Brief, wire.Analysis, "analyzing"
		v.AnalysisFee, v.Plan, v.Shots = nil, nil, []media.Shot{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/api/v1/media/projects/" + p.ID
	body := map[string]any{"version": p.Version, "inputId": p.Analysis.InputID, "imageAnalysis": wire.ImageAnalysis}
	unauthenticated := sourceRequest(f, "", "POST", prefix+"/image-analysis", body)
	if unauthenticated.Code != 401 {
		t.Fatal("image analysis missing auth", unauthenticated.Code)
	}
	response := sourceRequest(f, token, "POST", prefix+"/image-analysis", body)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	data := dataOf(t, response)
	if data["status"] != "image_review" || data["imageAnalysis"] == nil || data["plan"] != nil {
		t.Fatal("route lost separate observation")
	}
	replay := sourceRequest(f, token, "POST", prefix+"/image-analysis", body)
	if replay.Code != 200 || dataOf(t, replay)["version"] != data["version"] {
		t.Fatal("response loss not idempotent")
	}
	get := sourceRequest(f, token, "GET", prefix, nil)
	if get.Code != 200 || dataOf(t, get)["imageAnalysis"] == nil {
		t.Fatal("reload lost observation")
	}
	planning := sourceRequest(f, token, "POST", prefix+"/analysis", map[string]any{"version": data["version"], "analysis": media.Analysis{Kind: "planning", TaskID: p.Analysis.TaskID, InputID: "planning-input", WorkspacePath: p.Analysis.WorkspacePath}})
	if planning.Code != 200 || dataOf(t, planning)["imageAnalysis"] == nil {
		t.Fatal("planning lost observation", planning.Code, planning.Body.String())
	}
}
