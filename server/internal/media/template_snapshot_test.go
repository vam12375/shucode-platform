package media

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/templatelibrary"
)

func TestTemplateSnapshotDesktopContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/template-snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot TemplateSnapshot
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	f := setup(t)
	entries, err := (templatelibrary.Repository{Store: f.s.Repo.Store}).List(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, skill := range snapshot.Skills {
		found := false
		for _, e := range entries {
			if e.ID == skill.ID && e.Content == skill.Content && e.PackageDigest == skill.PackageDigest {
				found = true
			}
		}
		if !found || !slices.Contains(snapshot.Template.SkillIDs, skill.ID) {
			t.Fatal("desktop fixture skill does not match platform")
		}
	}
}

func updateProductionSkill(t *testing.T, f *fixture, change func(*templatelibrary.Mutation, templatelibrary.Entry)) {
	t.Helper()
	repo := templatelibrary.Repository{Store: f.s.Repo.Store}
	entries, err := repo.List(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.ID != "curated:brand-visual-director" {
			continue
		}
		input := templatelibrary.Mutation{ExpectedVersion: e.ExpectedVersion}
		change(&input, e)
		if _, err = repo.Save(context.Background(), nil, e.ID, input, false); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatal("skill missing")
}

func TestProductionSkillSnapshotIsolationAndReplay(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := Brief{Mode: "auto", ResearchPolicy: "disabled", Product: "fixture", AssetIDs: []string{"a"}, Platforms: []string{}, ReferenceURLs: []string{}, VideoModel: "v", Resolution: "720p", AspectRatio: "16:9", Duration: 15, MaxCreditsMicro: 10_000_000, ReviewPlan: true, TemplateID: "black-gold-reveal", TemplateVersion: currentTemplateVersion(t, "black-gold-reveal")}
	p, err := f.s.Create(ctx, f.team, f.user, "", uuid.NewString(), b)
	if err != nil {
		t.Fatal(err)
	}
	if p.TemplateSnapshot == nil || len(p.TemplateSnapshot.Skills) != 1 {
		t.Fatal("skill not frozen")
	}
	before, _ := json.Marshal(p.TemplateSnapshot)
	view := p.View()
	view.TemplateSnapshot.Skills[0].Content = "mutated projection"
	view.TemplateSnapshot.Template.Beats[0].Action = "mutated projection"
	current, _ := json.Marshal(p.TemplateSnapshot)
	if string(before) != string(current) {
		t.Fatal("projection aliases project snapshot")
	}
	updateProductionSkill(t, f, func(input *templatelibrary.Mutation, e templatelibrary.Entry) {
		fields := e.Fields
		fields.Content += "\n本次修订使用新的构图方法。\n"
		input.Fields = &fields
	})
	replayed, err := f.s.Create(ctx, f.team, f.user, "", p.ID, b)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(replayed.TemplateSnapshot)
	if string(before) != string(after) {
		t.Fatal("idempotent replay refreshed skill")
	}
	next, err := f.s.Create(ctx, f.team, f.user, "", uuid.NewString(), b)
	if err != nil {
		t.Fatal(err)
	}
	if next.TemplateSnapshot.Digest == p.TemplateSnapshot.Digest {
		t.Fatal("new order missed content edit")
	}
	updateProductionSkill(t, f, func(input *templatelibrary.Mutation, _ templatelibrary.Entry) {
		enabled := false
		input.Enabled = &enabled
	})
	if _, err = f.s.Create(ctx, f.team, f.user, "", uuid.NewString(), b); err == nil {
		t.Fatal("disabled skill accepted by new order")
	}
	_, plan, _ := adaptivePlanFixture()
	plan.Research.TemplateID = b.TemplateID
	plan.Research.TemplateVersion = b.TemplateVersion
	if err = f.s.validateAdaptivePlan(ctx, b, plan, p.Models, p.TemplateSnapshot); err != nil {
		t.Fatalf("old order cannot continue: %v", err)
	}
	plan.Research.Mode = "template"
	if normalized := normalizeAutoPlanMetadata(b, plan, p.TemplateSnapshot); normalized.Research.Mode != "original" {
		t.Fatal("unreviewed draft promoted to verified template")
	}
	b.TemplateID, b.TemplateVersion = "", ""
	clean, err := f.s.Create(ctx, f.team, f.user, "", uuid.NewString(), b)
	if err != nil || clean.TemplateSnapshot != nil {
		t.Fatal("new original order inherited skills", err)
	}
}

func TestTemplateReviewCannotSurviveSkillContentChange(t *testing.T) {
	f, p, input := templateReviewFixture(t)
	ctx := context.Background()
	b := p.Brief
	b.TemplateID, b.TemplateVersion = "black-gold-reveal", currentTemplateVersion(t, "black-gold-reveal")
	snapshot, err := f.s.freezeTemplate(ctx, b, p.Models)
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Brief = b
		v.TemplateSnapshot = snapshot
		v.Plan.Research.TemplateID, v.Plan.Research.TemplateVersion = b.TemplateID, b.TemplateVersion
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	input.TemplateID, input.TemplateVersion = b.TemplateID, b.TemplateVersion
	if _, err = f.s.ReviewVideoTemplate(ctx, "root-fixture", input); err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		rows, err := f.s.VideoTemplates(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.ID == b.TemplateID && slices.Contains(row.VerifiedModelIDs, b.VideoModel) == want {
				return
			}
		}
		t.Fatalf("verification should be %v", want)
	}
	check(true)
	updateProductionSkill(t, f, func(input *templatelibrary.Mutation, e templatelibrary.Entry) {
		fields := e.Fields
		fields.Content += "\n不同的落版方法。\n"
		input.Fields = &fields
	})
	check(false)
	if _, err = f.s.ReviewVideoTemplate(ctx, "root-fixture", input); err != nil {
		t.Fatal(err)
	}
	check(false)
}
