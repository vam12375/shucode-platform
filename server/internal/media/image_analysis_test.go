package media

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
)

func stagedImageFixture(t *testing.T) (*fixture, *Project, ImageAnalysis) {
	t.Helper()
	f := setup(t)
	p := f.project(t)
	raw, err := os.ReadFile("testdata/video-staged-images.json")
	if err != nil {
		t.Fatal(err)
	}
	var wire ProjectView
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&wire); err != nil {
		t.Fatal(err)
	}
	if wire.Status != "image_review" || wire.ImageAnalysis == nil || wire.Plan != nil || len(wire.Shots) != 0 {
		t.Fatal("wire lost image phase")
	}
	b, _, models := wholeDurationFixture([]int{15}, 15)
	b.ImageAnalysisFirst = true
	o := *wire.ImageAnalysis
	o.Assets[0].AssetID = "a"
	p, err = f.s.Mutate(context.Background(), f.team, p.ID, p.Version, func(v *Project) error {
		v.Brief, v.Models, v.Status = b, models, "draft"
		v.Plan, v.Shots, v.Analysis = nil, []Shot{}, nil
		v.QuoteMicro = 0
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, p, o
}

func TestStagedImageAnalysisRetryAndCancellation(t *testing.T) {
	for _, phase := range []string{"images", "image_review", "planning"} {
		t.Run(phase, func(t *testing.T) {
			f, p, observation := stagedImageFixture(t)
			ctx := context.Background()
			a := Analysis{Kind: "images", TaskID: "same-task", InputID: "images-input", WorkspacePath: "/fixture"}
			p, err := f.s.Bind(ctx, f.team, p.ID, p.Version, a)
			if err != nil {
				t.Fatal(err)
			}
			if phase != "images" {
				p, err = f.s.SetImageAnalysis(ctx, f.team, p.ID, p.Version, a.InputID, observation)
				if err != nil {
					t.Fatal(err)
				}
			}
			if phase == "planning" {
				a.Kind, a.InputID = "planning", "plan-input"
				p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, a)
				if err != nil {
					t.Fatal(err)
				}
			}
			if phase != "image_review" {
				p, err = f.s.FailAnalysis(ctx, f.team, p.ID, p.Version, a.InputID, "analysis_failed")
				if err != nil {
					t.Fatal(err)
				}
				wrong := a
				wrong.InputID = "wrong-phase"
				if phase == "images" {
					wrong.Kind = "planning"
				} else {
					wrong.Kind = "images"
				}
				if _, err = f.s.Bind(ctx, f.team, p.ID, p.Version, wrong); err == nil {
					t.Fatal("retry changed phase")
				}
				a.InputID += "-retry"
				p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, a)
				if err != nil {
					t.Fatal(err)
				}
				if p.Analysis.TaskID != "same-task" || (phase == "planning" && p.ImageAnalysis == nil) {
					t.Fatal("retry lost context")
				}
			}
			p, err = f.s.Action(ctx, f.team, p.ID, p.Version, "cancel")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.SetImageAnalysis(ctx, f.team, p.ID, p.Version, a.InputID, observation); err == nil {
				t.Fatal("cancelled result accepted")
			}
			a.InputID += "-after-cancel"
			if _, err = f.s.Bind(ctx, f.team, p.ID, p.Version, a); err == nil {
				t.Fatal("cancelled project rebound")
			}
		})
	}
}

func TestStagedImageAnalysisRejectsInvalidObservation(t *testing.T) {
	for _, invalid := range []string{"stale-version", "empty", "changed-workspace"} {
		t.Run(invalid, func(t *testing.T) {
			f, p, observation := stagedImageFixture(t)
			ctx := context.Background()
			a := Analysis{Kind: "images", TaskID: "same-task", InputID: "images-input", WorkspacePath: "/fixture"}
			p, err := f.s.Bind(ctx, f.team, p.ID, p.Version, a)
			if err != nil {
				t.Fatal(err)
			}
			version := p.Version
			switch invalid {
			case "stale-version":
				version--
			case "empty":
				observation = ImageAnalysis{Text: " \n "}
			case "changed-workspace":
				p, err = f.s.FailAnalysis(ctx, f.team, p.ID, p.Version, a.InputID, "analysis_failed")
				if err != nil {
					t.Fatal(err)
				}
				a.InputID, a.WorkspaceIdentity = "retry", "other-identity"
				if _, err = f.s.Bind(ctx, f.team, p.ID, p.Version, a); err == nil {
					t.Fatal("identity changed")
				}
				return
			}
			if _, err = f.s.SetImageAnalysis(ctx, f.team, p.ID, version, a.InputID, observation); err == nil {
				t.Fatal("invalid observation accepted")
			}
			current, _ := f.s.Repo.Get(ctx, f.team, p.ID)
			if current.Version != p.Version || current.ImageAnalysis != nil {
				t.Fatal("rejection mutated project")
			}
		})
	}
}

