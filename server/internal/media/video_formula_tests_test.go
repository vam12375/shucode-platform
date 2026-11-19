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
	if err != nil || len(regular) != 64 {
		t.Fatalf("production catalog: %d %v", len(regular), err)
	}
	tests, err := VideoFormulaTestDefinitions()
	if err != nil || len(tests) != 31 {
		t.Fatalf("test catalog: %d %v", len(tests), err)
	}
	for i, item := range tests {
		id := "auto"
		if i > 0 {
			id = fmt.Sprintf("F%02d", i)
		}
		if item.Experiment.FormulaID != id || !strings.Contains(item.Experiment.Instructions, "## 每镜落实记录与验收") || len(item.VerifiedModelIDs) != 0 {
			t.Fatal("incomplete formula", id)
		}
		if item.Experiment.SuiteID != productAdDirectorSuite || item.Version != productAdDirectorVersion || !strings.Contains(item.Experiment.Instructions, "F31保留为制作方式，不增算第31类创意") {
			t.Fatal("director source or fallback missing", id)
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
	if err != nil || len(public) != 31 || public[0].Experiment.Instructions != "" {
		t.Fatal("overview must expose metadata only", err)
	}
	public[0].Experiment.FormulaID = "mutated"
	b := Brief{Mode: "auto", ResearchPolicy: "disabled", Product: "fixture", AssetIDs: []string{"a"}, Platforms: []string{}, ReferenceURLs: []string{}, VideoModel: "v", Resolution: "720p", AspectRatio: "16:9", Duration: 30, ReviewPlan: true, TemplateID: fresh[30].ID, TemplateVersion: fresh[30].Version, GenerationPolicy: "whole-first", ReferenceMode: "multimodal", CreativeExpansion: "related"}
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
	if err != nil || len(prod) != 64 {
		t.Fatal("test toggle altered production", err)
	}
}

func TestFormulaTestBriefAndPlanRejectFirstFrameGeneration(t *testing.T) {
	templates, err := VideoFormulaTestDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	template := templates[30]
	template.Version = legacyFormulaVersion
	template.Experiment = &TemplateExperiment{SuiteID: legacyFormulaSuite, FormulaID: "F31", Instructions: "frozen legacy fixture"}
	templates = []VideoTemplate{template}
	b := Brief{Mode: "auto", Duration: 30, AssetIDs: []string{"a"}, AspectRatio: "9:16", ReferenceMode: "multimodal", CreativeExpansion: "related", ResearchPolicy: "disabled", ReviewPlan: true, TemplateID: template.ID, TemplateVersion: template.Version}
	if _, err = validateTemplateSelection(b, templates); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Brief){func(b *Brief) { b.ReferenceMode = "frames" }, func(b *Brief) { b.ImageModel = "image" }, func(b *Brief) { b.Duration = 15 }, func(b *Brief) { b.AssetIDs = nil }} {
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

func TestProductAdDirectorRequiresWholeFirstAndPreservesLegacyPlan(t *testing.T) {
	templates, err := VideoFormulaTestDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	template := templates[1]
	b := Brief{Mode: "auto", Duration: 30, AssetIDs: []string{"a"}, AspectRatio: "9:16", ReferenceMode: "multimodal", CreativeExpansion: "related", ResearchPolicy: "disabled", ReviewPlan: true, TemplateID: template.ID, TemplateVersion: template.Version}
	if _, err := validateTemplateSelection(b, templates); err == nil {
		t.Fatal("new director template admitted legacy short-request mode")
	}
	b.GenerationPolicy = "whole-first"
	if _, err := validateTemplateSelection(b, templates); err != nil {
		t.Fatal("whole-first director brief rejected", err)
	}
	p := Plan{Shots: []ShotPlan{{Duration: 30, Creative: &ShotCreative{Source: "asset", AssetIDs: []string{"a"}, OutFrame: 900}}}}
	snapshot := &TemplateSnapshot{Template: template}
	if err := validateFormulaTestPlan(b, p, snapshot); err != nil {
		t.Fatal("six editing units must not require six paid requests", err)
	}
	for _, mutate := range []func(*Plan){
		func(p *Plan) { p.Shots[0].ImagePrompt = "redraw" },
		func(p *Plan) { p.Shots[0].AssetID = "a" },
		func(p *Plan) { p.Shots[0].Creative.AssetIDs = []string{"other"} },
	} {
		raw, _ := json.Marshal(p)
		var invalid Plan
		_ = json.Unmarshal(raw, &invalid)
		mutate(&invalid)
		if validateFormulaTestPlan(b, invalid, snapshot) == nil {
			t.Fatal("director plan bypassed reference-only boundary")
		}
	}
	b.GenerationPolicy = ""
	if validateFormulaTestPlan(b, p, snapshot) == nil {
		t.Fatal("director snapshot accepted old short-request execution")
	}
}

func TestFormulaTestModelRequiresShortMultimodalGeneration(t *testing.T) {
	templates, err := VideoFormulaTestDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	legacy := templates[0]
	legacy.Version = legacyFormulaVersion
	legacy.Experiment = &TemplateExperiment{SuiteID: legacyFormulaSuite, FormulaID: "auto", Instructions: "frozen legacy fixture"}
	b := Brief{VideoModel: "v", AssetIDs: []string{"a"}, ReferenceMode: "multimodal"}
	m := Model{ID: "v", Kind: "video", Adapter: "ark", UpstreamModel: "doubao-seedance-2-0-260128", Durations: []int{5}}
	for _, seconds := range []int{5, 6} {
		m.Durations = []int{seconds}
		if err := validateFormulaTestModel(b, legacy, []Model{m}); err != nil {
			t.Fatalf("supported %ds model rejected: %v", seconds, err)
		}
	}
	m.Durations = []int{4, 10, 15}
	if err := validateFormulaTestModel(b, legacy, []Model{m}); err == nil {
		t.Fatal("model without 5/6 second generation accepted")
	}
	if err := validateFormulaTestModel(b, VideoTemplate{}, []Model{m}); err != nil {
		t.Fatal("regular template affected", err)
	}
	m.Durations = []int{5}
	m.UpstreamModel = "unknown-model"
	if err := validateFormulaTestModel(b, legacy, []Model{m}); err == nil {
		t.Fatal("model without multimodal capability accepted")
	}
}

func TestFormulaImagesFollowModelCapacityInsteadOfThreeImageLimit(t *testing.T) {
	templates, err := VideoFormulaTestDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	m := seedance25CapacityModel()
	b := Brief{Mode: "auto", Duration: 30, AspectRatio: "9:16", ReferenceMode: "multimodal", CreativeExpansion: "related", ResearchPolicy: "disabled", ReviewPlan: true, VideoModel: m.ID, GenerationPolicy: "whole-first", ImageAnalysisFirst: true}
	for _, count := range []int{4, 9, 30} {
		b.AssetIDs, _ = capacityAssets(count)
		for _, recipe := range templates {
			b.TemplateID, b.TemplateVersion = recipe.ID, recipe.Version
			if _, err := validateTemplateSelection(b, templates); err != nil {
				t.Fatalf("%s rejected %d images within model capacity: %v", recipe.ID, count, err)
			}
			if err := validateFormulaTestModel(b, recipe, []Model{m}); err != nil {
				t.Fatalf("%s model admission rejected %d images: %v", recipe.ID, count, err)
			}
		}
	}
	// 去掉套件三张限制不代表绕过真实容量；非分段参考仍不能超过本型号的单次上限。
	b.ImageAnalysisFirst = false
	b.AssetIDs, _ = capacityAssets(31)
	if err := validateFormulaTestModel(b, templates[len(templates)-1], []Model{m}); err == nil {
		t.Fatal("actual model image limit was bypassed")
	}
}

func TestFormulaQuickUsesWholeModelDurationAndSameStagedTask(t *testing.T) {
	templates, err := VideoFormulaTestDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{15, 30} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			f, p, observation := stagedImageFixture(t)
			ctx := context.Background()
			durations := []int{15, 15}
			if limit == 30 {
				durations = []int{30}
			}
			b, plan, models := wholeDurationFixture(durations, limit)
			b.ImageAnalysisFirst = true
			b.ResearchPolicy = "disabled"
			b.ReferenceURLs = []string{}
			for i := range plan.Shots {
				plan.Shots[i].Creative.Source = "asset"
				plan.Shots[i].AssetID = ""
				plan.Shots[i].ImagePrompt = ""
			}
			b.ReferenceMode = "multimodal"
			b.ReviewPlan = false
			b.TemplateID, b.TemplateVersion = templates[30].ID, templates[30].Version
			models[0].Adapter = "ark"
			models[0].UpstreamModel = "doubao-seedance-2-5-260628"
			if _, err = validateTemplateSelection(b, templates); err != nil {
				t.Fatal("quick brief", err)
			}
			snapshot, err := f.s.freezeTemplate(ctx, b, models)
			if err != nil {
				t.Fatal("quick freeze", err)
			}
			if err = validateFormulaTestPlan(b, plan, snapshot, models...); err != nil {
				t.Fatal("whole plan", err)
			}
			p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error { v.Brief, v.Models, v.TemplateSnapshot = b, models, snapshot; return nil })
			if err != nil {
				t.Fatal(err)
			}
			a := Analysis{Kind: "images", TaskID: "same-task", InputID: "images-input", WorkspacePath: "/fixture"}
			p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, a)
			if err != nil {
				t.Fatal("images", err)
			}
			p, err = f.s.SetImageAnalysis(ctx, f.team, p.ID, p.Version, a.InputID, observation)
			if err != nil || p.Status != "image_review" {
				t.Fatal("image review", err)
			}
			a.Kind, a.InputID = "planning", "planning-input"
			p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, a, &TemplateSelection{TemplateID: b.TemplateID, TemplateVersion: b.TemplateVersion})
			if err != nil || p.Analysis.TaskID != "same-task" || p.TemplateSnapshot.Digest != snapshot.Digest {
				t.Fatal("planning lost snapshot", err)
			}
			// 此用例验证原计划owner的准入和排队；SetPlan的ffmpeg环境探测由既有后期套件覆盖。
			p, err = f.s.acceptAnalysisPlan(ctx, f.team, p.ID, p.Version, a.InputID, plan)
			if err != nil || p.Status != "queued" || len(p.Shots) != len(durations) {
				t.Fatal("auto queue", err)
			}
		})
	}
}
