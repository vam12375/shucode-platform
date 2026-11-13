package media

import (
	"strings"
	"testing"
)

// 样片把尾段当作完成态停留，逐项约束目录与末拍，防止只修黏土而遗漏其他模板。
func TestEveryTemplateUsesTheWholeTimeline(t *testing.T) {
	recipes, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(recipes) != 60 {
		t.Fatalf("incomplete catalog: %d", len(recipes))
	}
	for _, recipe := range recipes {
		t.Run(recipe.ID, func(t *testing.T) {
			version := "3.0.1"
			if recipe.ID == "brand-kv-unfold" {
				version = "2.1.1"
			}
			if recipe.Version != version || len(recipe.VerifiedModelIDs) != 0 {
				t.Fatal("changed recipe reused an old version or verification")
			}
			last := recipe.Beats[len(recipe.Beats)-1]
			for field, text := range map[string]string{
				"description": recipe.Description.ZH,
				"motion":      recipe.Style.Motion,
				"ending":      last.Action,
				"quality":     strings.Join(recipe.QualityChecks, "\n"),
			} {
				if !strings.Contains(text, "每秒") || !strings.Contains(text, "0.5秒") {
					t.Errorf("%s lacks active timeline and bounded settling: %s", field, text)
				}
			}
			if !strings.Contains(recipe.Description.EN, "every second") {
				t.Error("English description omits active timeline")
			}
			for _, phrase := range []string{"配乐", "音效", "清晰可听", "用户明确"} {
				if !strings.Contains(recipe.Style.Sound, phrase) {
					t.Errorf("sound design missing %s", phrase)
				}
			}
			sounds := map[string]bool{}
			for _, beat := range recipe.Beats {
				sounds[beat.Sound] = true
				if strings.Contains(beat.Sound, "无旁白") || !strings.Contains(beat.Sound, "匹配素材") {
					t.Error("blanket no-voice placeholder overrides sound design")
				}
				for _, removed := range []string{"停留至少1.5秒", "停稳至少1.5秒", "完成任务后的收纳或停留", "只用于停留"} {
					if strings.Contains(beat.Action+beat.Purpose+beat.Camera, removed) {
						t.Errorf("conflicting idle direction: %s", removed)
					}
				}
			}
			if len(sounds) < 3 {
				t.Error("sound has no opening, development and resolution")
			}
			// 秒数结构仍由既有算法唯一分配，动态内容不能改变时间轴总量。
			for target := recipe.DurationRange.Min; target <= recipe.DurationRange.Max; target++ {
				_, seconds, ok := planTemplateTiming(timingBeats(recipe.Beats), recipe.DurationRange, target)
				if !ok { // 既有核心数可能使短档不适用，不扩大模板能力。
					continue
				}
				total := 0
				for _, value := range seconds {
					total += value
				}
				if total != target {
					t.Fatalf("%d seconds planned as %d", target, total)
				}
			}
		})
	}
}
