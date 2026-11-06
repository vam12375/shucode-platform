package media

import (
	"context"
	"testing"
)

func TestAnalysisReviewUnavailableSavesValidOutputForReview(t *testing.T) {
	for _, status := range []string{"passed", "warning", "unavailable", "pending"} {
		t.Run(status, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			current := f.project(t)
			b, plan, models := storyFixture()
			hash, _ := candidatePlanHash(plan)
			current, err := f.s.Mutate(ctx, f.team, current.ID, current.Version, func(p *Project) error {
				p.Brief, p.Models = b, models
				p.Status, p.Plan, p.Shots = "analyzing", nil, nil
				p.Analysis = &Analysis{TaskID: "task", InputID: "input", WorkspacePath: "/fixture"}
				p.AnalysisFee = nil
				p.PlanReview = &PlanReview{InputID: "input", RequestID: "review", CandidateHash: hash, Status: status, Issues: []string{}}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := f.s.acceptAnalysisPlan(ctx, f.team, current.ID, current.Version, "input", plan)
			if status == "pending" {
				if err == nil {
					t.Fatal("pending review accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := "review"
			if status == "passed" {
				want = "queued"
			}
			if accepted.Status != want || accepted.Plan == nil || len(accepted.Shots) != 2 {
				t.Fatalf("status=%s want=%s", accepted.Status, want)
			}
			if status == "unavailable" && (accepted.PlanReview.Status != "unavailable" || accepted.Plan.Story.Review.Status != "warning" || len(accepted.Plan.Story.Review.Issues) == 0) {
				t.Fatal("unavailable review lost its diagnosis")
			}
		})
	}
}
