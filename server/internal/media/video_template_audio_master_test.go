package media

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// 产品样片被规划为无人声与低音量持续音；目录不能再用“需要时”替代主动介绍和逐句同拍。
func TestRegularTemplatesCarryProductSpeechThroughShortCuts(t *testing.T) {
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, recipe := range templates {
		t.Run(recipe.ID, func(t *testing.T) {
			if recipe.Style == nil {
				t.Fatal("missing audio style")
			}
			for _, phrase := range []string{"产品介绍", "同拍", "用户明确", "锁定", "shots[].prompt"} {
				if !strings.Contains(recipe.Style.Narration, phrase) {
					t.Errorf("narration missing %s", phrase)
				}
			}
			if !strings.Contains(recipe.Style.Sound, "正常说话") || !strings.Contains(strings.Join(recipe.QualityChecks, "\n"), "同拍") {
				t.Error("audibility or semantic synchronization missing")
			}
			for field, value := range map[string]string{"sound": recipe.Style.Sound, "narration": recipe.Style.Narration} {
				if utf8.RuneCountInString(value) > 600 {
					t.Errorf("%s exceeds client schema", field)
				}
			}
			for _, beat := range recipe.Beats {
				if utf8.RuneCountInString(beat.Sound) > 400 || !strings.Contains(beat.Sound, "同拍") {
					t.Error("beat lacks speech-to-visual anchor or exceeds client schema")
				}
			}
			for seconds := recipe.DurationRange.Min; seconds <= recipe.DurationRange.Max; seconds++ {
				kept, _, ok := planTemplateTiming(timingBeats(recipe.Beats), recipe.DurationRange, seconds)
				if !ok {
					continue
				}
				introduced := false
				for _, index := range kept {
					beat := recipe.Beats[index]
					introduced = introduced || (beat.Tier == TemplateTierCore && strings.Contains(beat.Sound, "介绍"))
				}
				if !introduced {
					t.Fatalf("%ds cut dropped all product introduction anchors", seconds)
				}
			}
		})
	}
}

// 两份同版本样片的台词被模型几乎逐字照读，却是一拍一句的名词清单：narration 的“每个介绍分句绑定同拍”
// “自然短句”与核心拍“自主写准确分句……说完再切换”共同鼓励逐拍点名。目录必须改为口播线取舍、
// 完整口语句、关键词同拍（同段内一句可跨相邻拍）、表演对象与重音，并在质量检查中朗读自查。
func TestRegularTemplatesDirectNaturalSpokenIntroduction(t *testing.T) {
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, recipe := range templates {
		t.Run(recipe.ID, func(t *testing.T) {
			if recipe.Style == nil {
				t.Fatal("missing audio style")
			}
			for _, phrase := range []string{"口播线", "完整口语句", "少讲一个特点", "一句最多并列两个名词", "关键词", "跨相邻拍", "末句"} {
				if !strings.Contains(recipe.Style.Narration, phrase) {
					t.Errorf("narration missing %s", phrase)
				}
			}
			for _, phrase := range []string{"重音", "平板念稿"} {
				if !strings.Contains(recipe.Style.Sound, phrase) {
					t.Errorf("sound missing performance direction %s", phrase)
				}
			}
			raw, _ := json.Marshal(recipe)
			for _, removed := range []string{"每个介绍分句", "自然短句", "一句承担一个要点", "自主写准确分句", "说完再切换", "每个分句与对象同拍", "逐句与展示部位同拍", "逐句与可见对象同拍"} {
				if strings.Contains(string(raw), removed) {
					t.Errorf("per-beat clause scaffold remains: %s", removed)
				}
			}
			for i, beat := range recipe.Beats {
				if beat.Tier == TemplateTierCore && strings.Contains(beat.Sound, "介绍") && !strings.Contains(beat.Sound, "口播线") {
					t.Errorf("core beat %d introduction is not tied to the spoken line", i+1)
				}
			}
			if !strings.Contains(strings.Join(recipe.QualityChecks, "\n"), "朗读") {
				t.Error("quality checks lack read-aloud naturalness check")
			}
		})
	}
}
