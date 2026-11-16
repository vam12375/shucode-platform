package media

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
)

func directBrief(count int) (Brief, []Model, []Asset) {
	m := fixtureVideoModel()
	m.GenerateAudio = true
	ids, assets := capacityAssets(count)
	return Brief{Mode: "auto", ImageAnalysisFirst: true, PromptPolicy: "direct", GenerationPolicy: "whole-first", CreativeExpansion: "related", ResearchPolicy: "disabled", ReferenceURLs: []string{}, Finishing: &FinishingOptions{Subtitles: "none", NativeAudio: true}, ReferenceMode: "multimodal", Product: "图片视频创作", AssetIDs: ids, Platforms: []string{}, VideoModel: m.ID, Resolution: "720p", AspectRatio: "16:9", Duration: 15}, []Model{m}, assets
}

// 跨仓冻结样例：客户端快速创作实际提交的直出 brief，平台必须严格解码并通过准入。
func TestDirectPromptBrowserContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/video-direct-prompt-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Brief Brief `json:"brief"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	b := fixture.Brief
	if b.PromptPolicy != "direct" || !b.ImageAnalysisFirst || b.ReviewPlan || b.ReferenceMode != "multimodal" || !b.Finishing.NativeAudio {
		t.Fatalf("browser intent changed: %+v", b)
	}
	m := fixtureVideoModel()
	m.ID, m.Resolutions, m.GenerateAudio = b.VideoModel, []string{"480p"}, true
	assets := []Asset{{ID: "E2E_IMAGE_1", Kind: "image"}, {ID: "E2E_IMAGE_2", Kind: "image"}}
	if err = ValidateBrief(b, []Model{m}, assets); err != nil {
		t.Fatal(err)
	}
}

func TestDirectPromptBriefAdmission(t *testing.T) {
	ineligible := "video_direct_prompt_ineligible"
	// fixtureVideoModel 每段最多 9 张、最长 15 秒：9 张一段可直出，10 张需要分段。
	b, models, assets := directBrief(9)
	if err := ValidateBrief(b, models, assets); err != nil {
		t.Fatalf("single segment rejected: %v", err)
	}
	b, models, assets = directBrief(10)
	if err := ValidateBrief(b, models, assets); err == nil || err.Error() != ineligible {
		t.Fatalf("multi-segment direct err=%v", err)
	}
	for name, mutate := range map[string]func(*Brief){
		"frames":          func(b *Brief) { b.ReferenceMode = "frames" },
		"subtitles":       func(b *Brief) { b.Finishing.Subtitles = "short" },
		"template":        func(b *Brief) { b.TemplateID, b.TemplateVersion = "t", "1" },
		"variants":        func(b *Brief) { b.VariantCount = 2 },
		"staged-pipeline": func(b *Brief) { b.ImageAnalysisFirst = false },
	} {
		t.Run(name, func(t *testing.T) {
			b, models, assets := directBrief(2)
			b.Finishing = &FinishingOptions{Subtitles: "none", NativeAudio: true}
			mutate(&b)
			if err := ValidateBrief(b, models, assets); err == nil || err.Error() != ineligible {
				t.Fatalf("err=%v", err)
			}
		})
	}
	b, models, assets = directBrief(2)
	b.PromptPolicy = "fast"
	if err := ValidateBrief(b, models, assets); err != ErrInvalid {
		t.Fatalf("unknown policy err=%v", err)
	}
	for _, policy := range []string{"", "staged"} {
		b, models, assets = directBrief(10)
		b.PromptPolicy = policy
		if err := ValidateBrief(b, models, assets); err != nil {
			t.Fatalf("staged brief %q affected: %v", policy, err)
		}
	}
}

func directProject(t *testing.T, f *fixture, mutate func(*Brief)) *Project {
	t.Helper()
	p := f.project(t)
	b, _, models := wholeDurationFixture([]int{15}, 15)
	b.ImageAnalysisFirst, b.PromptPolicy = true, "direct"
	if mutate != nil {
		mutate(&b)
	}
	p, err := f.s.Mutate(context.Background(), f.team, p.ID, p.Version, func(v *Project) error {
		v.Brief, v.Models, v.Status = b, models, "draft"
		v.Plan, v.Shots, v.Analysis, v.QuoteMicro = nil, []Shot{}, nil, 0
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDirectPromptSkipsImageReviewAndQueuesPlan(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := directProject(t, f, nil)
	_, plan, _ := wholeDurationFixture([]int{15}, 15)
	if _, err := f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{Kind: "images", TaskID: "task", InputID: "images", WorkspacePath: "/fixture"}); err == nil {
		t.Fatal("direct bound the staged images phase")
	}
	a := Analysis{Kind: "direct", TaskID: "task", InputID: "direct-input", WorkspacePath: "/fixture"}
	p, err := f.s.Bind(ctx, f.team, p.ID, p.Version, a)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "analyzing" || p.Analysis.Kind != "direct" {
		t.Fatalf("direct binding: %+v", p.Analysis)
	}
	if _, err = f.s.SetImageAnalysis(ctx, f.team, p.ID, p.Version, a.InputID, ImageAnalysis{Text: "白色杯子"}); err == nil {
		t.Fatal("direct entered image review")
	}
	p, err = f.s.SetPlanningResult(ctx, f.team, p.ID, p.Version, a.InputID, PlanningResult{Text: "素材主题：杯子\n【视频提示词】\n暖调晨光推近杯子。", Issues: []PlanningIssue{}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "analysis_ready" || p.ImageAnalysis != nil {
		t.Fatalf("planning result not saved: %s", p.Status)
	}
	hash, _ := candidatePlanHash(plan)
	p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.PlanReview = &PlanReview{InputID: a.InputID, CandidateHash: hash, Status: "passed", Issues: []string{}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.acceptAnalysisPlan(ctx, f.team, p.ID, p.Version, a.InputID, plan)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "queued" || p.Analysis.Kind != "direct" || p.ImageAnalysis != nil || p.Plan == nil {
		t.Fatalf("direct plan not queued: %s %+v", p.Status, p.Analysis)
	}
	if f.submits.Load() != 0 {
		t.Fatal("acceptance directly submitted provider work")
	}
}

func TestDirectPromptRetryAndStagedIsolation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := directProject(t, f, nil)
	a := Analysis{Kind: "direct", TaskID: "task", InputID: "first", WorkspacePath: "/fixture"}
	p, err := f.s.Bind(ctx, f.team, p.ID, p.Version, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Status = "analysis_failed"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, wrong := range map[string]Analysis{
		"other task":     {Kind: "direct", TaskID: "other", InputID: "second", WorkspacePath: "/fixture"},
		"same input":     a,
		"staged images":  {Kind: "images", TaskID: "task", InputID: "second", WorkspacePath: "/fixture"},
		"staged planner": {Kind: "planning", TaskID: "task", InputID: "second", WorkspacePath: "/fixture"},
	} {
		if _, err = f.s.Bind(ctx, f.team, p.ID, p.Version, wrong); err == nil {
			t.Fatalf("%s accepted for direct retry", name)
		}
	}
	retry := Analysis{Kind: "direct", TaskID: "task", InputID: "second", WorkspacePath: "/fixture"}
	if p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, retry); err != nil || p.Analysis.InputID != "second" {
		t.Fatalf("direct retry: %v", err)
	}

	direction := "新方向"
	draft := directProject(t, f, nil)
	if _, err = f.s.BindWithDirection(ctx, f.team, draft.ID, draft.Version, Analysis{Kind: "direct", TaskID: "task", InputID: "d", WorkspacePath: "/fixture"}, nil, &direction); err == nil {
		t.Fatal("direct accepted a creative direction")
	}
	if _, err = f.s.Bind(ctx, f.team, draft.ID, draft.Version, Analysis{Kind: "direct", TaskID: "task", InputID: "d", WorkspacePath: "/fixture"}, &TemplateSelection{TemplateID: "t", TemplateVersion: "1"}); err == nil {
		t.Fatal("direct accepted a template selection")
	}
	staged := directProject(t, f, func(b *Brief) { b.PromptPolicy = "" })
	if _, err = f.s.Bind(ctx, f.team, staged.ID, staged.Version, Analysis{Kind: "direct", TaskID: "task", InputID: "s", WorkspacePath: "/fixture"}); err == nil {
		t.Fatal("staged production bound the direct phase")
	}
}

func TestDirectPromptCapabilityFollowsImageDrivenCreation(t *testing.T) {
	f := setup(t)
	capabilities := f.s.FinishingCapabilities(context.Background())
	if capabilities["directPromptCreation"] != capabilities["imageDrivenCreation"] || capabilities["planningResults"] != true {
		t.Fatalf("capabilities: %+v", capabilities)
	}
}
