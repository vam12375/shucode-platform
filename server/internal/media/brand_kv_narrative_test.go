package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func brandNarrativeRecipe(t *testing.T) VideoTemplate {
	t.Helper()
	definitions, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := findVideoTemplate(definitions, "brand-kv-unfold", "1.1.0")
	if err != nil {
		t.Fatal("narrative rewrite must have a distinct version", err)
	}
	return recipe
}

func TestBrandKVNarrativeHasExecutableCommunicationContract(t *testing.T) {
	recipe := brandNarrativeRecipe(t)
	if recipe.MinAssets != 1 || recipe.DurationRange != (TemplateDurationRange{Min: 12, Max: 60}) || recipe.NominalSeconds != 0 || len(recipe.Beats) != 3 || len(recipe.VerifiedModelIDs) != 0 {
		t.Fatal("rewrite changed short-video compatibility or claimed production verification")
	}
	for i, role := range []string{"观看理由", "可见证据", "回答开头"} {
		if !strings.Contains(recipe.Beats[i].Purpose, role) {
			t.Fatalf("beat %d has no distinct information role: %s", i, recipe.Beats[i].Purpose)
		}
	}
	// 原配方只约束运动和留白；新正文必须将信息落点、实际通道与事实边界一起传给规划器。
	raw, _ := json.Marshal(recipe)
	for _, requirement := range []string{"发布者", "目标观众", "观看情境", "shots[].prompt", "新增信息", "未确认", "动线", "原生台词", "字幕", "留白不是结论", "全部选图"} {
		if !strings.Contains(string(raw), requirement) {
			t.Errorf("missing planning requirement: %s", requirement)
		}
	}
	if !reflect.DeepEqual(recipe.SkillIDs, []string{"curated:graphic-motion-director", "curated:brand-visual-director"}) {
		t.Fatal("rewrite changed shared skill dependencies")
	}
}

func TestBrandKVNarrativeFreezesNewVersionAndReplaysOldSnapshot(t *testing.T) {
	recipe := brandNarrativeRecipe(t)
	f := setup(t)
	ctx := context.Background()
	b := Brief{Mode: "auto", ResearchPolicy: "disabled", Product: "fixture", AssetIDs: []string{"a"}, Platforms: []string{}, ReferenceURLs: []string{}, VideoModel: "v", Resolution: "720p", AspectRatio: "16:9", Duration: 15, ReviewPlan: true, TemplateID: recipe.ID, TemplateVersion: recipe.Version}
	var project *Project
	for _, seconds := range []int{12, 15} {
		b.Duration = seconds
		p, err := f.s.Create(ctx, f.team, f.user, "", uuid.NewString(), b)
		if err != nil {
			t.Fatal("short production no longer accepts the recipe", err)
		}
		if p.TemplateSnapshot == nil || !reflect.DeepEqual(p.TemplateSnapshot.Template.Beats, recipe.Beats) || len(p.TemplateSnapshot.Skills) != 2 {
			t.Fatal("new order did not freeze the complete narrative and existing skills")
		}
		project = p
	}
	raw, err := os.ReadFile("testdata/brand-kv-unfold-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var legacy VideoTemplate
	if err = json.Unmarshal(raw, &legacy); err != nil || legacy.Version != "1.0.0" {
		t.Fatal("invalid historical recipe fixture", err)
	}
	// 保存旧制作单当时已冻结的正文，模拟目录升版后的继续，而不是用当前目录重建旧单。
	oldSnapshot := cloneTemplateSnapshot(project.TemplateSnapshot)
	oldSnapshot.Template = legacy
	oldSnapshot.Digest = ""
	raw, _ = json.Marshal(oldSnapshot)
	sum := sha256.Sum256(raw)
	oldSnapshot.Digest = hex.EncodeToString(sum[:])
	b.TemplateVersion = legacy.Version
	project, err = f.s.Mutate(ctx, f.team, project.ID, project.Version, func(p *Project) error {
		p.Brief = b
		p.TemplateSnapshot = oldSnapshot
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(project.TemplateSnapshot)
	replayed, err := f.s.Create(ctx, f.team, f.user, "", project.ID, b)
	if err != nil {
		t.Fatal("old order cannot replay after recipe upgrade", err)
	}
	after, _ := json.Marshal(replayed.TemplateSnapshot)
	if string(before) != string(after) || replayed.Brief.TemplateVersion != "1.0.0" {
		t.Fatal("replay replaced the frozen narrative")
	}
	if _, err = f.s.Create(ctx, f.team, f.user, "", uuid.NewString(), b); err == nil {
		t.Fatal("new order silently upgraded the requested old version")
	}
}

func TestBrandKVNarrativeDoesNotInheritOldReview(t *testing.T) {
	recipe := brandNarrativeRecipe(t)
	f := setup(t)
	ctx := context.Background()
	models, err := f.s.Repo.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := f.s.resolveTemplateCatalog(ctx, models)
	if err != nil {
		t.Fatal(err)
	}
	digest := ""
	for _, entry := range resolved {
		if entry.snapshot.Template.ID == recipe.ID {
			digest = entry.snapshot.Digest
		}
	}
	if digest == "" {
		t.Fatal("missing current recipe digest")
	}
	var reviews []TemplateReview
	for _, model := range models {
		if model.Kind == "video" {
			// 即使摘要和模型签名吻合，旧版本审核也不能认证新版正文。
			reviews = append(reviews, TemplateReview{TemplateReviewInput: TemplateReviewInput{TemplateID: recipe.ID, TemplateVersion: "1.0.0", Approved: true, Score: 100}, RecipeDigest: digest, ModelID: model.ID, ModelSignature: templateModelSignature(model)})
		}
	}
	if len(reviews) == 0 {
		t.Fatal("missing fixture video model")
	}
	raw, _ := json.Marshal(reviews)
	if err = f.s.Repo.Store.SetConfigValue(ctx, TemplateReviewsKey, raw); err != nil {
		t.Fatal(err)
	}
	catalog, err := f.s.VideoTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	current, err := findVideoTemplate(catalog, recipe.ID, recipe.Version)
	if err != nil || len(current.VerifiedModelIDs) != 0 {
		t.Fatal("new narrative inherited previous-version production evidence", err)
	}
}
