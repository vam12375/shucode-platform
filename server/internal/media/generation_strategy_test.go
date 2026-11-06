package media

import (
	"reflect"
	"strings"
	"testing"
)

func wholeDurationFixture(durations []int, maxDuration int) (Brief, Plan, []Model) {
	b, p, models := storyFixture()
	b.GenerationPolicy = "whole-first"
	b.MaxCreditsMicro = 100_000_000
	b.Duration = 0
	models[0].MinDuration, models[0].MaxDuration = 4, maxDuration
	first := p.Shots[0]
	p.Shots = nil
	for _, d := range durations {
		shot := first
		c := *first.Creative
		c.InFrame, c.OutFrame = 0, d*30
		shot.Creative, shot.Duration = &c, d
		shot.Beats = []GenerationBeat{{InFrame: 0, OutFrame: d * 30, Description: "完整动作与自然收尾"}}
		p.Shots = append(p.Shots, shot)
		b.Duration += d
	}
	if len(durations) > 1 {
		p.Segmentation = &Segmentation{Reason: "完整对话轮次结束后切换场景"}
		for range durations[1:] {
			p.Segmentation.Cuts = append(p.Segmentation.Cuts, GenerationCut{Kind: "dialogue-turn", Description: "回答结束，停顿后切换"})
		}
	}
	return b, p, models
}

func TestWholeDurationAllocation(t *testing.T) {
	for _, tc := range []struct {
		target, max     int
		durations, want []int
	}{
		{30, 30, nil, []int{30}}, {30, 15, nil, []int{15, 15}}, {31, 30, nil, []int{16, 15}},
		{30, 0, []int{8, 4, 8}, []int{8, 8, 8, 8}}, {12, 0, []int{15, 5}, []int{15}},
		{300, 0, []int{8}, nil}, {30, 0, nil, nil},
	} {
		m := Model{Durations: tc.durations}
		if tc.max > 0 {
			m.MinDuration, m.MaxDuration = 4, tc.max
		}
		if got := planGenerationDurations(tc.target, m); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%+v got %v", tc, got)
		}
	}
}

func TestWholeDurationJobsAndReview(t *testing.T) {
	for _, durations := range [][]int{{30}, {15, 15}, {10, 10, 10}} {
		limit := 15
		if len(durations) == 1 {
			limit = 30
		}
		b, p, m := wholeDurationFixture(durations, limit)
		shots, quote, err := BuildShots(b, p, m)
		if err != nil || len(shots) != len(durations) {
			t.Fatalf("%v: jobs=%d err=%v", durations, len(shots), err)
		}
		var want int64
		for _, d := range durations {
			want += mediaVideoQuoteMicro(m[0], b.Resolution, b.AspectRatio, d)
		}
		if quote != want {
			t.Fatalf("quote %d want %d", quote, want)
		}
		segments, err := compositionTimeline(&Project{Brief: b, Plan: &p, Shots: shots})
		if err != nil || compositionTotalFrames(segments) != 900 {
			t.Fatalf("timeline %v %v", segments, err)
		}
	}
	b, p, m := wholeDurationFixture([]int{15, 15}, 30)
	if _, _, err := BuildShots(b, p, m); err == nil {
		t.Fatal("one supported generation split into two")
	}
	b.GenerationPolicy = ""
	if _, _, err := BuildShots(b, p, m); err != nil {
		t.Fatalf("legacy changed: %v", err)
	}
}

func TestWholeDurationRejectsInvalidBeatCoverageAndCuts(t *testing.T) {
	for _, mutate := range []func(*Plan){
		func(p *Plan) { p.Shots[0].Beats = nil },
		func(p *Plan) { p.Shots[0].Beats[0].OutFrame-- },
		func(p *Plan) { p.Segmentation = nil },
		func(p *Plan) { p.Segmentation.Cuts[0].Kind = "mid-sentence" },
	} {
		b, p, m := wholeDurationFixture([]int{15, 15}, 15)
		mutate(&p)
		if _, _, err := BuildShots(b, p, m); err == nil {
			t.Fatal("invalid segmentation accepted")
		}
	}
}

func TestWholeDurationGenerationSoundGuard(t *testing.T) {
	for _, policy := range []string{"", "whole-first"} {
		for _, post := range []bool{false, true} {
			b, p, models := wholeDurationFixture([]int{30}, 30)
			b.GenerationPolicy = policy
			b.Finishing.NativeAudio = true
			b.Finishing.Voiceover, b.Finishing.Music = post, post
			models[0].GenerateAudio = true
			if body := videoSubmitBody(models[0], b, p.Shots[0], ""); body["generate_audio"] != true || body["duration"] != 30 {
				t.Fatalf("native full duration request changed: %v", body)
			}
			got, err := finishingGenerationPlan(&Project{Brief: b}, p.Shots[0])
			if err != nil || !strings.Contains(got.Prompt, "人声、对白、音乐、环境声和音效") {
				t.Fatalf("native audio lost: %s %v", got.Prompt, err)
			}
			for _, forbidden := range []string{"不要解说", "不另配音乐", "不重复生成旁白", "仅保留需要的环境声"} {
				if strings.Contains(got.Prompt, forbidden) {
					t.Fatalf("native audio restricted: %s", got.Prompt)
				}
			}
		}
	}
}
