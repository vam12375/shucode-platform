package media

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/templatelibrary"
)

func TestImageTemplateSelectionDesktopContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/video-template-selection.json")
	if err != nil {
		t.Fatal(err)
	}
	var selection TemplateSelection
	if err = json.Unmarshal(raw, &selection); err != nil || !selection.valid() || selection.TemplateID != "black-gold-reveal" || selection.TemplateVersion != "1.0.0" {
		t.Fatal("desktop selection identity diverged", err)
	}
}

func imageTemplateFixture(t *testing.T) (*fixture, *Project, Analysis, *TemplateSelection) {
	t.Helper()
	f, p, observation := stagedImageFixture(t)
	ctx := context.Background()
	a := Analysis{Kind: "images", TaskID: "same-task", InputID: "images-input", WorkspacePath: "/fixture"}
	p, err := f.s.Bind(ctx, f.team, p.ID, p.Version, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.SetImageAnalysis(ctx, f.team, p.ID, p.Version, a.InputID, observation)
	if err != nil {
		t.Fatal(err)
	}
	a.Kind, a.InputID = "planning", "planning-input"
	return f, p, a, &TemplateSelection{TemplateID: "black-gold-reveal", TemplateVersion: "1.0.0"}
}

func TestImageTemplateSelectionFreezesOnceAndAutomaticallyQueues(t *testing.T) {
	f, p, a, selection := imageTemplateFixture(t)
	ctx := context.Background()
	version := p.Version
	bound, err := f.s.Bind(ctx, f.team, p.ID, version, a, selection)
	if err != nil {
		t.Fatal(err)
	}
	if bound.Version != version+1 || bound.Status != "analyzing" || bound.Analysis.TaskID != "same-task" || bound.Brief.ReviewPlan || bound.Brief.TemplateID != selection.TemplateID || bound.TemplateSnapshot == nil || len(bound.TemplateSnapshot.Skills) != 1 || bound.ImageAnalysis == nil {
		t.Fatal("selection was not frozen atomically with the original task binding")
	}
	before, _ := json.Marshal(bound.TemplateSnapshot)
	updateProductionSkill(t, f, func(input *templatelibrary.Mutation, _ templatelibrary.Entry) {
		enabled := false
		input.Enabled = &enabled
	})
	replayed, err := f.s.Bind(ctx, f.team, p.ID, version, a, selection)
	if err != nil || replayed.Version != bound.Version {
		t.Fatal("accepted binding was not idempotent", err)
	}
	failed, err := f.s.FailAnalysis(ctx, f.team, p.ID, bound.Version, a.InputID, "analysis_failed")
	if err != nil {
		t.Fatal(err)
	}
	a.InputID = "planning-retry"
	retried, err := f.s.Bind(ctx, f.team, p.ID, failed.Version, a)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(retried.TemplateSnapshot)
	if string(before) != string(after) {
		t.Fatal("retry refreshed the frozen skill")
	}
	_, plan, _ := wholeDurationFixture([]int{15}, 15)
	plan.Research.Mode = "original"
	plan.Research.TemplateID, plan.Research.TemplateVersion = selection.TemplateID, selection.TemplateVersion
	result, err := f.s.acceptAnalysisPlan(ctx, f.team, p.ID, retried.Version, a.InputID, plan)
	if err != nil || result.Status != "queued" || result.Analysis.TaskID != "same-task" {
		t.Fatal("unverified recipe required an extra manual step", err)
	}
	if f.submits.Load() != 0 {
		t.Fatal("binding or plan acceptance directly invoked the paid supplier")
	}
}

func TestImageTemplateSelectionRejectsInvalidOrStaleWithoutMutation(t *testing.T) {
	for _, variant := range []string{"missing-version", "missing-id", "oversize", "unknown", "stale-version", "wrong-task", "wrong-workspace", "wrong-phase", "has-plan", "cancelled", "disabled-skill"} {
		t.Run(variant, func(t *testing.T) {
			f, p, a, selection := imageTemplateFixture(t)
			ctx := context.Background()
			version := p.Version
			switch variant {
			case "missing-version":
				selection.TemplateVersion = ""
			case "missing-id":
				selection.TemplateID = ""
			case "oversize":
				selection.TemplateID = strings.Repeat("x", 101)
			case "unknown":
				selection.TemplateID = "unknown-template"
			case "stale-version":
				version--
			case "wrong-task":
				a.TaskID = "another-task"
			case "wrong-workspace":
				a.WorkspaceIdentity = "another-workspace"
			case "wrong-phase":
				a.Kind = "images"
			case "has-plan":
				var err error
				p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error { v.Plan = &Plan{}; return nil })
				if err != nil {
					t.Fatal(err)
				}
				version = p.Version
			case "cancelled":
				var err error
				p, err = f.s.Action(ctx, f.team, p.ID, p.Version, "cancel")
				if err != nil {
					t.Fatal(err)
				}
				version = p.Version
			case "disabled-skill":
				updateProductionSkill(t, f, func(input *templatelibrary.Mutation, _ templatelibrary.Entry) {
					disabled := false
					input.Enabled = &disabled
				})
			}
			before, _ := json.Marshal(p)
			if _, err := f.s.Bind(ctx, f.team, p.ID, version, a, selection); err == nil {
				t.Fatal("invalid selection accepted")
			}
			current, _ := f.s.Repo.Get(ctx, f.team, p.ID)
			after, _ := json.Marshal(current)
			if string(before) != string(after) {
				t.Fatal("rejected selection mutated the project")
			}
		})
	}
}

func TestImageTemplateSelectionCannotChangeAfterBindingOrClaimVerification(t *testing.T) {
	f, p, a, selection := imageTemplateFixture(t)
	ctx := context.Background()
	p, err := f.s.Bind(ctx, f.team, p.ID, p.Version, a, selection)
	if err != nil {
		t.Fatal(err)
	}
	other := &TemplateSelection{TemplateID: "single-proof", TemplateVersion: "1.0.0"}
	if _, err = f.s.Bind(ctx, f.team, p.ID, p.Version, a, other); err == nil {
		t.Fatal("same input changed its selected recipe")
	}
	_, plan, _ := wholeDurationFixture([]int{15}, 15)
	plan.Research.Mode = "template"
	plan.Research.TemplateID, plan.Research.TemplateVersion = selection.TemplateID, selection.TemplateVersion
	if _, err = f.s.acceptAnalysisPlan(ctx, f.team, p.ID, p.Version, a.InputID, plan); err == nil {
		t.Fatal("unverified recipe claimed verified template mode")
	}
	advanced := p.Brief
	advanced.ImageAnalysisFirst = false
	if _, err = validateTemplateSelection(advanced, []VideoTemplate{p.TemplateSnapshot.Template}); err == nil {
		t.Fatal("advanced flow lost its draft review requirement")
	}
}
