package media

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeOnlyLegacyFlagsDoNotDisableModelSound(t *testing.T) {
	for _, options := range []*FinishingOptions{nil, {Subtitles: "none"}, {Voiceover: true, Music: true, Subtitles: "none"}} {
		brief := Brief{Mode: "auto", Finishing: options}
		body := videoSubmitBody(Model{GenerateAudio: true}, brief, ShotPlan{Prompt: "角色说你好"}, "")
		if body["generate_audio"] != true {
			t.Fatalf("native audio disabled by old flags: %v", body)
		}
		p := &Project{Brief: brief}
		if options != nil {
			plan, err := finishingGenerationPlan(p, ShotPlan{Prompt: "角色说你好"})
			if err != nil || strings.Contains(plan.Prompt, "不要解说、音乐和音效") {
				t.Fatalf("legacy silent guard: %v %s", err, plan.Prompt)
			}
		}
		effective := effectiveFinishingOptions(p)
		if effective.Voiceover || effective.Music || !effective.NativeAudio {
			t.Fatalf("legacy post audio reenabled: %+v", effective)
		}
	}
}

func TestNativeOnlyDoesNotRaiseQuietAudioNoiseFloor(t *testing.T) {
	if !FinishingCompositionAvailable() {
		t.Skip("finishing ffmpeg unavailable")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "quiet.wav")
	finishingFFmpegFixture(t, "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000:duration=2", "-af", "volume=0.008", "-c:a", "pcm_s16le", source)
	p := &Project{Brief: Brief{Mode: "auto", Finishing: &FinishingOptions{Voiceover: true, Music: true, Subtitles: "none"}}}
	output, err := (&Service{}).finishingMasterAudio(context.Background(), p, dir, 2, []string{"quiet.wav"})
	if err != nil {
		t.Fatal(err)
	}
	before := finishingAmplitude(finishingSamples(t, source, .2, 1), 880)
	after := finishingAmplitude(finishingSamples(t, output, .2, 1), 880)
	if before <= 0 || math.Abs(after/before-1) > .05 {
		t.Fatalf("quiet model audio amplified: input=%v output=%v", before, after)
	}
}

func TestNativeOnlyUnsupportedAudioModelOmitsParameter(t *testing.T) {
	body := videoSubmitBody(Model{GenerateAudio: false}, Brief{Finishing: &FinishingOptions{Voiceover: true, Music: true, NativeAudio: true, Subtitles: "none"}}, ShotPlan{Prompt: "商品展示"}, "")
	if _, exists := body["generate_audio"]; exists {
		t.Fatal("unsupported audio parameter sent")
	}
}
