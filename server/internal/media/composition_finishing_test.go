package media

import (
	"math"
	"strings"
	"testing"
)

func TestFinishingTransitionsPreserveDeliveryClock(t *testing.T) {
	p := &Project{Brief: Brief{Mode: "auto", Duration: 6, Finishing: &FinishingOptions{Subtitles: "none"}}, Shots: []Shot{
		{Plan: ShotPlan{Duration: 2}},
		{Plan: ShotPlan{Duration: 2, Transition: "fade"}},
		{Plan: ShotPlan{Duration: 2, Transition: "cut"}},
	}}
	segments, err := compositionTimeline(p)
	if err != nil {
		t.Fatal(err)
	}
	if segments[0].frames+segments[1].frames+segments[2].frames != 180 || segments[1].start != 2 || segments[2].end != 6 {
		t.Fatalf("delivery clock changed: %+v", segments)
	}
	if got := finishingOverlapFrames(p, segments, 0); got != 9 {
		t.Fatalf("fade overlap=%d want 9 tail frames", got)
	}
	if got := finishingOverlapFrames(p, segments, 1); got != 0 {
		t.Fatalf("cut must not overlap: %d", got)
	}
	filter := finishingPictureFilter(p, segments)
	if !strings.Contains(filter, "xfade=transition=fade:duration=0.300000000:offset=2.000000000") || !strings.Contains(filter, "concat=n=2:v=1:a=0") || strings.Contains(filter, "fade=t=") {
		t.Fatalf("unexpected picture filter: %s", filter)
	}
}

func TestFinishingNarrationSpeedIsBoundedAndNeverTruncates(t *testing.T) {
	for _, tc := range []struct {
		duration, target, rate float64
		valid                  bool
	}{{5, 10, 1, true}, {10.5, 10, 10.5 / 9.85, true}, {11.5, 10, 0, false}, {math.NaN(), 10, 0, false}, {0, 10, 0, false}} {
		rate, err := finishingNarrationRate(tc.duration, tc.target)
		if (err == nil) != tc.valid || tc.valid && math.Abs(rate-tc.rate) > 0.000001 {
			t.Errorf("rate(%v,%v)=%v/%v", tc.duration, tc.target, rate, err)
		}
	}
}

func TestFinishingGenerationGuardDoesNotMutateStoredPlan(t *testing.T) {
	p := &Project{Brief: Brief{Mode: "auto", Finishing: &FinishingOptions{Subtitles: "none"}}}
	plan := ShotPlan{Prompt: "阳光下商品的近景", Narration: "日常的一点从容。", Subtitle: "持久字幕"}
	guarded, err := finishingGenerationPlan(p, plan)
	if err != nil || !strings.Contains(guarded.Prompt, "不要字幕") || !strings.Contains(guarded.Prompt, "音乐") || !strings.Contains(guarded.Prompt, plan.Prompt) || plan.Prompt != "阳光下商品的近景" {
		t.Fatalf("guarded=%+v err=%v original=%+v", guarded, err, plan)
	}
	legacy, err := finishingGenerationPlan(&Project{}, plan)
	if err != nil || legacy.Prompt != plan.Prompt {
		t.Fatalf("legacy prompt changed: %+v %v", legacy, err)
	}
	if _, err = finishingGenerationPlan(p, ShotPlan{Prompt: strings.Repeat("字", 3999)}); err == nil {
		t.Fatal("oversize guarded prompt was silently truncated")
	}
}
