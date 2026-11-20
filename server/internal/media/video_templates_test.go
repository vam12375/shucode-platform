package media

import (
	"bytes"
	"context"
	"slices"
	"strconv"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/templatelibrary"
)

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

func TestTemplateAvailabilityTracksVideoModelsWithoutReview(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	const historicalKey = "media.video-template-reviews"
	legacy := []byte(`[{"approved":false,"score":40,"templateId":"black-gold-reveal"}]`)
	if err := f.s.Repo.Store.SetConfigValue(ctx, historicalKey, legacy); err != nil {
		t.Fatal(err)
	}
	b, plan, models := adaptivePlanFixture()
	b.TemplateID = "black-gold-reveal"
	b.TemplateVersion = currentTemplateVersion(t, b.TemplateID)
	b.Duration = 15
	b.ReviewPlan = false
	newVideo := models[0]
	newVideo.ID = "new-video"
	models[0].UpstreamModel = "new-mapping"
	models = append(models, newVideo, Model{ID: "image", Kind: "image"})
	templates, _, err := f.s.VideoTemplateCatalog(ctx, models)
	if err != nil || len(templates) == 0 {
		t.Fatal("catalog", err)
	}
	for _, tpl := range templates {
		if tpl.Experiment != nil || !slices.Equal(tpl.VerifiedModelIDs, []string{b.VideoModel, newVideo.ID}) {
			t.Fatalf("invalid model availability for %s: %v", tpl.ID, tpl.VerifiedModelIDs)
		}
	}
	snapshot, err := f.s.freezeTemplate(ctx, b, models)
	if err != nil || !slices.Equal(snapshot.Template.VerifiedModelIDs, []string{b.VideoModel}) {
		t.Fatalf("selected recipe did not freeze with the requested model: %v", err)
	}
	snapshot.Template.VerifiedModelIDs = []string{} // 旧项目的历史正文和摘要必须可重放。
	project := Project{Brief: b, TemplateSnapshot: snapshot}
	view := project.View()
	if !slices.Equal(view.TemplateSnapshot.Template.VerifiedModelIDs, []string{b.VideoModel}) || len(snapshot.Template.VerifiedModelIDs) != 0 {
		t.Fatal("legacy projection failed to isolate selected model")
	}
	if normalized := normalizeAutoPlanMetadata(b, plan, snapshot, nil); normalized.Research.Mode != "template" {
		t.Fatal("old snapshot still required review")
	}
	plan.References = []Reference{{URL: "https://example.com/source", Title: "观察到的参考镜头"}}
	if normalized := normalizeAutoPlanMetadata(b, plan, snapshot, nil); normalized.Research.Mode != "reference" || normalized.References[0] != plan.References[0] {
		t.Fatal("frozen recipe took precedence over existing references")
	}
	wrong := b
	wrong.TemplateVersion = "unavailable-version"
	if _, err := f.s.freezeTemplate(ctx, wrong, models); err == nil {
		t.Fatal("unavailable version accepted")
	}
	after, err := f.s.Repo.Store.GetConfigValue(ctx, historicalKey)
	if err != nil || !bytes.Equal(after, legacy) {
		t.Fatalf("historical evidence changed: %s %v", after, err)
	}
}

func TestTemplateCatalogKeepsLegacyModelListReadable(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b, _, fixture := adaptivePlanFixture()
	models := make([]Model, 101)
	for i := range models {
		models[i] = fixture[0]
		models[i].ID = "video-" + strconv.Itoa(i)
	}
	templates, _, err := f.s.VideoTemplateCatalog(ctx, models)
	if err != nil || len(templates) == 0 {
		t.Fatal("catalog", err)
	}
	if ids := templates[0].VerifiedModelIDs; len(ids) != 100 || ids[99] != models[99].ID {
		t.Fatalf("legacy catalog rejected or model order changed: %v", ids)
	}
	b.TemplateID, b.TemplateVersion = templates[0].ID, templates[0].Version
	b.VideoModel = models[100].ID
	b.Duration = templates[0].NominalSeconds
	snapshot, err := f.s.freezeTemplate(ctx, b, models)
	if err != nil || snapshot == nil || !slices.Equal(snapshot.Template.VerifiedModelIDs, []string{b.VideoModel}) {
		t.Fatalf("catalog projection restricted explicit selection: %v", err)
	}
}

