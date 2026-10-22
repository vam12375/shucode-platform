package media

import (
	"math"
	"testing"
)

func TestFractionalSubtitleClock(t *testing.T) {
	for input, want := range map[float64]string{0: "0:00:00.00", 1.235: "0:00:01.24", 59.999: "0:01:00.00", 3661.27: "1:01:01.27"} {
		if got := subtitleTime(input); got != want {
			t.Errorf("subtitleTime(%v)=%s want %s", input, got, want)
		}
	}
}

func TestReplicaCompositionUsesSourceClockAndBoundaryFrames(t *testing.T) {
	p := &Project{Brief: Brief{ReferenceAssetID: "source"}}
	for i := range 10 {
		p.Shots = append(p.Shots, Shot{Plan: ShotPlan{Duration: 5, SourceStart: float64(i) * 1.117, SourceEnd: float64(i+1) * 1.117, Transition: "cut"}})
	}
	p.Shots[1].Plan.Transition = "fade"
	segments, err := compositionTimeline(p)
	if err != nil {
		t.Fatal(err)
	}
	frames := 0
	for _, segment := range segments {
		frames += segment.frames
	}
	if frames != 335 {
		t.Fatalf("frames=%d want round(11.17*30)=335", frames)
	}
	if math.Abs(segments[1].start-1.117) > .00001 || math.Abs(segments[1].end-2.234) > .00001 {
		t.Fatalf("source clock lost: %+v", segments[1])
	}
	if segments[0].fadeOut <= 0 || segments[1].fadeIn <= 0 || segments[1].fadeOut != 0 || segments[2].fadeIn != 0 {
		t.Fatalf("fade must apply only at the selected boundary: %+v", segments[:3])
	}
	initialFade, err := compositionTimeline(&Project{Brief: Brief{ReferenceAssetID: "source"}, Shots: []Shot{{Plan: ShotPlan{Duration: 5, SourceStart: 0, SourceEnd: 1.117, Transition: "fade"}}}})
	if err != nil || initialFade[0].fadeIn <= 0 {
		t.Fatalf("first shot fade must start from black: %+v %v", initialFade, err)
	}
	legacy, err := compositionTimeline(&Project{Shots: []Shot{{Plan: ShotPlan{Duration: 5, SourceStart: 1, SourceEnd: 2.1}}}})
	if err != nil || legacy[0].frames != 150 || legacy[0].end != 5 {
		t.Fatalf("legacy duration changed: %+v %v", legacy, err)
	}
}

func TestReplicaCompositionRejectsUndeliverableRange(t *testing.T) {
	for _, end := range []float64{0, .001, math.NaN(), math.Inf(1), 6} {
		p := &Project{Brief: Brief{ReferenceAssetID: "source"}, Shots: []Shot{{Plan: ShotPlan{Duration: 5, SourceEnd: end}}}}
		if _, err := compositionTimeline(p); err == nil {
			t.Errorf("accepted invalid source end %v", end)
		}
	}
}

func TestSubtitleMetrics(t *testing.T) {
	cases := []struct {
		w, h                    int
		font, marginLR, marginV int
	}{
		{854, 480, 32, 26, 53},
		{1280, 720, 48, 40, 80},
		{1920, 1080, 72, 60, 120},
		{3840, 2160, 144, 120, 240},
	}
	for _, c := range cases {
		font, marginLR, marginV := subtitleMetrics(c.w, c.h)
		if font != c.font || marginLR != c.marginLR || marginV != c.marginV {
			t.Errorf("subtitleMetrics(%d, %d) = %d/%d/%d, want %d/%d/%d", c.w, c.h, font, marginLR, marginV, c.font, c.marginLR, c.marginV)
		}
	}
}

func TestFourKComposeBudget(t *testing.T) {
	if composeTimeout("4k") <= composeTimeout("1080p") {
		t.Fatal("4k compose timeout must be longer than 1080p")
	}
	if workerLeaseDuration("4k") <= composeTimeout("4k") {
		t.Fatal("4k worker lease must cover the compose timeout")
	}
	if composeTimeout("1080p") != defaultComposeTimeout {
		t.Fatal("non-4k compose timeout must stay unchanged")
	}
}

func TestOutputSize(t *testing.T) {
	cases := []struct {
		resolution, ratio string
		w, h              int
	}{
		{"480p", "16:9", 854, 480},
		{"480p", "9:16", 480, 854},
		{"480p", "1:1", 480, 480},
		{"720p", "16:9", 1280, 720},
		{"720p", "9:16", 720, 1280},
		{"720p", "1:1", 720, 720},
		{"1080p", "16:9", 1920, 1080},
		{"1080p", "9:16", 1080, 1920},
		{"1080p", "1:1", 1080, 1080},
		{"4k", "16:9", 3840, 2160},
		{"4k", "9:16", 2160, 3840},
		{"4k", "1:1", 2160, 2160},
	}
	for _, c := range cases {
		w, h := outputSize(c.resolution, c.ratio)
		if w != c.w || h != c.h {
			t.Errorf("outputSize(%q, %q) = %dx%d, want %dx%d", c.resolution, c.ratio, w, h, c.w, c.h)
		}
	}
}
