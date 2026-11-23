package media

import (
	"context"
	"testing"
)

func TestTemplateAutoStopKeepsImageAndInvalidatesPendingPlanning(t *testing.T) {
	f, p, a, sel := imageTemplateFixture(t)
	ctx := context.Background()
	p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Brief.TemplateID, v.Brief.TemplateVersion = sel.TemplateID, sel.TemplateVersion
		v.Brief.TemplateAutoGenerate = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	direction := "replacement"
	if _, err := f.s.BindWithDirection(ctx, f.team, p.ID, p.Version, a, sel, &direction); err == nil {
		t.Fatal("replaced frozen direction")
	}
	stopped, err := f.s.Action(ctx, f.team, p.ID, p.Version, "stop-template-auto")
	if err != nil || !stopped.TemplateAutoStopped || !stopped.Brief.TemplateAutoGenerate || stopped.Status != "image_review" || stopped.ImageAnalysis == nil {
		t.Fatal("lost observation or authorization history", err)
	}
	if _, err := f.s.Bind(ctx, f.team, p.ID, p.Version, a, sel); err == nil {
		t.Fatal("accepted stale automatic continuation")
	}
	manual, err := f.s.BindWithDirection(ctx, f.team, p.ID, stopped.Version, a, sel, &direction)
	if err != nil || manual.Brief.SellingPoints != direction {
		t.Fatal("manual continuation failed", err)
	}
	if _, err := f.s.Action(ctx, f.team, p.ID, manual.Version, "stop-template-auto"); err == nil {
		t.Fatal("stopped after planning")
	}
}

func TestTemplateAutoRequiresExplicitTemplateAndStagedFlow(t *testing.T) {
	b, _, _ := wholeDurationFixture([]int{15}, 15)
	b.ImageAnalysisFirst, b.TemplateAutoGenerate = true, true
	if validateAdaptiveBrief(b) == nil {
		t.Fatal("accepted automatic planning without a template")
	}
	b.TemplateID, b.TemplateVersion = "frozen", "1"
	if err := validateAdaptiveBrief(b); err != nil {
		t.Fatal(err)
	}
	b.ReviewPlan = true
	if validateAdaptiveBrief(b) == nil {
		t.Fatal("accepted contradictory review policy")
	}
}
