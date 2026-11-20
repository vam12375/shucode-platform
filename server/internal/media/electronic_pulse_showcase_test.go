package media

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// 白棚和工坊的支持拍只延长镜内行程。压短时所有核心镜仍能依次执行，到第三轮粒子回落结束。
func TestElectronicPulseShowcaseKeepsSixScenesThroughLastPulse(t *testing.T) {
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

	type scene struct {
		name     string
		cues     []string
		soundCue string
	}
	scenes := []scene{
		{"白棚巡航", []string{"悬空间隙", "回正急推", "读侧口", "横扫背纹", "绕控制键", "从背面飞退", "顶沿遮住远侧"}, "反拉和回正急推"},
		{"微缩工坊", []string{"台前缓推", "前景机械臂", "完整外壳", "后墙仪表", "加速"}, "工具掠过"},
		{"螺旋绕线", []string{"线头露边", "第二圈", "独立悬空光丝", "后圈被壳遮住", "连接侧"}, "线头起势"},
		{"同轴对位", []string{"沿轴靠近", "与开口重合", "F434插稳", "外侧对齐停住", "保留细缝"}, "光丝对齐停住"},
		{"飞溅慢转", []string{"水幕外扩", "画面平面内", "逆时针慢转", "越过水平", "暖光回扫"}, "机身滚过水平"},
		{"黑台脉冲", []string{"实体圆台", "落回台面", "F530–542", "F552–564", "F574–586", "F590剪止"}, "F577亮击在上升途中"},
	}
	sceneForBeat := make([]int, len(recipe.Beats))
	var coreIndices []int
	for i, beat := range recipe.Beats {
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
				t.Fatalf("beat %d must extend the existing white studio or workshop shot", i+1)
			}
			continue
		}
		// 多张正面图也可能看不到接口。可替换的动作和声音须写在核心拍，短版才会交给导演。
		if !strings.Contains(beat.Sound, scenes[index].soundCue) {
			t.Errorf("core scene %s lost sound cue %s", name, scenes[index].soundCue)
		}
		for _, cue := range scenes[index].cues {
			if !strings.Contains(beat.Action, cue) {
				t.Fatalf("core scene %s lost %s", name, cue)
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
