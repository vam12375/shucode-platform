package media

import (
	"slices"
	"strings"
	"testing"
)

func TestFoodServingKeepsTheSamePortionFromPickupToFirstBite(t *testing.T) {
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := findVideoTemplate(templates, "food-serving", expectedRecipeVersion("food-serving"))
	if err != nil {
		t.Fatal(err)
	}
	// 旧配方只要求笼统承接前态，样片从满托切到装盘时丢了份量，尾段也没交付到食客面前。
	// 取离、落点、两端份量与最后递送都在核心链上，压缩只能删不改变状态的插镜。
	var core []int
	for index, beat := range recipe.Beats {
		if beat.Tier == TemplateTierCore {
			core = append(core, index)
		}
	}
	for _, stage := range []struct {
		index int
		cue   string
	}{
		{0, "落点还没有接收它"},
		{2, "尚未离开原位置"},
		{4, "原处仅减少已取份量"},
		{6, "先接触并获得支撑，再松夹或撤手"},
		{8, "原处减少多少，接盘增加多少"},
		{11, "将盘或盛器轻送到可取的位置"},
	} {
		if stage.index >= len(recipe.Beats) || !slices.Contains(core, stage.index) || !strings.Contains(recipe.Beats[stage.index].Action, stage.cue) {
			t.Fatalf("missing core action %q at beat %d", stage.cue, stage.index+1)
		}
	}
	for duration := recipe.DurationRange.Min; duration <= recipe.DurationRange.Max; duration++ {
		kept, seconds, ok := planTemplateTiming(timingBeats(recipe.Beats), recipe.DurationRange, duration)
		if !ok || duration == 15 && !slices.Equal(kept, core) {
			t.Fatalf("%ds does not preserve the intended short-cut structure: %v", duration, kept)
		}
		for _, index := range core {
			position := slices.Index(kept, index)
			if position < 0 || seconds[position] < 2 {
				t.Fatalf("%ds loses readable core beat %d: %v / %v", duration, index+1, kept, seconds)
			}
		}
	}
	if !strings.Contains(recipe.Beats[6].Sound, "着盘同拍") || !strings.Contains(recipe.Beats[6].Sound, "避开旁白重音") {
		t.Fatal("landing no longer reserves space for the synchronized contact sound")
	}
}
