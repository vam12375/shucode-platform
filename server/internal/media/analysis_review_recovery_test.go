package media

import (
	"context"
	"testing"
)

func TestAnalysisPlanDoesNotDependOnHistoricalReview(t *testing.T) {
	for _, status := range []string{"none", "passed", "warning", "unavailable", "pending", "stale"} {
		t.Run(status, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			current := f.project(t)
			b, plan, models := storyFixture()
			hash, _ := candidatePlanHash(plan)
			b.ReviewPlan = false
			current, err := f.s.Mutate(ctx, f.team, current.ID, current.Version, func(p *Project) error {
				p.Brief, p.Models = b, models
				p.Status, p.Plan, p.Shots = "analyzing", nil, nil
				p.Analysis = &Analysis{TaskID: "task", InputID: "input", WorkspacePath: "/fixture"}
				p.AnalysisFee = nil
				if status != "none" {
					if status == "stale" {
						hash = "stale"
					}
					p.PlanReview = &PlanReview{InputID: "input", RequestID: "review", CandidateHash: hash, Status: status, Issues: []string{}}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := f.s.acceptAnalysisPlan(ctx, f.team, current.ID, current.Version, "input", plan)
			if err != nil {
				t.Fatal(err)
			}
			if accepted.Status != "queued" || accepted.Plan == nil || len(accepted.Shots) != 2 {
				t.Fatalf("status=%s want=queued", accepted.Status)
			}
			if accepted.Plan.Story.Review != nil {
				t.Fatal("input review must not masquerade as independent review")
			}
			if status != "none" && accepted.PlanReview == nil {
				t.Fatal("historical review was discarded")
			}
		})
	}
}
