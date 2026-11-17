package media

import (
	"slices"
	"testing"
)

func TestFloatingExplodedShortCutsKeepSeparationAndReassembly(t *testing.T) {
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := findVideoTemplate(templates, "floating-exploded-showcase", expectedRecipeVersion("floating-exploded-showcase"))
	if err != nil {
		t.Fatal(err)
	}
	// 根因：归位曾是 support，短版压缩会删掉它，只剩展开或翻转。
	// 展开、原路归位和整机回扣必须在所有可用时长中按原顺序连续成立。
	for target := recipe.DurationRange.Min; target <= recipe.DurationRange.Max; target++ {
		kept, seconds, ok := planTemplateTiming(timingBeats(recipe.Beats), recipe.DurationRange, target)
		if !ok {
			t.Fatalf("%d-second template cannot be planned", target)
		}
		separation, reassembly, ending := slices.Index(kept, 8), slices.Index(kept, 10), slices.Index(kept, 11)
		if separation < 0 || reassembly <= separation || ending <= reassembly {
			t.Fatalf("%d-second cut lost the separation/reassembly sequence: %v", target, kept)
		}
		for _, duration := range seconds {
			if duration < 2 {
				t.Fatalf("%d-second cut has an unreadable beat window: %v", target, seconds)
			}
		}
	}
}
