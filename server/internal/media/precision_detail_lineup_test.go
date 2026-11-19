package media

import (
	"slices"
	"strings"
	"testing"
)

// 平台配时按整秒；相邻两个原镜共用一个核心拍，改变总时长仍要能从接缝读到产品列阵。
func TestPrecisionDetailLineupRetainsTenViews(t *testing.T) {
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	const id = "precision-detail-lineup"
	recipe, err := findVideoTemplate(templates, id, expectedRecipeVersion(id))
	if err != nil {
		t.Fatal(err)
	}
	if recipe.NominalSeconds != 15 || recipe.DurationRange.Min != 12 || recipe.DurationRange.Max != 30 {
		t.Fatalf("range %+v, nominal %d", recipe.DurationRange, recipe.NominalSeconds)
	}
	cues := []struct {
		pair, first, second string
	}{
		{"闭合与圆键", "F0–42", "F43硬切"},
		{"孔网与界面", "F76", "F116硬切"},
		{"斜槽与轴心", "F164", "F200硬切"},
		{"按键与对缝", "F251", "F289硬切"},
		{"侧头与列阵", "F326", "F374硬切"},
	}
	if len(recipe.Beats) != len(cues) {
		t.Fatalf("expected five pairs, got %d", len(recipe.Beats))
	}
	for i, cue := range cues {
		b := recipe.Beats[i]
		name, _, _ := strings.Cut(b.Purpose, "｜")
		if name != cue.pair || b.Tier != TemplateTierCore {
			t.Fatalf("pair %d: %s / %s", i+1, name, b.Tier)
		}
		body := strings.Join([]string{b.Action, b.Camera}, "\n")
		_, after, found := strings.Cut(body, cue.first)
		if !found || !strings.Contains(after, cue.second) {
			t.Fatalf("%s lost the reference order %s → %s", name, cue.first, cue.second)
		}
	}
	// 逐帧解码确认最后三帧仍是陈列；画面索引为 0 至 449，排他出点为 450。
	if !strings.Contains(recipe.Beats[4].Action, "阵列显示至F449后结束") || !strings.Contains(recipe.Style.Motion, "[F0,F450)") {
		t.Fatal("expected the lineup to include the last displayed frame")
	}
	for target := 12; target <= 30; target++ {
		kept, secs, ok := planTemplateTiming(timingBeats(recipe.Beats), recipe.DurationRange, target)
		if !ok || !slices.Equal(kept, []int{0, 1, 2, 3, 4}) {
			t.Fatalf("%ds lost a reference pair: %v", target, kept)
		}
		total := 0
		for _, s := range secs {
			if s < 2 {
				t.Fatalf("%ds has an unusable pair: %v", target, secs)
			}
			total += s
		}
		if total != target || target == 12 && !slices.Equal(secs, []int{2, 2, 3, 2, 3}) || target == 15 && !slices.Equal(secs, []int{3, 3, 3, 2, 4}) || target == 30 && !slices.Equal(secs, []int{6, 6, 6, 4, 8}) {
			t.Fatalf("expected %ds, got %v", target, secs)
		}
	}
}
