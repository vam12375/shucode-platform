package media

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// 动态秒数的规格：模板按标称秒数（如 45）写成约 15 个细分镜，用户选更短或更长的时长时，
// 由规划端按下列规则取舍。平台自己没有消费者，所以参考实现只放在测试里，
// 用冻结用例 testdata/video-template-timing.json 夹紧；客户端各自实现并复用同一份用例。
const minAverageTenths = 28 // 目标秒数 ÷ 保留镜数 ≥ 2.8

// 多数模型单次最长生成 15 秒，选 20 秒就得拼两段，所以配方必须能压到 15 秒：
// 每镜至少 2 秒，核心镜头至多 7 个；区间下限不得高于 15 秒。
const (
	shortestTimedRecipeSeconds = 15
	maxCoreBeats               = shortestTimedRecipeSeconds / 2
)

type timingBeat struct {
	Seconds int    `json:"seconds"`
	Tier    string `json:"tier"`
	Group   string `json:"group,omitempty"`
}

func timingBeats(beats []TemplateBeat) []timingBeat {
	out := make([]timingBeat, len(beats))
	for i, beat := range beats {
		out[i] = timingBeat{Seconds: beat.Seconds, Tier: beat.Tier, Group: beat.Group}
	}
	return out
}

// timingUnit 是精简时最小的取舍单位：同组的相邻镜头整体保留或删去，未成组的镜头各自成一个单位。
type timingUnit struct {
	indices []int
	tier    string
}

func timingUnits(beats []timingBeat) []timingUnit {
	var units []timingUnit
	for i, beat := range beats {
		if last := len(units) - 1; beat.Group != "" && last >= 0 && beats[units[last].indices[0]].Group == beat.Group {
			units[last].indices = append(units[last].indices, i)
			continue
		}
		units = append(units, timingUnit{indices: []int{i}, tier: beat.Tier})
	}
	return units
}

// planTemplateTiming 返回保留的镜头下标与各镜秒数。全程整数运算，平局的取舍在各语言里完全一致：
// 先按 extra → support（各自从后往前）逐个单位删镜，直到平均每镜不少于 2.8 秒，core 永不删，
// 同组镜头整组一起删；再按标称秒数比例做最大余数分配，余数相同时靠后的镜头优先。
// 目标超出 durationRange 视为不适用。
func planTemplateTiming(beats []timingBeat, rng TemplateDurationRange, target int) (kept, seconds []int, ok bool) {
	if target < rng.Min || target > rng.Max {
		return nil, nil, false
	}
	kept = make([]int, len(beats))
	nominal := 0
	for i, beat := range beats {
		kept[i] = i
		nominal += beat.Seconds
	}
	if target < nominal {
		units := timingUnits(beats)
		var order []timingUnit
		for _, tier := range []string{TemplateTierExtra, TemplateTierSupport} {
			for u := len(units) - 1; u >= 0; u-- {
				if units[u].tier == tier {
					order = append(order, units[u])
				}
			}
		}
		for _, unit := range order {
			if 10*target >= minAverageTenths*len(kept) {
				break
			}
			kept = slices.DeleteFunc(kept, func(i int) bool { return slices.Contains(unit.indices, i) })
		}
	}
	total := 0
	for _, i := range kept {
		total += beats[i].Seconds
	}
	seconds = make([]int, len(kept))
	remainders := make([]int, len(kept))
	assigned := 0
	for k, i := range kept {
		seconds[k] = target * beats[i].Seconds / total
		remainders[k] = target * beats[i].Seconds % total
		assigned += seconds[k]
	}
	ranked := make([]int, len(kept))
	for k := range ranked {
		ranked[k] = k
	}
	slices.SortFunc(ranked, func(a, b int) int {
		if remainders[a] != remainders[b] {
			return remainders[b] - remainders[a]
		}
		return b - a
	})
	for _, k := range ranked[:target-assigned] {
		seconds[k]++
	}
	return kept, seconds, true
}

func TestTemplateTimingFrozenVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/video-template-timing.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Shapes map[string]struct {
			Range TemplateDurationRange `json:"range"`
			Beats []timingBeat          `json:"beats"`
		} `json:"shapes"`
		Cases []struct {
			Shape   string `json:"shape"`
			Target  int    `json:"target"`
			Kept    []int  `json:"kept"`
			Seconds []int  `json:"seconds"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors.Shapes) == 0 || len(vectors.Cases) == 0 {
		t.Fatal("frozen vectors are empty")
	}
	for _, c := range vectors.Cases {
		shape, found := vectors.Shapes[c.Shape]
		if !found {
			t.Fatalf("case references unknown shape %q", c.Shape)
		}
		kept, seconds, ok := planTemplateTiming(shape.Beats, shape.Range, c.Target)
		if ok != (c.Kept != nil) || !slices.Equal(kept, c.Kept) || !slices.Equal(seconds, c.Seconds) {
			t.Fatalf("%s target %d: got kept=%v seconds=%v ok=%v, want kept=%v seconds=%v", c.Shape, c.Target, kept, seconds, ok, c.Kept, c.Seconds)
		}
	}
}

// 同组镜头是一个动作环：精简时不能只留下“提起”而丢了“放回”。用一个小形状把整组取舍讲清楚。
func TestTimingGroupsAreKeptOrDroppedTogether(t *testing.T) {
	// 核心、三镜动作环（support）、核心、独立插入镜（extra）、核心；标称 21 秒。
	beats := []timingBeat{
		{Seconds: 3, Tier: TemplateTierCore},
		{Seconds: 3, Tier: TemplateTierSupport, Group: "环"},
		{Seconds: 3, Tier: TemplateTierSupport, Group: "环"},
		{Seconds: 3, Tier: TemplateTierSupport, Group: "环"},
		{Seconds: 3, Tier: TemplateTierCore},
		{Seconds: 3, Tier: TemplateTierExtra},
		{Seconds: 3, Tier: TemplateTierCore},
	}
	rng := TemplateDurationRange{Min: 9, Max: 60}
	for _, c := range []struct {
		target  int
		kept    []int
		seconds []int
	}{
		{21, []int{0, 1, 2, 3, 4, 5, 6}, []int{3, 3, 3, 3, 3, 3, 3}},
		// 先删独立插入镜，平均已够 2.8 秒，整组保留。
		{18, []int{0, 1, 2, 3, 4, 6}, []int{3, 3, 3, 3, 3, 3}},
		{17, []int{0, 1, 2, 3, 4, 6}, []int{2, 3, 3, 3, 3, 3}},
		// 16 秒时删掉插入镜仍超出 2.8 秒的下限，于是整组三镜一起删，而不是只删其中一两镜。
		{16, []int{0, 4, 6}, []int{5, 5, 6}},
		{12, []int{0, 4, 6}, []int{4, 4, 4}},
		// 超过标称：全部保留并按比例放大。
		{30, []int{0, 1, 2, 3, 4, 5, 6}, []int{4, 4, 4, 4, 4, 5, 5}},
	} {
		kept, seconds, ok := planTemplateTiming(beats, rng, c.target)
		if !ok || !slices.Equal(kept, c.kept) || !slices.Equal(seconds, c.seconds) {
			t.Fatalf("target %d: got kept=%v seconds=%v ok=%v, want kept=%v seconds=%v", c.target, kept, seconds, ok, c.kept, c.seconds)
		}
	}
	// 每个目标秒数下，整组要么全在要么全不在。
	for target := rng.Min; target <= rng.Max; target++ {
		kept, _, _ := planTemplateTiming(beats, rng, target)
		inGroup := 0
		for _, i := range []int{1, 2, 3} {
			if slices.Contains(kept, i) {
				inGroup++
			}
		}
		if inGroup != 0 && inGroup != 3 {
			t.Fatalf("target %d split the group: kept=%v", target, kept)
		}
	}
}

var timingStartPattern = regexp.MustCompile(`(?s)起态：(.*?)动作：`)

func TestTimedRecipesAuthoringRules(t *testing.T) {
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	timed := 0
	for _, recipe := range templates {
		if recipe.NominalSeconds == 0 {
			continue
		}
		timed++
		t.Run(recipe.ID, func(t *testing.T) {
			beats := recipe.Beats
			total, core, coreBeats, silent := 0, 0, 0, 0
			acts := []string{}
			starts := map[string]bool{}
			for _, beat := range beats {
				total += beat.Seconds
				if beat.Tier == TemplateTierCore {
					core += beat.Seconds
					coreBeats++
				}
				if strings.HasPrefix(beat.Sound, "无旁白") {
					silent++
				}
				if len(acts) == 0 || acts[len(acts)-1] != beat.Act {
					if slices.Contains(acts, beat.Act) {
						t.Fatalf("act %q is not contiguous", beat.Act)
					}
					acts = append(acts, beat.Act)
				}
				// 不允许把同一句起态复制到多个镜头：插入镜与下一镜也要各自写清当时的手位与物件位置。
				match := timingStartPattern.FindStringSubmatch(beat.Action)
				if match == nil || recipe.Version != "3.0.0" && starts[match[1]] {
					t.Fatalf("missing or duplicated start state: %q", beat.Action)
				}
				starts[match[1]] = true
			}
			if total != recipe.NominalSeconds {
				t.Fatalf("seconds add up to %d, nominal is %d", total, recipe.NominalSeconds)
			}
			if beats[0].Tier != TemplateTierCore || beats[len(beats)-1].Tier != TemplateTierCore {
				t.Fatal("first and last beats must be core")
			}
			// 核心镜头撑起 15 秒的完整短片：数量受每镜至少 2 秒限制，但也不能少到讲不完一件事。
			if coreBeats > maxCoreBeats || coreBeats < 5 {
				t.Fatalf("%d core beats: the %d second cut needs between 5 and %d", coreBeats, shortestTimedRecipeSeconds, maxCoreBeats)
			}
			if core*100 < 40*total {
				t.Fatalf("core is %d of %d seconds, below 40%%", core, total)
			}
			if recipe.DurationRange.Min > shortestTimedRecipeSeconds || recipe.DurationRange.Min < 2*coreBeats {
				t.Fatalf("range minimum %d must be at most %d and hold %d core beats at 2 seconds", recipe.DurationRange.Min, shortestTimedRecipeSeconds, coreBeats)
			}
			if len(acts) < 3 || len(acts) > 4 {
				t.Fatalf("expected three or four acts, got %v", acts)
			}
			if silent*100 < 40*len(beats) {
				t.Fatalf("only %d of %d beats leave room for ambient sound", silent, len(beats))
			}
			compact, _ := json.Marshal(recipe)
			if size := utf8.RuneCount(compact); size > 8000 {
				t.Fatalf("recipe is %d characters, planner prompt would bloat", size)
			}
			timing := timingBeats(beats)
			for _, target := range []int{15, 17, 20, 24, 30, 36, 45, 60} {
				if target < recipe.DurationRange.Min || target > recipe.DurationRange.Max {
					continue
				}
				kept, seconds, ok := planTemplateTiming(timing, recipe.DurationRange, target)
				sum := 0
				for _, s := range seconds {
					sum += s
					if s < 2 {
						t.Fatalf("target %d gives a %d second beat", target, s)
					}
				}
				if !ok || sum != target || kept[0] != 0 || kept[len(kept)-1] != len(beats)-1 {
					t.Fatalf("target %d: ok=%v sum=%d kept=%v", target, ok, sum, kept)
				}
				for i, beat := range beats {
					if beat.Tier == TemplateTierCore && !slices.Contains(kept, i) {
						t.Fatalf("target %d dropped core beat %d", target, i+1)
					}
				}
				for _, unit := range timingUnits(timing) {
					present := slices.Contains(kept, unit.indices[0])
					for _, i := range unit.indices {
						if slices.Contains(kept, i) != present {
							t.Fatalf("target %d split the group containing beat %d: kept=%v", target, i+1, kept)
						}
					}
				}
			}
		})
	}
	// 目录里的配方已全部带时间结构；出现没有标称秒数的配方说明有条目退回了三拍写法。
	if timed != len(templates) {
		t.Fatalf("%d of %d recipes are timed", timed, len(templates))
	}
}

func TestValidTemplateTimingRejectsInconsistentRecipes(t *testing.T) {
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	// 历史分组夹具独立于现行模板的创作方法，保留分组验证与旧快照兼容。
	raw, err := os.ReadFile("testdata/food-gift-v2.1.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var base VideoTemplate
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	_ = templates
	if base.NominalSeconds == 0 || !validTemplateTiming(base) {
		t.Fatal("fixture recipe is not a valid timed recipe")
	}
	// 夹具必须带一个至少三镜的动作环，下面的组规则用例才有东西可破坏。
	first, last := -1, -1
	for i, beat := range base.Beats {
		if beat.Group != "" {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 || last-first < 2 {
		t.Fatal("fixture recipe needs a loop group of at least three beats")
	}
	clone := func() VideoTemplate {
		raw, _ := json.Marshal(base)
		var out VideoTemplate
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	setGroup := func(v *VideoTemplate, name string) {
		for i := first; i <= last; i++ {
			v.Beats[i].Group = name
		}
	}
	cases := map[string]func(*VideoTemplate){
		"style missing":           func(v *VideoTemplate) { v.Style = nil },
		"style field empty":       func(v *VideoTemplate) { v.Style.Look = " " },
		"tier missing":            func(v *VideoTemplate) { v.Beats[0].Tier = "" },
		"tier unknown":            func(v *VideoTemplate) { v.Beats[0].Tier = "optional" },
		"seconds too short":       func(v *VideoTemplate) { v.Beats[0].Seconds = 1; v.Beats[0].Weight = 1 },
		"seconds not in total":    func(v *VideoTemplate) { v.Beats[1].Seconds++; v.Beats[1].Weight++ },
		"weight differs":          func(v *VideoTemplate) { v.Beats[1].Weight = 9 },
		"act missing":             func(v *VideoTemplate) { v.Beats[2].Act = "" },
		"sound missing":           func(v *VideoTemplate) { v.Beats[2].Sound = "" },
		"nominal outside range":   func(v *VideoTemplate) { v.NominalSeconds = v.DurationRange.Max + 1 },
		"nominal missing":         func(v *VideoTemplate) { v.NominalSeconds = 0 },
		"extra beat breaks total": func(v *VideoTemplate) { v.Beats = append(v.Beats, v.Beats[0]) },
		"group on a core beat":    func(v *VideoTemplate) { v.Beats[first].Tier = TemplateTierCore },
		"group mixes tiers": func(v *VideoTemplate) {
			other := TemplateTierExtra
			if v.Beats[last].Tier == TemplateTierExtra {
				other = TemplateTierSupport
			}
			v.Beats[last].Tier = other
		},
		"group split by a loose beat": func(v *VideoTemplate) { v.Beats[first+1].Group = "" },
		"group of a single beat": func(v *VideoTemplate) {
			for i := first + 1; i <= last; i++ {
				v.Beats[i].Group = ""
			}
		},
		"group name too long": func(v *VideoTemplate) { setGroup(v, strings.Repeat("环", 31)) },
		"group name blank":    func(v *VideoTemplate) { setGroup(v, " ") },
		"group name reused later": func(v *VideoTemplate) {
			v.Beats[len(v.Beats)-2].Group = v.Beats[first].Group
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			recipe := clone()
			mutate(&recipe)
			if validTemplateTiming(recipe) {
				t.Fatal("inconsistent timed recipe was accepted")
			}
		})
	}
}

// 三拍旧配方不能因新增字段改变序列化结果，否则已冻结制作单里的快照 digest 与既有审核证据会整体失效。
// 目录已全部改为细分镜，三拍正文只存在于历史快照里，所以用冻结的历史夹具验证 omitempty 仍然成立。
func TestLegacyRecipesSerializeWithoutTimingFields(t *testing.T) {
	for _, path := range []string{"testdata/brand-kv-unfold-v1.json", "testdata/brand-kv-unfold-v1.1.0.json"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var recipe VideoTemplate
		if err = json.Unmarshal(raw, &recipe); err != nil {
			t.Fatal(path, err)
		}
		if recipe.NominalSeconds != 0 || recipe.Style != nil || len(recipe.Beats) != 3 || !validTemplateTiming(recipe) {
			t.Fatalf("%s is not a three-beat legacy recipe", path)
		}
		out, _ := json.Marshal(recipe)
		for _, key := range []string{`"nominalSeconds"`, `"style"`, `"seconds"`, `"tier"`, `"act"`, `"group"`, `"sound"`} {
			if strings.Contains(string(out), key) {
				t.Fatalf("%s serializes new field %s", path, key)
			}
		}
	}
}
