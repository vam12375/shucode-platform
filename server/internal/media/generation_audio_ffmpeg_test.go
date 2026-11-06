package media

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestWholeDurationNativeAudioDeclickKeepsSampleClock(t *testing.T) {
	if !FinishingCompositionAvailable() {
		t.Skip("finishing ffmpeg unavailable")
	}
	for _, count := range []int{1, 2} {
		t.Run(string(rune('0'+count)), func(t *testing.T) {
			f := setup(t)
			dir := t.TempDir()
			p := &Project{Brief: Brief{Mode: "auto", GenerationPolicy: "whole-first", Duration: count, Finishing: &FinishingOptions{NativeAudio: true, Subtitles: "none"}}}
			native := []string{}
			for i := 0; i < count; i++ {
				name := "positive.wav"
				expr := "aevalsrc=0.2*cos(2*PI*440*t):s=48000:d=1"
				if i > 0 {
					name = "negative.wav"
					expr = "aevalsrc=-0.2*cos(2*PI*440*t):s=48000:d=1"
				}
				finishingFFmpegFixture(t, "-f", "lavfi", "-i", expr, "-ac", "2", "-c:a", "pcm_s16le", filepath.Join(dir, name))
				native = append(native, name)
				p.Shots = append(p.Shots, Shot{Plan: ShotPlan{Duration: 1}})
			}
			master, err := f.s.finishingMasterAudio(context.Background(), p, dir, float64(count), "", native)
			if err != nil {
				t.Fatal(err)
			}
			samples := finishingSamples(t, master, 0, float64(count))
			if len(samples) != count*48000 {
				t.Fatalf("sample clock shifted: %d", len(samples))
			}
			if math.Abs(samples[0]) < .01 {
				t.Fatal("first sample unnecessarily faded")
			}
			if count == 2 {
				jump := math.Abs(samples[48000] - samples[47999])
				if jump > .003 {
					t.Fatalf("native seam click remains: %f", jump)
				}
				if finishingAmplitude(samples[50000:60000], 440) < .01 {
					t.Fatal("second segment lost")
				}
			}
		})
	}
}

func TestWholeDurationRejectsMissingTailInsteadOfFreezing(t *testing.T) {
	if !FinishingCompositionAvailable() {
		t.Skip("finishing ffmpeg unavailable")
	}
	f := setup(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "short.mp4")
	finishingFFmpegFixture(t, "-f", "lavfi", "-i", "color=c=red:s=160x90:r=30", "-t", "0.8", "-c:v", "libx264", source)
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := f.s.Blobs.Put(f.team, bytes.NewReader(raw), MaxVideo)
	if err != nil {
		t.Fatal(err)
	}
	p := &Project{TeamID: f.team, Brief: Brief{Mode: "auto", GenerationPolicy: "whole-first", Duration: 1, Finishing: &FinishingOptions{NativeAudio: true, Subtitles: "none"}}, Shots: []Shot{{OutputAssetID: id, Plan: ShotPlan{Duration: 1}}}}
	segments, err := compositionTimeline(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.renderFinishingComposition(context.Background(), p, segments, dir, 160, 90); err == nil || err.Error() != "video_result_too_short" {
		t.Fatalf("missing tail accepted: %v", err)
	}
}
