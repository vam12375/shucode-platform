package media

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// 简介也会进入规划输入。只改 narration 会让分镜或双语简介继续提示模型逐镜念稿。
func TestRegularTemplatesRemoveShotTourSpeechScaffold(t *testing.T) {
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	quota := regexp.MustCompile(`(?:每\s*\d+\s*秒[^。；”"]{0,24}(?:个|字|句)|每秒\s*\d+\s*(?:字|词)|\d+\s*秒默认.{0,12}(?:句|字))`)
	for _, recipe := range templates {
		t.Run(recipe.ID, func(t *testing.T) {
			if recipe.Style == nil {
				t.Fatal("missing audio style")
			}
			raw, err := json.Marshal(recipe)
			if err != nil {
				t.Fatal(err)
			}
			for _, old := range []string{"先定一条口播线", "先看、再、翻过来、最后", "末句回到整体", "一句最多并列两个名词", "讲两三个有据特点", "covering a few grounded features", "每个介绍分句", "自主写准确分句", "说完再切换", "产品开场用一句"} {
				if strings.Contains(string(raw), old) {
					t.Errorf("obsolete speech direction: %s", old)
				}
			}
			if quota.Match(raw) {
				t.Error("speech quota remains in the catalog")
			}
			for _, phrase := range []string{"对谁说", "同拍", "同一生成段", "不逐镜", "留白", "用户明确", "锁定script", "shots[].prompt"} {
				if !strings.Contains(recipe.Style.Narration, phrase) {
					t.Errorf("narration is missing %q", phrase)
				}
			}
			if recipe.Family == "brand" {
				want := "不默认加旁白"
				if recipe.ID == "brand-ip-mini-drama" || recipe.ID == "contrast-brand-mini-story" || recipe.ID == "clay-style-brand-world" {
					want = "不加商品旁白"
				}
				if !strings.Contains(recipe.Style.Narration, want) {
					t.Errorf("brand/character goal must avoid forced sales narration: missing %q", want)
				}
			}
			for _, phrase := range []string{"正常说话", "重音", "平板念稿"} {
				if !strings.Contains(recipe.Style.Sound, phrase) {
					t.Errorf("sound is missing %q", phrase)
				}
			}
			if utf8.RuneCountInString(recipe.Style.Sound) > 600 || utf8.RuneCountInString(recipe.Style.Narration) > 600 {
				t.Error("audio style exceeds client schema")
			}
			for i, beat := range recipe.Beats {
				if !strings.HasPrefix(beat.Sound, "声音（模型原声）：匹配素材") || utf8.RuneCountInString(beat.Sound) > 400 || strings.Contains(beat.Sound, "口播线") || strings.Contains(beat.Sound, "产品用自然口语介绍") {
					t.Errorf("beat %d lacks usable native sound or prescribes a spoken beat: %s", i+1, beat.Sound)
				}
			}
			if !strings.Contains(strings.Join(recipe.QualityChecks, "\n"), "朗读") {
				t.Error("missing read-aloud naturalness check")
			}
		})
	}
}

func TestTemplateAudioRegressionsByCreativeGoal(t *testing.T) {
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   string
		cues []string
	}{
		// 对应香水静物、橙子与饼干盒样片；品牌/氛围另补合成用例，不借样片冒称新版听审。
		{"product-detail-reassembly", []string{"默认无人声", "shots[].narration 留空", "仅在用户明确要求讲解", "要念的锁定script"}},
		{"product-texture", []string{"默认", "材质等级", "轮廓"}},
		{"beauty-packaging", []string{"开启", "不猜材质"}},
		{"beauty-texture", []string{"看得见的铺展", "不推断配方"}},
		{"fresh-ingredient", []string{"甜度", "适配"}},
		{"food-serving", []string{"桌边招呼", "接触声"}},
		{"food-gift", []string{"格数", "不编"}},
		{"brand-ip-mini-drama", []string{"角色对白", "不加商品旁白"}},
		{"contrast-brand-mini-story", []string{"角色对白", "不加商品旁白"}},
		{"brand-kv-unfold", []string{"音乐", "不默认加旁白"}},
		{"scent-memory-film", []string{"不默认加旁白", "香调"}},
		{"scent-atmosphere", []string{"不默认加旁白", "ASMR"}},
		{"drink-pour", []string{"不默认加旁白", "液流"}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			recipe, err := findVideoTemplate(templates, tc.id, expectedRecipeVersion(tc.id))
			if err != nil {
				t.Fatal(err)
			}
			for _, phrase := range tc.cues {
				if !strings.Contains(recipe.Style.Narration, phrase) {
					t.Errorf("narration is missing the domain choice %q", phrase)
				}
			}
			if !strings.Contains(recipe.Style.Narration, "默认根据素材写产品介绍") && strings.Contains(recipe.Style.Sound, "产品默认有清楚介绍") {
				t.Error("mixing guidance overrides the template's choice of whether to speak")
			}
		})
	}
}
