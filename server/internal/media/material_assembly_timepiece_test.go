package media

import (
	"strings"
	"testing"
)

// 平台按整秒落拍。短版也须保留两次材料成形和中间的结构装配，否则只剩商品转台。
func TestMaterialAssemblyTimepieceKeepsReferenceSequence(t *testing.T) {
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	const id = "material-assembly-timepiece"
	recipe, err := findVideoTemplate(templates, id, expectedRecipeVersion(id))
	if err != nil {
		t.Fatal(err)
	}
	if recipe.NominalSeconds != 38 {
		t.Fatalf("reference cut duration = %d, want 38", recipe.NominalSeconds)
	}
	stages := []string{"色卡", "粉退露壳", "纵列", "合壳", "丝束展平", "品牌尾卡"}
	for target := 12; target <= 38; target++ {
		kept, seconds, ok := planTemplateTiming(timingBeats(recipe.Beats), recipe.DurationRange, target)
		if !ok || target == 38 && len(kept) != len(recipe.Beats) {
			t.Fatalf("%ds: reference sequence unavailable: %v", target, kept)
		}
		stage := 0
		for i, index := range kept {
			if seconds[i] < 2 {
				t.Fatalf("%ds: beat %d lasts only %ds", target, index+1, seconds[i])
			}
			if stage < len(stages) && strings.Contains(recipe.Beats[index].Purpose, stages[stage]) {
				stage++
			}
		}
		if stage < len(stages) {
			t.Fatalf("%ds: lost stage %q from %v", target, stages[stage], kept)
		}
	}
}
