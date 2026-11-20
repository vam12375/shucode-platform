package media

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// 旧配方把音箱样片的道具写进必拍场次，导致只有外观图的游戏机也会绕线、起声浪。
// 六场的时序仍须保留，可用的外观拍法也须留在压缩后实际下发的核心拍。
func TestElectronicPulseShowcaseKeepsAssetChoicesAcrossDurations(t *testing.T) {
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	const id = "electronic-pulse-showcase"
	recipe, err := findVideoTemplate(templates, id, expectedRecipeVersion(id))
	if err != nil {
		t.Fatal(err)
	}
	if recipe.NominalSeconds != 20 || recipe.DurationRange.Min != 12 || recipe.DurationRange.Max != 30 {
		t.Fatalf("unexpected duration range %+v, nominal %d", recipe.DurationRange, recipe.NominalSeconds)
	}

	scenes := []struct {
		name    string
		choices []string
	}{
		{"白棚巡航", nil},
		// 12 秒会删除全部延展拍；独立布景及切回实物的办法必须在核心拍里。
		{"结构层次", []string{"结构图", "完整外壳", "布景", "切回", "strict"}},
		{"特征引入", []string{"接口", "焦点"}},
		{"特征落点", []string{"接口", "焦点"}},
		{"明暗转面", []string{"防水", "干景"}},
		{"节拍收束", []string{"音箱", "游戏机", "轮廓"}},
	}
	labels := slices.Clone(recipe.Keywords)
	sceneForBeat := make([]int, len(recipe.Beats))
	var coreIndices []int
	for i, beat := range recipe.Beats {
		labels = append(labels, beat.Purpose, beat.Act)
		name, _, _ := strings.Cut(beat.Purpose, "｜")
		if beat.Tier == TemplateTierCore {
			coreIndices = append(coreIndices, i)
		}
		index := len(coreIndices) - 1
		if index < 0 || index >= len(scenes) || name != scenes[index].name {
			t.Fatalf("beat %d does not belong to its continuous scene: %s", i+1, name)
		}
		sceneForBeat[i] = index
		if beat.Tier != TemplateTierCore {
			if index > 1 || beat.Tier != TemplateTierSupport || beat.Act != recipe.Beats[coreIndices[index]].Act {
				t.Fatalf("beat %d must extend the existing cruise or detail shot", i+1)
			}
			continue
		}
		for _, cue := range scenes[index].choices {
			if !strings.Contains(beat.Action, cue) {
				t.Errorf("core scene %s lacks material choice %s", name, cue)
			}
		}
	}
	// 标题、段名和检索词直接用于选片，不能把某个有条件的演法又标成全片的通用卖点。
	for _, label := range labels {
		for _, prop := range []string{"机器人", "工坊", "机械臂", "绕线", "线缆", "光丝", "水冠", "飞溅", "声波", "同心纹"} {
			if strings.Contains(label, prop) {
				t.Errorf("unconditional label %q prescribes %s", label, prop)
			}
		}
	}
	if len(coreIndices) != len(scenes) {
		t.Fatalf("expected all six core scenes, got %v", coreIndices)
	}
	raw, err := json.Marshal(recipe)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ToLower(string(raw))
	for _, cue := range []string{"尾卡", "联系方式", "联系信息", "读字", "cta", "end card"} {
		if strings.Contains(body, cue) {
			t.Fatalf("product-only template includes closing text: %s", cue)
		}
	}

	expected := map[int][]int{
		12: {2, 2, 2, 2, 2, 2},
		15: {4, 2, 2, 2, 2, 3},
		20: {8, 4, 2, 2, 2, 2},
		30: {12, 6, 3, 3, 3, 3},
	}
	for target := recipe.DurationRange.Min; target <= recipe.DurationRange.Max; target++ {
		kept, seconds, ok := planTemplateTiming(timingBeats(recipe.Beats), recipe.DurationRange, target)
		if !ok || !slices.IsSorted(kept) {
			t.Fatalf("%ds lost scene order: %v", target, kept)
		}
		for _, index := range coreIndices {
			if !slices.Contains(kept, index) {
				t.Fatalf("%ds dropped core %s", target, recipe.Beats[index].Purpose)
			}
		}
		total := 0
		sceneSeconds := make([]int, len(scenes))
		for i, index := range kept {
			if seconds[i] < 2 {
				t.Fatalf("%ds gives an unusable beat: %v", target, seconds)
			}
			total += seconds[i]
			sceneSeconds[sceneForBeat[index]] += seconds[i]
		}
		if total != target || expected[target] != nil && !slices.Equal(sceneSeconds, expected[target]) {
			t.Fatalf("%ds produced %v", target, sceneSeconds)
		}
	}
}
