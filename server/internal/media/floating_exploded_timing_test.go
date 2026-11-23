package media

import (
	"slices"
	"strings"
	"testing"
)

func TestFloatingExplodedCutsKeepOneAssemblySequence(t *testing.T) {
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	const id = "floating-exploded-showcase"
	recipe, err := findVideoTemplate(templates, id, expectedRecipeVersion(id))
	if err != nil {
		t.Fatal(err)
	}
	// 短版过去会删掉归位，30秒版又在段界重新亮相。按节拍的实际出入点核对拆合顺序。
	want := []string{"认形", "寻缝", "展开", "对照", "归位", "回扣"}
	for target := 12; target <= 30; target++ {
		kept, seconds, ok := planTemplateTiming(timingBeats(recipe.Beats), recipe.DurationRange, target)
		if !ok {
			t.Fatalf("%ds: cannot plan recipe", target)
		}
		var stages []string
		elapsed := 0
		for k, index := range kept {
			beat := recipe.Beats[index]
			stage, _, _ := strings.Cut(beat.Purpose, "｜")
			end := elapsed + seconds[k]
			if seconds[k] < 2 {
				t.Fatalf("%ds: %s is too short: %v", target, stage, seconds)
			}
			if beat.Tier == TemplateTierCore {
				stages = append(stages, stage)
			}
			if target == 30 && (stage == "展开" && end != 15 || stage == "对照" && elapsed != 15) {
				t.Errorf("15+15s cut: %s at [%d,%d) misses the open seam", stage, elapsed, end)
			}
			elapsed = end
		}
		if elapsed != target || !slices.Equal(stages, want) {
			t.Fatalf("%ds: got %d seconds with core %v", target, elapsed, stages)
		}
	}
}
