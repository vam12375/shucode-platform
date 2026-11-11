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

func brandIPSceneRecipe(t *testing.T) VideoTemplate {
	t.Helper()
	definitions, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := findVideoTemplate(definitions, "brand-ip-mini-drama", "2.1.0")
	if err != nil {
		t.Fatal("scene rewrite must have a distinct version", err)
	}
	return recipe
}

// 2.0.0 的成片只有一个摄影棚、一个机位，观看时场景单薄。2.1.0 把场景与镜头写进配方，
// 并让台词只落在三个核心镜头：这些约束必须在任何压缩时长下都保得住，所以锁在核心镜头上。
func TestBrandIPSceneRecipeKeepsScenesAndDialogueInCoreBeats(t *testing.T) {
	recipe := brandIPSceneRecipe(t)
	if len(recipe.Beats) != 12 || len(recipe.VerifiedModelIDs) != 0 || recipe.DurationRange != (TemplateDurationRange{Min: 15, Max: 30}) {
		t.Fatal("rewrite changed the beat count, duration range or claimed production verification")
	}
	dialogue, sceneChanges := []int{}, []int{}
	for i, beat := range recipe.Beats {
		if strings.HasPrefix(beat.Sound, "台词：") {
			dialogue = append(dialogue, i)
			if beat.Tier != TemplateTierCore {
				t.Fatalf("beat %d carries dialogue but can be dropped when the cut is shortened", i+1)
			}
		}
		if strings.Contains(beat.Action, "硬切换景") {
			sceneChanges = append(sceneChanges, i)
			if beat.Tier != TemplateTierCore {
				t.Fatalf("beat %d changes scene but can be dropped, leaving the short cut in one set", i+1)
			}
		}
	}
	if len(dialogue) != 3 {
		t.Fatalf("expected exactly three dialogue beats (entrance, turn, sign-off), got %v", dialogue)
	}
	if len(sceneChanges) != 2 {
		t.Fatalf("expected two scene changes for three scenes, got %v", sceneChanges)
	}
	// 15 秒版只保留核心镜头：三个场景与三句台词都必须留下。
	kept, seconds, ok := planTemplateTiming(timingBeats(recipe.Beats), recipe.DurationRange, 15)
	if !ok || len(kept) != 6 {
		t.Fatalf("15 second cut: ok=%v kept=%v seconds=%v", ok, kept, seconds)
	}
	for _, index := range append(dialogue, sceneChanges...) {
		found := false
		for _, k := range kept {
			found = found || k == index
		}
		if !found {
			t.Fatalf("15 second cut dropped beat %d that carries a scene change or dialogue", index+1)
		}
	}
	// 配方正文要把场景、道具、景别节奏与禁文字一并传给规划器，缺一就会退回单一场景。
	raw, _ := json.Marshal(recipe)
	for _, requirement := range []string{"至多三个场景", "无文字的道具", "同一件道具", "相邻两镜", "不超过4秒", "合计不超过3句", "1到1.5秒", "不出现可读文字"} {
		if !strings.Contains(string(raw), requirement) {
			t.Errorf("missing scene or shot requirement: %s", requirement)
		}
	}
	// 旧写法的约束会让规划器回到一个棚子拍到底，不能再出现在正文里。
	for _, removed := range []string{"每镜只有主角与一个主题物", "背景取素材背景色或同色调的简洁场景", "或固定机位等待表演", "固定机位，"} {
		if strings.Contains(string(raw), removed) {
			t.Errorf("single-set constraint still present: %s", removed)
		}
	}
}

func TestBrandIPSceneRecipeFreezesNewVersionAndReplaysOldSnapshot(t *testing.T) {
	recipe := brandIPSceneRecipe(t)
	f := setup(t)
	ctx := context.Background()
	b := Brief{Mode: "auto", ResearchPolicy: "disabled", Product: "fixture", AssetIDs: []string{"a"}, Platforms: []string{}, ReferenceURLs: []string{}, VideoModel: "v", Resolution: "720p", AspectRatio: "9:16", Duration: 15, ReviewPlan: true, TemplateID: recipe.ID, TemplateVersion: recipe.Version}
	var project *Project
	for _, seconds := range []int{15, 20} {
		b.Duration = seconds
		p, err := f.s.Create(ctx, f.team, f.user, "", uuid.NewString(), b)
		if err != nil {
			t.Fatal("production no longer accepts the recipe", err)
		}
		if p.TemplateSnapshot == nil || !reflect.DeepEqual(p.TemplateSnapshot.Template.Beats, recipe.Beats) || len(p.TemplateSnapshot.Skills) != 1 {
			t.Fatal("new order did not freeze the scene recipe and its director skill")
		}
		project = p
	}
	// 技能随制作单冻结：新单拿到的是硬切分镜与三句台词版本，而不是旧的“0到15秒连续”写法。
	skill := project.TemplateSnapshot.Skills[0]
	for _, requirement := range []string{"动作弧（硬切分镜）", "全片至多三句", "至多三个", "标识卡"} {
		if !strings.Contains(skill.Content, requirement) {
			t.Errorf("frozen director skill is missing %q", requirement)
		}
	}
	if strings.Contains(skill.Content, "按秒分镜，如“0到3秒”") {
		t.Error("frozen director skill still teaches the single continuous take")
	}
	raw, err := os.ReadFile("testdata/brand-ip-mini-drama-v2.0.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var legacy VideoTemplate
	if err = json.Unmarshal(raw, &legacy); err != nil || legacy.Version != "2.0.0" || legacy.ID != recipe.ID {
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
		t.Fatal("old order cannot replay after the recipe upgrade", err)
	}
	after, _ := json.Marshal(replayed.TemplateSnapshot)
	if string(before) != string(after) || replayed.Brief.TemplateVersion != "2.0.0" {
		t.Fatal("replay replaced the frozen 2.0.0 recipe")
	}
	if _, err = f.s.Create(ctx, f.team, f.user, "", uuid.NewString(), b); err == nil {
		t.Fatal("new order silently kept the retired 2.0.0 version")
	}
}
