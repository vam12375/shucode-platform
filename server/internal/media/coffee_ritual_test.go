package media

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// 旧配方只支持冲煮证据，成杯素材被改编成静物；台词又只写意图，短片最后只剩底噪。
// 核心镜必须同时承载动作、素材与可朗读的原生台词，且压缩不能挤掉说话和片尾停留的时间。
func TestCoffeeRitualKeepsActionAndAudibleStoryAtEveryDuration(t *testing.T) {
	// 精确秒数和旧三句台词留在历史快照，现行模板允许按新故事重新编排。
	raw, err := os.ReadFile("testdata/coffee-ritual-v2.2.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var recipe VideoTemplate
	if err := json.Unmarshal(raw, &recipe); err != nil {
		t.Fatal(err)
	}
	if len(recipe.VerifiedModelIDs) != 0 || recipe.MinAssets != 1 {
		t.Fatal("finished-cup recipe must accept one asset without claiming production verification")
	}
	voiced := map[int]string{0: "忙里，偷一小会儿。", 6: "慢一点，也很好。", 11: "这一刻，刚刚好。"}
	actions := map[int]string{0: "落到桌面", 3: "转焦", 6: "平移约6厘米", 9: "端起约5厘米", 11: "放回原杯碟"}
	for i, beat := range recipe.Beats {
		line, hasLine := voiced[i]
		if strings.HasPrefix(beat.Sound, "画外音（原声）：") != hasLine {
			t.Fatalf("beat %d unexpectedly added or removed a voiceover", i+1)
		}
		if hasLine && (!strings.Contains(beat.Sound, "“"+line+"”") || beat.Tier != TemplateTierCore) {
			t.Fatalf("beat %d lost its spoken words or can be dropped", i+1)
		}
		if !strings.Contains(beat.Sound, "钢琴") || !strings.Contains(beat.Sound, "拨弦低音") {
			t.Fatalf("beat %d lost the musical direction, leaving only ambience", i+1)
		}
	}
	for target := recipe.DurationRange.Min; target <= recipe.DurationRange.Max; target++ {
		kept, seconds, ok := planTemplateTiming(timingBeats(recipe.Beats), recipe.DurationRange, target)
		if !ok {
			t.Fatalf("cannot plan %d seconds", target)
		}
		for index, action := range actions {
			if !slices.Contains(kept, index) || !strings.Contains(recipe.Beats[index].Action, action) {
				t.Fatalf("%d seconds lost action %q", target, action)
			}
		}
		for index, line := range voiced {
			k := slices.Index(kept, index)
			// 每句约两秒，另留一秒给开场接触声或片尾停留；不能把声音挤到两秒的镜头里。
			if k < 0 || seconds[k] < 3 || utf8.RuneCountInString(line) > (seconds[k]-1)*5 {
				t.Fatalf("%d seconds cannot fit voiceover %q and its pause: kept=%v seconds=%v", target, line, kept, seconds)
			}
		}
		if target == 15 && (!slices.Equal(kept, []int{0, 3, 6, 9, 11}) || !slices.Equal(seconds, []int{3, 3, 3, 3, 3})) {
			t.Fatalf("short cut lost its five complete beats: kept=%v seconds=%v", kept, seconds)
		}
	}
	if !strings.Contains(recipe.Beats[3].Action, "甜点断面") || !strings.Contains(recipe.Beats[11].Framing, "窗边座位") {
		t.Fatal("optional reference assets disappeared from the core cut")
	}
	for _, requirement := range []string{"后期独白关闭时narration留空，原生台词仍写进prompt", "后期配乐关闭不等于禁用原生音乐", "无旁白镜仍有清楚音乐", "用户明确禁用人声、音乐时遵守本单设置"} {
		if !strings.Contains(recipe.Style.Sound, requirement) {
			t.Errorf("missing audio ownership boundary: %s", requirement)
		}
	}
}
