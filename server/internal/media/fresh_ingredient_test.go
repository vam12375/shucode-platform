package media

import (
	"strings"
	"testing"
)

// 食材细节不能把“清洗/切分/试吃”设成固定动作，也不能让镜头补全冒充产地、口感或新鲜度证据。
func TestFreshIngredientRecipeUsesCategoryFitAndGroundedVoice(t *testing.T) {
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := findVideoTemplate(templates, "fresh-ingredient", "3.0.6")
	if err != nil {
		t.Fatal(err)
	}
	if len(recipe.Beats) != 12 || recipe.NominalSeconds != 36 {
		t.Fatalf("unexpected ingredient story structure: beats=%d seconds=%d", len(recipe.Beats), recipe.NominalSeconds)
	}
	if !strings.Contains(recipe.Description.ZH, "不是必选动作") || !strings.Contains(recipe.Description.ZH, "适配") {
		t.Error("description must make ingredient handling optional and category-appropriate")
	}
	for _, phrase := range []string{"甜度", "营养", "新鲜时长", "完整口语句", "关键词", "shots[].prompt"} {
		if !strings.Contains(recipe.Style.Narration, phrase) {
			t.Errorf("narration is missing %q", phrase)
		}
	}
	for i, beat := range recipe.Beats {
		if !strings.Contains(beat.Sound, "匹配素材") || !strings.Contains(beat.Sound, "同拍") {
			t.Errorf("beat %d is missing sound/visual anchoring", i+1)
		}
		if beat.Tier == TemplateTierCore && !strings.Contains(beat.Sound, "口播线") {
			t.Errorf("core beat %d is disconnected from the spoken line", i+1)
		}
	}
	for _, phrase := range []string{"真实水接触", "不为镜头强行加工", "不杜撰配方"} {
		if !strings.Contains(strings.Join(recipe.QualityChecks, "\n")+recipe.Style.Look+strings.Join(recipe.RequiredFacts, "\n"), phrase) {
			t.Errorf("ingredient recipe is missing factual/visual guardrail %q", phrase)
		}
	}
}
