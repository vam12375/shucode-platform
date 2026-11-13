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
	// 2.1.1 保留主角与配时方法，修正静帧尾段并按素材匹配原声。
	recipe, err := findVideoTemplate(definitions, "brand-kv-unfold", "2.1.1")
	if err != nil {
		t.Fatal("hero-led rewrite must have a distinct version", err)
	}
	return recipe
}

// 1.1.0 只有三条原则性拍点，15 秒六图样片被规划成六次约 2 秒的平均轮播：暗光夜景开场、
// 方形海报整幅放进竖屏成了模糊衬底的画中画并被重画出错字、结尾落在甜品上。
// 2.0.0 改为带秒数的细分镜：主角开场、手部取用和回到主角都锁在核心镜头，任何压缩时长都保得住。
func TestBrandKVHeroRecipeKeepsHeroActionAndEndingInCoreBeats(t *testing.T) {
	recipe := brandNarrativeRecipe(t)
	if recipe.MinAssets != 1 || recipe.DurationRange != (TemplateDurationRange{Min: 12, Max: 60}) || recipe.NominalSeconds != 36 || len(recipe.Beats) != 12 || len(recipe.VerifiedModelIDs) != 0 {
		t.Fatal("rewrite changed short-video compatibility, beat count or claimed production verification")
	}
	core := []int{}
	for i, beat := range recipe.Beats {
		if beat.Tier == TemplateTierCore {
			core = append(core, i)
		}
	}
	// 主角开场、交代场所、手部取用、其余出品一镜带过、回到主角：五个核心镜头各有一个职责。
	if !reflect.DeepEqual(core, []int{0, 2, 4, 7, 11}) {
		t.Fatalf("core beats moved: %v", core)
	}
	for _, check := range []struct {
		beat   int
		phrase string
	}{{0, "主角满幅近景"}, {2, "环境线索"}, {4, "只露手"}, {7, "一镜内带过"}, {11, "回到主角"}} {
		if text := recipe.Beats[check.beat].Purpose + recipe.Beats[check.beat].Framing; !strings.Contains(text, check.phrase) {
			t.Errorf("core beat %d lost its job %q", check.beat+1, check.phrase)
		}
	}
	// 12 与 15 秒只留核心镜头且每镜不少于 2 秒；15 秒是五镜各 3 秒，不再平均轮播。
	for target, want := range map[int][]int{12: {2, 2, 2, 3, 3}, 15: {3, 3, 3, 3, 3}} {
		kept, seconds, ok := planTemplateTiming(timingBeats(recipe.Beats), recipe.DurationRange, target)
		if !ok || !reflect.DeepEqual(kept, core) || !reflect.DeepEqual(seconds, want) {
			t.Fatalf("%d second cut: ok=%v kept=%v seconds=%v", target, ok, kept, seconds)
		}
	}
	raw, _ := json.Marshal(recipe)
	for _, requirement := range []string{"唯一主角", "不做画中画", "按成片画幅满幅重现", "会被模型重画成错字", "不按图片张数平均分时长", "creative.assetIds 按图片在镜头里出现的先后排列", "不以配角、甜品、空镜或留白结尾", "不足2.5秒的轮播", "shots[].prompt", "不编造进店路线"} {
		if !strings.Contains(string(raw), requirement) {
			t.Errorf("missing planning requirement: %s", requirement)
		}
	}
	// 旧写法让规划器把扁平海报整体轻推、结尾留白等后期，正是样片的画中画与空结尾。
	for _, removed := range []string{"整幅轻推", "留出品牌落版", "落版留白"} {
		if strings.Contains(string(raw), removed) {
			t.Errorf("old poster-handling instruction still present: %s", removed)
		}
	}
	// 两个视觉技能要求扁平海报整体轻推、落版留出干净空间，与满幅重现和回到主角收尾冲突；改用卖点与质地导演。
	if !reflect.DeepEqual(recipe.SkillIDs, []string{"curated:product-proof-director", "curated:sensory-food-director"}) {
		t.Fatal("rewrite must use the proof and texture director skills")
	}
}

func TestBrandKVNarrativeFreezesNewVersionAndReplaysOldSnapshot(t *testing.T) {
	recipe := brandNarrativeRecipe(t)
	f := setup(t)
	ctx := context.Background()
	b := Brief{Mode: "auto", ResearchPolicy: "disabled", Product: "fixture", AssetIDs: []string{"a"}, Platforms: []string{}, ReferenceURLs: []string{}, VideoModel: "v", Resolution: "720p", AspectRatio: "16:9", Duration: 15, ReviewPlan: true, TemplateID: recipe.ID, TemplateVersion: recipe.Version}
	var project *Project
	for _, seconds := range []int{12, 15, 30} {
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
	// 历史版本都已有冻结的制作单：继续或重放必须保留当时的正文，而不是用当前目录重建。
	for _, fixture := range []struct{ path, version string }{{"testdata/brand-kv-unfold-v1.json", "1.0.0"}, {"testdata/brand-kv-unfold-v1.1.0.json", "1.1.0"}, {"testdata/brand-kv-unfold-v2.0.0.json", "2.0.0"}, {"testdata/brand-kv-unfold-v2.1.0.json", "2.1.0"}} {
		raw, err := os.ReadFile(fixture.path)
		if err != nil {
			t.Fatal(err)
		}
		var legacy VideoTemplate
		if err = json.Unmarshal(raw, &legacy); err != nil || legacy.Version != fixture.version || legacy.ID != recipe.ID {
			t.Fatal("invalid historical recipe fixture", fixture.path, err)
		}
		oldSnapshot := cloneTemplateSnapshot(project.TemplateSnapshot)
		oldSnapshot.Template = legacy
		oldSnapshot.Digest = ""
		raw, _ = json.Marshal(oldSnapshot)
		sum := sha256.Sum256(raw)
		oldSnapshot.Digest = hex.EncodeToString(sum[:])
		old := b
		old.TemplateVersion = legacy.Version
		saved, err := f.s.Mutate(ctx, f.team, project.ID, project.Version, func(p *Project) error {
			p.Brief = old
			p.TemplateSnapshot = oldSnapshot
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		project = saved
		before, _ := json.Marshal(project.TemplateSnapshot)
		replayed, err := f.s.Create(ctx, f.team, f.user, "", project.ID, old)
		if err != nil {
			t.Fatal("old order cannot replay after recipe upgrade", fixture.version, err)
		}
		after, _ := json.Marshal(replayed.TemplateSnapshot)
		if string(before) != string(after) || replayed.Brief.TemplateVersion != fixture.version {
			t.Fatal("replay replaced the frozen recipe", fixture.version)
		}
		if _, err = f.s.Create(ctx, f.team, f.user, "", uuid.NewString(), old); err == nil {
			t.Fatal("new order silently upgraded the requested old version", fixture.version)
		}
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
			reviews = append(reviews, TemplateReview{TemplateReviewInput: TemplateReviewInput{TemplateID: recipe.ID, TemplateVersion: "1.1.0", Approved: true, Score: 100}, RecipeDigest: digest, ModelID: model.ID, ModelSignature: templateModelSignature(model)})
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