func TestStagedImageAnalysis(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := f.project(t)
	b, plan, models := wholeDurationFixture([]int{15}, 15)
	b.ImageAnalysisFirst = true
	var err error
	p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Brief, v.Models, v.Status = b, models, "draft"
		v.Plan, v.Shots, v.Analysis = nil, []Shot{}, nil
		v.QuoteMicro = 0
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a := Analysis{Kind: "images", TaskID: "same-task", InputID: "images-input", WorkspacePath: "/fixture"}
	p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SetPlan(ctx, f.team, p.ID, p.Version, a.InputID, plan); err == nil {
		t.Fatal("images accepted a video plan")
	}
	o := ImageAnalysis{Text: "A white cup. Recommend an original presentation."}
	prior := p.Version
	p, err = f.s.SetImageAnalysis(ctx, f.team, p.ID, p.Version, a.InputID, o)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "image_review" || p.Plan != nil || len(p.Shots) != 0 || p.QuoteMicro != 0 || p.ImageAnalysis == nil {
		t.Fatalf("invalid image phase: %+v", p)
	}
	repeated, err := f.s.SetImageAnalysis(ctx, f.team, p.ID, prior, a.InputID, o)
	if err != nil || repeated.Version != p.Version {
		t.Fatal("image result not idempotent", err)
	}
	if _, err = f.s.Action(ctx, f.team, p.ID, p.Version, "generate"); err == nil {
		t.Fatal("generation skipped planning")
	}
	a.Kind, a.InputID = "planning", "plan-input"
	wrong := a
	wrong.TaskID = "other-task"
	if _, err = f.s.Bind(ctx, f.team, p.ID, p.Version, wrong); err == nil {
		t.Fatal("task changed")
	}
	p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SetImageAnalysis(ctx, f.team, p.ID, p.Version, "images-input", o); err == nil {
		t.Fatal("stale image result accepted")
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
	if p.Status != "queued" || p.Analysis.TaskID != "same-task" || p.ImageAnalysis == nil {
		t.Fatal("planning lost task or observation")
	}
	if f.submits.Load() != 0 {
		t.Fatal("acceptance directly submitted provider work")
	}
}

func TestStagedImageAnalysisAcceptsTextAndRecoversCompletedFailure(t *testing.T) {
	for _, text := range []string{"Images show a robot character. Subject is uncertain.", "## Image content\n- Two different subjects\n## Templates\nNone", `{"ambiguity":"please confirm subject","assets":[]}`, `{"summary":"incomplete JSON"`} {
		for _, status := range []string{"analyzing", "analysis_failed"} {
			t.Run(status+text, func(t *testing.T) {
				f, p, _ := stagedImageFixture(t)
				ctx := context.Background()
				p, err := f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{Kind: "images", TaskID: "same-task", InputID: "images-input", WorkspacePath: "/fixture"})
				if err != nil {
					t.Fatal(err)
				}
				if status == "analysis_failed" {
					p, err = f.s.FailAnalysis(ctx, f.team, p.ID, p.Version, p.Analysis.InputID, "analysis_failed")
					if err != nil {
						t.Fatal(err)
					}
				}
				p, err = f.s.SetImageAnalysis(ctx, f.team, p.ID, p.Version, p.Analysis.InputID, ImageAnalysis{Text: text})
				if err != nil || p.Status != "image_review" || p.ImageAnalysis.Text != text {
					t.Fatal("completed text rejected", err)
				}
				encoded, err := json.Marshal(f.s.ProjectView(ctx, p))
				if err != nil {
					t.Fatal(err)
				}
				var view map[string]any
				if err = json.Unmarshal(encoded, &view); err != nil {
					t.Fatal(err)
				}
				image := view["imageAnalysis"].(map[string]any)
				if len(image) != 1 || image["text"] != text {
					t.Fatal("text wire leaked empty legacy fields")
				}
			})
		}
	}
	// 旧客户端提交的主体疑问仅作文字说明，也不再阻断。
	f, p, o := stagedImageFixture(t)
	o.Ambiguity = "Please confirm the subject"
	o.Assets[0].AssetID = "descriptive-unmatched-id"
	ctx := context.Background()
	p, err := f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{Kind: "images", TaskID: "same-task", InputID: "images-input", WorkspacePath: "/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SetImageAnalysis(ctx, f.team, p.ID, p.Version, p.Analysis.InputID, o); err != nil {
		t.Fatal("legacy description rejected", err)
	}
}

func TestImageAnalysisTextWire(t *testing.T) {
	raw, err := os.ReadFile("testdata/video-image-analysis-text.json")
	if err != nil {
		t.Fatal(err)
	}
	var p ProjectView
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.ImageAnalysis == nil || p.ImageAnalysis.Text == "" || p.ImageAnalysis.AssetUnderstanding != nil || p.Status != "image_review" {
		t.Fatal("text contract changed")
	}
}
