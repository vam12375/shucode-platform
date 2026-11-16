package media

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/templatelibrary"
)

func TestFormulaTestCatalogIsolationAndFrozenInstructions(t *testing.T) {
	regular, err := VideoTemplateDefinitions()
	if err != nil || len(regular) != 60 {
		t.Fatalf("production catalog: %d %v", len(regular), err)
	}
	tests, err := VideoFormulaTestDefinitions()
	if err != nil || len(tests) != 32 {
		t.Fatalf("test catalog: %d %v", len(tests), err)
	}
	for i, item := range tests {
		id := "auto"
		if i > 0 {
			id = fmt.Sprintf("F%02d", i)
		}
		if item.Experiment.FormulaID != id || !strings.Contains(item.Experiment.Instructions, "## 八、封装主提示词") || len(item.VerifiedModelIDs) != 0 {
			t.Fatal("incomplete formula", id)
		}
	}
	tests[0].Experiment.Instructions = "mutated"
	fresh, _ := VideoFormulaTestDefinitions()
	if fresh[0].Experiment.Instructions == "mutated" {
		t.Fatal("catalog alias")
	}
	f := setup(t)
	ctx := context.Background()
	public, err := f.s.VideoFormulaTestTemplates(ctx)
	if err != nil || len(public) != 32 || public[0].Experiment.Instructions != "" {
		t.Fatal("overview must expose metadata only", err)
	}
	public[0].Experiment.FormulaID = "mutated"
	b := Brief{Mode: "auto", ResearchPolicy: "disabled", Product: "fixture", AssetIDs: []string{"a"}, Platforms: []string{}, ReferenceURLs: []string{}, VideoModel: "v", Resolution: "720p", AspectRatio: "16:9", Duration: 30, ReviewPlan: true, TemplateID: fresh[31].ID, TemplateVersion: fresh[31].Version, ReferenceMode: "multimodal", CreativeExpansion: "related"}
	models, _ := f.s.Repo.Models(ctx)
	// 夹具模型显式声明全能参考；不根据名称推测能力。
	models[0].Adapter = "ark"
	models[0].UpstreamModel = "doubao-seedance-2-0-260128"
	models[0].Durations = []int{5, 6}
	snapshot, err := f.s.freezeTemplate(ctx, b, models)
	if err != nil || snapshot == nil || snapshot.Template.Experiment.Instructions == "" {
		t.Fatal("freeze", err)
	}
	before, _ := json.Marshal(snapshot)
	repo := templatelibrary.Repository{Store: f.s.Repo.Store}
	seeds := []templatelibrary.Entry{{ID: templatelibrary.VideoID(b.TemplateID), Kind: "video", Source: "builtin-video", Enabled: true}}
	entries, err := repo.List(ctx, seeds)
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err = repo.Save(ctx, seeds, entries[0].ID, templatelibrary.Mutation{ExpectedVersion: entries[0].ExpectedVersion, Enabled: &disabled}, false); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.freezeTemplate(ctx, b, models); err == nil {
		t.Fatal("disabled test admitted")
	}
	after, _ := json.Marshal(snapshot)
	if string(before) != string(after) {
		t.Fatal("old snapshot changed")
	}
	prod, err := f.s.VideoTemplates(ctx)
	if err != nil || len(prod) != 60 {
		t.Fatal("test toggle altered production", err)
	}
}

func TestFormulaTestBriefAndPlanRejectFirstFrameGeneration(t *testing.T) {
	templates, err := VideoFormulaTestDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	template := templates[31]
	b := Brief{Mode: "auto", Duration: 30, AssetIDs: []string{"a"}, AspectRatio: "9:16", ReferenceMode: "multimodal", CreativeExpansion: "related", ResearchPolicy: "disabled", ReviewPlan: true, TemplateID: template.ID, TemplateVersion: template.Version}
	if _, err = validateTemplateSelection(b, templates); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Brief){func(b *Brief) { b.ReferenceMode = "frames" }, func(b *Brief) { b.ImageModel = "image" }, func(b *Brief) { b.Duration = 15 }, func(b *Brief) { b.GenerationPolicy = "whole-first" }, func(b *Brief) { b.AssetIDs = []string{"a", "b", "c", "d"} }} {
		invalid := b
		mutate(&invalid)
		if _, err = validateTemplateSelection(invalid, templates); err == nil {
			t.Fatal("invalid test brief accepted")
		}
	}
	p := Plan{}
	for _, delivery := range []int{5, 6, 5, 5, 5, 4} {
		p.Shots = append(p.Shots, ShotPlan{Duration: max(5, delivery), Creative: &ShotCreative{Source: "asset", AssetIDs: []string{"a"}, InFrame: 0, OutFrame: delivery * 30}})
	}
	snapshot := &TemplateSnapshot{Template: template}
	if err = validateFormulaTestPlan(b, p, snapshot); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Plan){func(p *Plan) { p.Shots[0].Duration = 15 }, func(p *Plan) { p.Shots[0].ImagePrompt = "redraw" }, func(p *Plan) { p.Shots[0].AssetID = "a" }, func(p *Plan) { p.Shots[0].Creative.OutFrame = 180 }} {
		raw, _ := json.Marshal(p)
		var invalid Plan
		if err = json.Unmarshal(raw, &invalid); err != nil {
			t.Fatal(err)
		}
		mutate(&invalid)
		if err = validateFormulaTestPlan(b, invalid, snapshot); err == nil {
			t.Fatal("invalid test plan accepted")
		}
		if err = validateFormulaTestPlan(b, invalid, nil); err != nil {
			t.Fatal("production plan affected")
		}
	}
	b.AssetIDs = append(b.AssetIDs, "b")
	if validateFormulaTestPlan(b, p, snapshot) == nil {
		t.Fatal("omitted reference accepted")
	}
}

func TestFormulaTestModelRequiresShortMultimodalGeneration(t *testing.T) {
	templates, err := VideoFormulaTestDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	b := Brief{VideoModel: "v", AssetIDs: []string{"a"}, ReferenceMode: "multimodal"}
	m := Model{ID: "v", Kind: "video", Adapter: "ark", UpstreamModel: "doubao-seedance-2-0-260128", Durations: []int{5}}
	for _, seconds := range []int{5, 6} {
		m.Durations = []int{seconds}
		if err := validateFormulaTestModel(b, templates[0], []Model{m}); err != nil {
			t.Fatalf("supported %ds model rejected: %v", seconds, err)
		}
	}
	m.Durations = []int{4, 10, 15}
	if err := validateFormulaTestModel(b, templates[0], []Model{m}); err == nil {
		t.Fatal("model without 5/6 second generation accepted")
	}
	if err := validateFormulaTestModel(b, VideoTemplate{}, []Model{m}); err != nil {
		t.Fatal("regular template affected", err)
	}
	m.Durations = []int{5}
	m.UpstreamModel = "unknown-model"
	if err := validateFormulaTestModel(b, templates[0], []Model{m}); err == nil {
		t.Fatal("model without multimodal capability accepted")
	}
}
