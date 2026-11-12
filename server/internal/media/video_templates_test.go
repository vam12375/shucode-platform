package media

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/templatelibrary"
)

func templateReviewFixture(t *testing.T) (*fixture, *Project, TemplateReviewInput) {
	t.Helper()
	f := setup(t)
	ctx := context.Background()
	templates, err := VideoTemplateDefinitions()
	if err != nil || len(templates) < 32 {
		t.Fatalf("catalog: count=%d err=%v", len(templates), err)
	}
	template := templates[0]
	b, p, models := adaptivePlanFixture()
	b.VideoModel = "v"
	b.TemplateID = template.ID
	b.TemplateVersion = template.Version
	b.ReviewPlan = true
	b.ReferenceMode = "frames"
	p.Shots[0].AssetID = "a"
	b.Duration = template.DurationRange.Min
	p.Shots[0].Duration = b.Duration
	p.Research.TemplateID = template.ID
	p.Research.TemplateVersion = template.Version
	models, err = f.s.Repo.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	project := &Project{ID: uuid.NewString(), TeamID: f.team, UserID: f.user, Version: 1, Status: "completed", Brief: b, Plan: &p, Models: models, OutputAssetID: uuid.NewString(), Shots: []Shot{}}
	if err = f.s.Repo.Save(ctx, project, nil); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Repo.AddAsset(ctx, f.team, Asset{ID: project.OutputAssetID, Kind: "video", Size: 7}); err != nil {
		t.Fatal(err)
	}
	path := f.s.Blobs.Path(f.team, project.OutputAssetID)
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	return f, project, TemplateReviewInput{TemplateID: template.ID, TemplateVersion: template.Version, ProjectTeamID: f.team, ProjectID: project.ID, Score: 90, Approved: true, Note: "人工fixture仅证明审核约束，不证明成片质量"}
}

// currentTemplateVersion 返回目录里该配方的现行版本：配方正文每次改写都会升版，夹具不写死版本号。
func currentTemplateVersion(t *testing.T, id string) string {
	t.Helper()
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, template := range templates {
		if template.ID == id {
			return template.Version
		}
	}
	t.Fatalf("recipe %s is missing from the catalog", id)
	return ""
}

func TestTemplateReviewDerivesModelFromCompletedEvidenceAndCanRevoke(t *testing.T) {
	f, p, input := templateReviewFixture(t)
	ctx := context.Background()
	templates, err := f.s.VideoTemplates(ctx)
	if err != nil || len(templates[0].VerifiedModelIDs) != 0 {
		t.Fatal("unreviewed catalog claims verification")
	}
	if err = f.s.validateAdaptivePlan(ctx, p.Brief, *p.Plan, p.Models); err != nil {
		t.Fatal(err)
	}
	p.Plan.Research.Mode = "template"
	if err = f.s.validateAdaptivePlan(ctx, p.Brief, *p.Plan, p.Models); err != nil {
		t.Fatal("unreviewed template blocked executable plan", err)
	}
	review, err := f.s.ReviewVideoTemplate(ctx, "root-fixture", input)
	if err != nil || review.ModelID != p.Brief.VideoModel || review.OutputAssetID != p.OutputAssetID || review.ReviewerID != "root-fixture" {
		t.Fatalf("review=%+v err=%v", review, err)
	}
	templates, err = f.s.VideoTemplates(ctx)
	if err != nil || !slices.Contains(templates[0].VerifiedModelIDs, "v") {
		t.Fatalf("review missing: %+v %v", templates[0], err)
	}
	if err = f.s.validateAdaptivePlan(ctx, p.Brief, *p.Plan, p.Models); err != nil {
		t.Fatal(err)
	}
	input.Approved = false
	input.Score = 70
	if _, err = f.s.ReviewVideoTemplate(ctx, "root-fixture", input); err != nil {
		t.Fatal(err)
	}
	templates, err = f.s.VideoTemplates(ctx)
	if err != nil || len(templates[0].VerifiedModelIDs) != 0 {
		t.Fatal("revoked template still verified")
	}
}

