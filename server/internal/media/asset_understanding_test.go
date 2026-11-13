package media

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestImageDrivenBrowserContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/video-image-driven-contract.json")
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
	if err = validateAssetUnderstanding(fixture.Brief); err != nil {
		t.Fatal(err)
	}
	if fixture.Brief.ReviewPlan || !fixture.Brief.Finishing.NativeAudio || fixture.Brief.Duration != 30 {
		t.Fatal("browser intent changed")
	}
	if err = validateAssetUnderstanding(Brief{}); err != nil {
		t.Fatal("legacy affected", err)
	}
}

func TestImageDrivenPlanQueuesWithoutManualStoryboardConfirmation(t *testing.T) {
	for _, duration := range [][]int{{30}, {15, 15}} {
		f := setup(t)
		ctx := context.Background()
		current := f.project(t)
		limit := 30
		if len(duration) > 1 {
			limit = 15
		}
		b, plan, models := wholeDurationFixture(duration, limit)
		b.ImageModel = ""
		b.Finishing.NativeAudio = true
		models[0].GenerateAudio = true
		b.AssetUnderstanding = &AssetUnderstanding{Product: "cup", Category: "cup", Summary: "white cup", Keywords: []string{"cup"}, Assets: []ObservedAsset{{AssetID: "a", Role: "product", SubjectGroup: "cup", Description: "white cup"}}, TemplateCandidates: []ObservedTemplate{}}
		if err := ValidateBrief(b, models, []Asset{{ID: "a", Kind: "image"}}); err != nil {
			t.Fatal(err)
		}
		hash, _ := candidatePlanHash(plan)
		current, err := f.s.Mutate(ctx, f.team, current.ID, current.Version, func(p *Project) error {
			p.Brief, p.Models, p.Status = b, models, "analyzing"
			p.Plan, p.Shots, p.AnalysisFee = nil, nil, nil
			p.Analysis = &Analysis{TaskID: "task", InputID: "input", WorkspacePath: "/fixture"}
			p.PlanReview = &PlanReview{InputID: "input", RequestID: "review", CandidateHash: hash, Status: "passed", Issues: []string{}}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		accepted, err := f.s.acceptAnalysisPlan(ctx, f.team, current.ID, current.Version, "input", plan)
		if err != nil {
			t.Fatal(err)
		}
		if accepted.Status != "queued" || len(accepted.Shots) != len(duration) || accepted.Brief.AssetUnderstanding.Product != "cup" {
			t.Fatalf("automatic flow stopped: %+v", accepted)
		}
		if f.submits.Load() != 0 {
			t.Fatal("plan acceptance must not directly submit to provider")
		}
	}
}

func TestImageDrivenBriefValidation(t *testing.T) {
	b := Brief{Mode: "auto", GenerationPolicy: "whole-first", AssetIDs: []string{"a"}, AssetUnderstanding: &AssetUnderstanding{Product: "cup", Category: "cup", Summary: "white cup", Keywords: []string{"cup"}, Assets: []ObservedAsset{{AssetID: "a", Role: "product", SubjectGroup: "cup", Description: "white cup"}}, TemplateCandidates: []ObservedTemplate{}}}
	if err := validateAssetUnderstanding(b); err != nil {
		t.Fatal(err)
	}
	original, _ := json.Marshal(b)
	for _, mutate := range []func(*Brief){
		func(v *Brief) { v.AssetIDs = []string{"foreign"} },
		func(v *Brief) {
			v.AssetUnderstanding.Assets = append(v.AssetUnderstanding.Assets, v.AssetUnderstanding.Assets[0])
		},
		func(v *Brief) { v.ReviewPlan = true },
		func(v *Brief) { v.GenerationPolicy = "" },
		func(v *Brief) { v.AssetUnderstanding.Assets[0].Role = "logo" },
		func(v *Brief) { v.AssetUnderstanding.Ambiguity = "pick a product" },
	} {
		var value Brief
		if err := json.Unmarshal(original, &value); err != nil {
			t.Fatal(err)
		}
		mutate(&value)
		if err := validateAssetUnderstanding(value); err == nil {
			t.Fatalf("accepted invalid observation: %+v", value)
		}
	}
	var roundtrip Brief
	if err := json.Unmarshal(original, &roundtrip); err != nil || roundtrip.AssetUnderstanding.Product != "cup" {
		t.Fatal("lost frozen observation", err)
	}
}

func TestImageDrivenRolesDoNotInflateTemplateEvidence(t *testing.T) {
	b := Brief{Mode: "auto", GenerationPolicy: "whole-first", VideoModel: "v", TemplateID: "t", TemplateVersion: "1", AspectRatio: "9:16", Duration: 30, AssetIDs: []string{"a", "scene", "logo", "unused"}, AssetUnderstanding: &AssetUnderstanding{Assets: []ObservedAsset{{AssetID: "a", Role: "product"}, {AssetID: "scene", Role: "scene"}, {AssetID: "logo", Role: "logo"}, {AssetID: "unused", Role: "unused"}}}}
	if got := videoProductAssetIDs(b); len(got) != 3 || got[0] != "a" || got[1] != "scene" || got[2] != "logo" {
		t.Fatalf("unbound logo did not remain available as a video reference: %v", got)
	}
	b.Finishing = &FinishingOptions{Brand: testVideoBrand("logo")}
	if got := videoProductAssetIDs(b); len(got) != 2 || got[0] != "a" || got[1] != "scene" {
		t.Fatalf("post-only logo entered video generation: %v", got)
	}
	template := VideoTemplate{ID: "t", Version: "1", MinAssets: 2, AspectRatios: []string{"9:16"}, DurationRange: TemplateDurationRange{Min: 10, Max: 60}, VerifiedModelIDs: []string{"v"}}
	if _, err := validateTemplateSelection(b, []VideoTemplate{template}); err == nil {
		t.Fatal("scene incorrectly counted as product evidence")
	}
	template.MinAssets = 1
	if _, err := validateTemplateSelection(b, []VideoTemplate{template}); err != nil {
		t.Fatal(err)
	}
}