func TestTemplateLibraryDisablePreservesFrozenAdmission(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b, p, models := adaptivePlanFixture()
	b.TemplateID = "black-gold-reveal"
	b.TemplateVersion = currentTemplateVersion(t, b.TemplateID)
	b.Duration = 15
	snapshot, err := f.s.freezeTemplate(ctx, b, models)
	if err != nil {
		t.Fatal(err)
	}
	repo := templatelibrary.Repository{Store: f.s.Repo.Store}
	id := templatelibrary.VideoID(b.TemplateID)
	seeds := []templatelibrary.Entry{{ID: id, Kind: "video", Source: "builtin-video", Enabled: true}}
	entries, err := repo.List(ctx, seeds)
	if err != nil {
		t.Fatal(err)
	}
	var target templatelibrary.Entry
	for _, e := range entries {
		if e.ID == id {
			target = e
			break
		}
	}
	off := false
	_, err = repo.Save(ctx, seeds, id, templatelibrary.Mutation{ExpectedVersion: target.ExpectedVersion, Enabled: &off}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.freezeTemplate(ctx, b, models); err == nil {
		t.Fatal("disabled recipe admitted")
	}
	if normalized := normalizeAutoPlanMetadata(b, p, snapshot, nil); normalized.Research.Mode != "template" {
		t.Fatal("frozen recipe changed when its catalog entry was disabled")
	}
}

func TestAutomaticTemplatePlanUsesLiveCatalogAndReplaysAcceptedIdentity(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := f.project(t)
	b, plan, models := adaptivePlanFixture()
	b.Duration, plan.Shots[0].Duration = 15, 15
	b.VideoModel, models[0].ID = "v", "v"
	b.ReviewPlan = true
	plan.Research.Mode = "template"
	plan.Research.TemplateID = "black-gold-reveal"
	plan.Research.TemplateVersion = currentTemplateVersion(t, plan.Research.TemplateID)
	p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Brief, v.Models, v.Status = b, models, "analyzing"
		v.Plan, v.Shots, v.AnalysisFee = nil, []Shot{}, nil
		v.Analysis = &Analysis{TaskID: "task", InputID: "input", Kind: "planning", WorkspacePath: "/fixture"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	recipes, _ := VideoFormulaTestDefinitions()
	for _, claim := range []TemplateSelection{
		{TemplateID: "invented", TemplateVersion: "1"},
		{TemplateID: plan.Research.TemplateID, TemplateVersion: "retired-version"},
		{TemplateID: recipes[0].ID, TemplateVersion: recipes[0].Version},
	} {
		attempt := plan
		claimResearch := *plan.Research
		claimResearch.TemplateID, claimResearch.TemplateVersion = claim.TemplateID, claim.TemplateVersion
		attempt.Research = &claimResearch
		clean, err := f.s.normalizeNewPlan(ctx, p, attempt)
		if err != nil || clean.Research.Mode != "original" || clean.Research.TemplateID != "" {
			t.Fatalf("unavailable automatic identity admitted: %s %v", claim.TemplateID, err)
		}
	}
	beforeReview := p.Version
	accepted, err := f.s.SetPlan(ctx, f.team, p.ID, beforeReview, "input", plan)
	if err != nil || accepted.Plan.Research.Mode != "template" || accepted.Plan.Research.TemplateID != plan.Research.TemplateID ||
		accepted.Plan.Research.TemplateVersion != plan.Research.TemplateVersion || accepted.Brief.TemplateID != "" || accepted.TemplateSnapshot != nil {
		t.Fatal("automatic recipe or freeze semantics changed", err)
	}
	updateProductionSkill(t, f, func(input *templatelibrary.Mutation, _ templatelibrary.Entry) {
		off := false
		input.Enabled = &off
	})
	newPlan, err := f.s.normalizeNewPlan(ctx, p, plan)
	if err != nil || newPlan.Research.Mode != "original" || newPlan.Research.TemplateID != "" {
		t.Fatal("new automatic selection used a disabled recipe", err)
	}
	replayed, err := f.s.SetPlan(ctx, f.team, p.ID, beforeReview, "input", plan)
	if err != nil || replayed.Version != accepted.Version || replayed.Plan.Research.Mode != "template" {
		t.Fatal("replay consulted the changed catalog", err)
	}
	queued, err := f.s.Action(ctx, f.team, p.ID, accepted.Version, "generate")
	if err != nil || queued.Status != "queued" || queued.Plan.Research.Mode != "template" {
		t.Fatal("generation lost the previously accepted identity", err)
	}
}
