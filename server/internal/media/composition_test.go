package media

import "testing"

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
