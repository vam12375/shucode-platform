package apiapp

import (
	"context"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/media"
)

func TestImageReviewAnalysisRouteFreezesSelectedRecipeAndSkills(t *testing.T) {
	f, token, p := analysisConfigFixture(t)
	ctx := context.Background()
	p, err := f.deps.Media.Mutate(ctx, p.TeamID, p.ID, p.Version, func(v *media.Project) error {
		v.Brief.Mode, v.Brief.GenerationPolicy, v.Brief.ResearchPolicy = "auto", "whole-first", "disabled"
		v.Brief.ImageAnalysisFirst, v.Brief.ReviewPlan = true, false
		v.Brief.Duration, v.Brief.AspectRatio = 15, "16:9"
		v.Brief.ReferenceAssetID, v.Brief.VideoModel = "", "fixture-video"
		v.Brief.AssetIDs = []string{"fixture-image"}
		v.Status = "image_review"
		v.Analysis = &media.Analysis{Kind: "images", TaskID: "same-task", InputID: "images-input", WorkspacePath: "/fixture"}
		v.ImageAnalysis = &media.ImageAnalysis{Text: "A warm product close-up."}
		v.AnalysisFee, v.Plan, v.Shots = nil, nil, []media.Shot{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/api/v1/media/projects/" + p.ID
	selection := map[string]any{"templateId": "black-gold-reveal", "templateVersion": "1.0.0"}
	body := map[string]any{"version": p.Version, "analysis": media.Analysis{Kind: "planning", TaskID: "same-task", InputID: "planning-input", WorkspacePath: "/fixture"}, "templateSelection": selection}
	bad := sourceRequest(f, token, "POST", prefix+"/analysis", map[string]any{"version": p.Version, "analysis": body["analysis"], "templateSelection": map[string]any{"templateId": "black-gold-reveal"}})
	if bad.Code == 200 {
		t.Fatal("partial selection accepted")
	}
	response := sourceRequest(f, token, "POST", prefix+"/analysis", body)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	data := dataOf(t, response)
	brief := data["brief"].(map[string]any)
	snapshot := data["templateSnapshot"].(map[string]any)
	if data["status"] != "analyzing" || brief["templateId"] != selection["templateId"] || brief["reviewPlan"] != false || len(snapshot["skills"].([]any)) != 1 || data["imageAnalysis"] == nil {
		t.Fatal("route lost accepted template, skill or image facts")
	}
	replay := sourceRequest(f, token, "POST", prefix+"/analysis", body)
	if replay.Code != 200 || dataOf(t, replay)["version"] != data["version"] {
		t.Fatal("route replay created another binding", replay.Code, replay.Body.String())
	}
	loaded := sourceRequest(f, token, "GET", prefix, nil)
	if loaded.Code != 200 || dataOf(t, loaded)["templateSnapshot"] == nil {
		t.Fatal("reload lost the frozen production skills")
	}
}
