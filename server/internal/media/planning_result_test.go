package media

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestPlanningResultPreservesCompletedOutput(t *testing.T) {
	for _, status := range []string{"analyzing", "analysis_failed"} {
		t.Run(status, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			p := f.project(t)
			p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
				v.Status = status
				v.Plan, v.Shots = nil, []Shot{}
				v.Analysis = &Analysis{Kind: "planning", TaskID: "same-task", InputID: "same-input", WorkspacePath: "/fixture"}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			original := p.Version
			result := PlanningResult{Text: "完整导演方案，声音按对白、音效和时窗描述。", Issues: []PlanningIssue{{Path: "shots.0.duration", Code: "invalid_type"}}}
			accepted, err := f.s.SetPlanningResult(ctx, f.team, p.ID, p.Version, "same-input", result)
			if err != nil {
				t.Fatal(err)
			}
			if accepted.Status != "analysis_ready" || accepted.PlanningResult.Text != result.Text || len(accepted.Shots) != 0 || accepted.Plan != nil {
				t.Fatalf("unexpected result: %+v", accepted)
			}
			again, err := f.s.SetPlanningResult(ctx, f.team, p.ID, original, "same-input", result)
			if err != nil || again.Version != accepted.Version {
				t.Fatalf("replay: %v", err)
			}
			if _, err := f.s.SetPlanningResult(ctx, f.team, p.ID, accepted.Version, "other-input", result); err == nil {
				t.Fatal("stale input accepted")
			}
			if _, err := f.s.SetPlanningResult(ctx, f.team, p.ID, accepted.Version, "same-input", PlanningResult{Text: " ", Issues: []PlanningIssue{}}); err == nil {
				t.Fatal("blank accepted")
			}
			cancelled, err := f.s.Action(ctx, f.team, p.ID, accepted.Version, "cancel")
			if err != nil || cancelled.Status != "cancelled" {
				t.Fatalf("cancel: %v", err)
			}
			if _, err := f.s.SetPlanningResult(ctx, f.team, p.ID, cancelled.Version, "same-input", result); err == nil {
				t.Fatal("cancelled result accepted")
			}
		})
	}
}

func TestPlanningResultWire(t *testing.T) {
	raw, err := os.ReadFile("testdata/video-planning-result.json")
	if err != nil {
		t.Fatal(err)
	}
	var p ProjectView
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.Status != "analysis_ready" || p.PlanningResult == nil || len(p.PlanningResult.Issues) != 1 || p.Plan != nil {
		t.Fatal("wire lost complete output")
	}
}

func TestPlanningResultContinuesExistingPlanAdmission(t *testing.T) {
	for _, review := range []string{"passed", "warning", "unavailable"} {
		t.Run(review, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			p := f.project(t)
			b, plan, models := wholeDurationFixture([]int{15}, 15)
			b.ImageAnalysisFirst = true
			hash, _ := candidatePlanHash(plan)
			p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
				v.Brief, v.Models, v.Status = b, models, "analyzing"
				v.Plan, v.Shots, v.AnalysisFee = nil, []Shot{}, nil
				v.Analysis = &Analysis{TaskID: "task", InputID: "input", Kind: "planning", WorkspacePath: "/fixture"}
				v.ImageAnalysis = &ImageAnalysis{Text: "角色素材"}
				v.PlanReview = &PlanReview{InputID: "input", RequestID: "review", CandidateHash: hash, Status: review, Issues: []string{"创作建议"}}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			p, err = f.s.SetPlanningResult(ctx, f.team, p.ID, p.Version, "input", PlanningResult{Text: "完整方案", Issues: []PlanningIssue{{Path: "$", Code: "generation_parameters_invalid"}}})
			if err != nil {
				t.Fatal(err)
			}
			p, err = f.s.acceptAnalysisPlan(ctx, f.team, p.ID, p.Version, "input", plan)
			if err != nil {
				t.Fatal(err)
			}
			if p.Status != "queued" || p.PlanningResult == nil || len(p.PlanningResult.Issues) != 0 || len(p.Shots) != 1 {
				t.Fatalf("did not continue: %s", p.Status)
			}
		})
	}
}