func TestTemplateReviewRejectsMissingOutputAndChangedModelSnapshot(t *testing.T) {
	f, p, input := templateReviewFixture(t)
	ctx := context.Background()
	if _, err := f.s.ReviewVideoTemplate(ctx, "root-fixture", input); err != nil {
		t.Fatal(err)
	}
	models, err := f.s.Repo.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	models[0].UpstreamModel = "changed-model"
	raw, _ := json.Marshal(models)
	if err = f.s.Repo.Store.SetConfigValue(ctx, ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	templates, err := f.s.VideoTemplates(ctx)
	if err != nil || len(templates[0].VerifiedModelIDs) != 0 {
		t.Fatal("old evidence verified changed model")
	}
	if err = os.Remove(f.s.Blobs.Path(f.team, p.OutputAssetID)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.ReviewVideoTemplate(ctx, "root-fixture", input); err == nil {
		t.Fatal("missing real output accepted")
	}
	input.ProjectTeamID = uuid.NewString()
	if _, err = f.s.ReviewVideoTemplate(ctx, "root-fixture", input); err == nil {
		t.Fatal("cross-team fabricated evidence accepted")
	}
}

func TestTemplateReviewForChangedModelCannotVerifyEarlierFrozenProject(t *testing.T) {
	f, frozen, input := templateReviewFixture(t)
	ctx := context.Background()
	changed := slices.Clone(frozen.Models)
	for i := range changed {
		if changed[i].ID == frozen.Brief.VideoModel {
			changed[i].UpstreamModel = "new-unreviewed-mapping"
		}
	}
	raw, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Repo.Store.SetConfigValue(ctx, ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	evidence := *frozen
	evidence.ID = uuid.NewString()
	evidence.Models = changed
	if err = f.s.Repo.Save(ctx, &evidence, nil); err != nil {
		t.Fatal(err)
	}
	input.ProjectID = evidence.ID
	if _, err = f.s.ReviewVideoTemplate(ctx, "root-fixture", input); err != nil {
		t.Fatal(err)
	}
	templates, err := f.s.VideoTemplates(ctx)
	if err != nil || !slices.Contains(templates[0].VerifiedModelIDs, frozen.Brief.VideoModel) {
		t.Fatalf("current model B should be verified: %v", err)
	}
	plan := *frozen.Plan
	decision := *plan.Research
	decision.Mode = "template"
	plan.Research = &decision
	// 旧映射 A 可继续原创执行，但不能借用同 ID 的 B 验证或信任模型自评。
	if normalized := normalizeAutoPlanMetadata(frozen.Brief, plan, frozen.TemplateSnapshot); normalized.Research.Mode != "original" {
		t.Fatal("model B approval incorrectly verified frozen model A")
	}
	if err = f.s.validateAdaptivePlan(ctx, evidence.Brief, plan, evidence.Models); err != nil {
		t.Fatalf("approved frozen model B rejected: %v", err)
	}
	currentBrief := evidence.Brief
	currentBrief.ReviewPlan = false
	if err = f.s.validateTemplateBrief(ctx, currentBrief, changed); err != nil {
		t.Fatalf("current model B creation selection rejected: %v", err)
	}
	for _, entry := range []string{"accept", "revise", "generate"} {
		t.Run(entry, func(t *testing.T) {
			project := *frozen
			project.ID = uuid.NewString()
			project.Status = "review"
			project.Plan = &plan
			if entry == "accept" {
				project.Status = "analyzing"
				project.Plan = nil
				project.Analysis = &Analysis{TaskID: "task", InputID: "input", WorkspacePath: "/tmp"}
			}
			if err := f.s.Repo.Save(ctx, &project, nil); err != nil {
				t.Fatal(err)
			}
			var err error
			switch entry {
			case "accept":
				_, err = f.s.SetPlan(ctx, f.team, project.ID, project.Version, "input", plan)
			case "revise":
				_, err = f.s.Revise(ctx, f.team, project.ID, project.Version, plan)
			case "generate":
				_, err = f.s.action(ctx, f.team, project.ID, project.Version, "generate", "", "")
			}
			if err != nil {
				t.Fatalf("%s blocked executable frozen A: %v", entry, err)
			}
			stored, readErr := f.s.Repo.Get(ctx, f.team, project.ID)
			if readErr != nil || stored.Plan.Research.Mode != "original" {
				t.Fatal("model B approval leaked into A", readErr)
			}
		})
	}
}

func TestTemplateLibraryDisableBlocksAdmissionAndPreservesReview(t *testing.T) {
	f, p, input := templateReviewFixture(t)
	ctx := context.Background()
	if _, err := f.s.ReviewVideoTemplate(ctx, "root", input); err != nil {
		t.Fatal(err)
	}
	repo := templatelibrary.Repository{Store: f.s.Repo.Store}
	seeds := []templatelibrary.Entry{{ID: templatelibrary.VideoID(input.TemplateID), Kind: "video", Source: "builtin-video", Enabled: true}}
	entries, err := repo.List(ctx, seeds)
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	updated, err := repo.Save(ctx, seeds, entries[0].ID, templatelibrary.Mutation{ExpectedVersion: entries[0].ExpectedVersion, Enabled: &disabled}, false)
	if err != nil {
		t.Fatal(err)
	}
	templates, err := f.s.VideoTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range templates {
		if item.ID == input.TemplateID {
			t.Fatal("disabled template leaked")
		}
	}
	if err = f.s.validateTemplateBrief(ctx, p.Brief, p.Models); err == nil {
		t.Fatal("disabled template admitted")
	}
	if err = f.s.validateAdaptivePlan(ctx, p.Brief, *p.Plan, p.Models); err != nil {
		t.Fatal("disabled current catalog blocked frozen plan", err)
	}
	all, err := f.s.AllVideoTemplates(ctx, p.Models)
	if err != nil || !slices.Contains(all[0].VerifiedModelIDs, p.Brief.VideoModel) {
		t.Fatal("disabled template lost evidence")
	}
	enabled := true
	updated, err = repo.Save(ctx, seeds, updated.ID, templatelibrary.Mutation{ExpectedVersion: updated.ExpectedVersion, Enabled: &enabled}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.validateTemplateBrief(ctx, p.Brief, p.Models); err != nil {
		t.Fatal("re-enabled template rejected")
	}
	if _, err = repo.Save(ctx, seeds, updated.ID, templatelibrary.Mutation{ExpectedVersion: updated.ExpectedVersion}, true); err != nil {
		t.Fatal(err)
	}
	if err = f.s.validateTemplateBrief(ctx, p.Brief, p.Models); err == nil {
		t.Fatal("deleted template admitted")
	}
	if err = f.s.validateAdaptivePlan(ctx, p.Brief, *p.Plan, p.Models); err != nil {
		t.Fatal("deleted current catalog blocked frozen plan", err)
	}
	all, err = f.s.AllVideoTemplates(ctx, p.Models)
	if err != nil || !slices.Contains(all[0].VerifiedModelIDs, p.Brief.VideoModel) {
		t.Fatal("deleted template lost historical review evidence")
	}
}
