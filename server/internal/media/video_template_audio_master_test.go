package media

import (
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
