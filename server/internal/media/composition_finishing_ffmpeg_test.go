package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func finishingFFmpegFixture(t *testing.T, args ...string) {
	t.Helper()
	output, err := exec.Command("ffmpeg", append([]string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg fixture: %v: %s", err, output)
	}
}

func finishingSampleFrame(t *testing.T, file string, at float64, filter string) []byte {
	t.Helper()
	frame, err := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-ss", fmt.Sprintf("%.6f", at), "-i", file, "-vf", filter, "-frames:v", "1", "-pix_fmt", "rgb24", "-f", "rawvideo", "pipe:1").Output()
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func finishingSamples(t *testing.T, file string, start, seconds float64) []float64 {
	t.Helper()
	raw, err := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-ss", fmt.Sprintf("%.6f", start), "-i", file, "-t", fmt.Sprintf("%.6f", seconds), "-vn", "-ac", "1", "-ar", "48000", "-c:a", "pcm_f32le", "-f", "f32le", "pipe:1").Output()
	if err != nil || len(raw) < 480 {
		t.Fatalf("audio sample length=%d err=%v", len(raw), err)
	}
	result := make([]float64, len(raw)/4)
	for i := range result {
		result[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:])))
	}
	return result
}

func finishingAmplitude(samples []float64, frequency float64) float64 {
	real, imaginary := 0.0, 0.0
	for i, value := range samples {
		phase := 2 * math.Pi * frequency * float64(i) / 48000
		real += value * math.Cos(phase)
		imaginary += value * math.Sin(phase)
	}
	return 2 * math.Hypot(real, imaginary) / float64(len(samples))
}

func TestFinishingRealFFmpegPreservesFramesReplacesAudioAndOmitsCaptions(t *testing.T) {
	if !FinishingCompositionAvailable() {
		t.Skip("finishing ffmpeg unavailable")
	}
	for _, transition := range []string{"cut", "fade"} {
		t.Run(transition, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			dir := t.TempDir()
			p := &Project{TeamID: f.team, Brief: Brief{Mode: "auto", Duration: 4, Finishing: &FinishingOptions{Subtitles: "none"}}}
			for i, color := range []string{"red", "blue"} {
				input := filepath.Join(dir, fmt.Sprintf("input%d.mp4", i))
				// 竖向源素材交付横向满幅，原音特意加入可辨频率以检查彻底替换。
				finishingFFmpegFixture(t, "-f", "lavfi", "-i", "color=c="+color+":s=90x160:r=30", "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000", "-t", "2", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", input)
				raw, err := os.ReadFile(input)
				if err != nil {
					t.Fatal(err)
				}
				id, _, err := f.s.Blobs.Put(f.team, bytes.NewReader(raw), MaxVideo)
				if err != nil {
					t.Fatal(err)
				}
				p.Shots = append(p.Shots, Shot{OutputAssetID: id, Plan: ShotPlan{Duration: 2, Transition: transition, Subtitle: "UNWANTED CAPTION"}})
			}
			segments, err := compositionTimeline(p)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.s.renderFinishingComposition(ctx, p, segments, dir, 160, 90); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(dir, "final.mp4")
			if err = validateFinishingOutput(ctx, output, 120); err != nil {
				t.Fatal(err)
			}
			for _, at := range []float64{.1, 1, 1.95, 2.45, 3.9} {
				frame := finishingSampleFrame(t, output, at, "scale=1:1")
				if len(frame) != 3 || int(frame[0])+int(frame[2]) < 210 {
					t.Fatalf("black frame/border/caption at %v: %v", at, frame)
				}
			}
			middle := finishingSampleFrame(t, output, 2.15, "scale=1:1")
			if transition == "fade" && (middle[0] < 30 || middle[2] < 30 || int(middle[0])+int(middle[2]) < 210) {
				t.Fatalf("fade must blend red and blue without black: %v", middle)
			}
			if transition == "cut" && (middle[0] > 20 || middle[2] < 220) {
				t.Fatalf("cut unexpectedly blended: %v", middle)
			}
			if _, err = os.Stat(filepath.Join(dir, "captions.ass")); !os.IsNotExist(err) {
				t.Fatalf("disabled captions were generated: %v", err)
			}
			for _, value := range finishingSamples(t, output, 0, 4) {
				if math.Abs(value) > .0001 {
					t.Fatalf("source model audio leaked: %v", value)
				}
			}
		})
	}
}

func TestFinishingRealFFmpegMusicLoopsContinuouslyAndDucksUnderNarration(t *testing.T) {
	if !FinishingCompositionAvailable() {
		t.Skip("finishing ffmpeg unavailable")
	}
	f := setup(t)
	dir := t.TempDir()
	music, narration := filepath.Join(dir, "licensed-music.wav"), filepath.Join(dir, "narration.wav")
	finishingFFmpegFixture(t, "-f", "lavfi", "-i", "sine=frequency=220:sample_rate=48000", "-t", "1.23", "-c:a", "pcm_s16le", music)
	finishingFFmpegFixture(t, "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000", "-af", "atrim=duration=1,apad", "-t", "4", "-c:a", "pcm_s16le", narration)
	f.s.BackgroundMusicPath = music
	p := &Project{Brief: Brief{Mode: "auto", Finishing: &FinishingOptions{Voiceover: true, Music: true, Subtitles: "none"}}}
	master, err := f.s.finishingMasterAudio(context.Background(), p, dir, 4, narration)
	if err != nil {
		t.Fatal(err)
	}
	length, err := probeFinishingAudio(context.Background(), master)
	if err != nil || math.Abs(length-4) > .001 {
		bedLength, bedErr := probeFinishingAudio(context.Background(), filepath.Join(dir, "music.wav"))
		t.Logf("music bed length=%v err=%v", bedLength, bedErr)
		t.Fatalf("master duration=%v err=%v", length, err)
	}
	spoken := finishingSamples(t, master, .55, .2)
	quiet := finishingSamples(t, master, 1.8, .2)
	if finishingAmplitude(spoken, 880) < .03 || finishingAmplitude(quiet, 880) > .001 {
		t.Fatalf("narration missing or failed to end: spoken=%v quiet=%v", finishingAmplitude(spoken, 880), finishingAmplitude(quiet, 880))
	}
	spokenMusic, quietMusic := finishingAmplitude(spoken, 220), finishingAmplitude(quiet, 220)
	if quietMusic < .005 || spokenMusic >= quietMusic*.85 {
		t.Fatalf("music did not duck under speech: spoken=%v quiet=%v", spokenMusic, quietMusic)
	}
	for _, at := range []float64{1.25, 1.95, 2.05, 2.7} {
		if amplitude := finishingAmplitude(finishingSamples(t, master, at, .1), 220); amplitude < .003 {
			t.Fatalf("single music track dropped at %v: amplitude=%v", at, amplitude)
		}
	}
	// 整片淡出只发生在末尾，循环点不重新执行片头或片尾淡化。
	if end := finishingAmplitude(finishingSamples(t, master, 3.9, .08), 220); end >= quietMusic*.25 {
		t.Fatalf("music ending did not fade: %v vs %v", end, quietMusic)
	}
}

func TestFinishingWorkerExplicitlyDisablesNativeAudio(t *testing.T) {
	if !FinishingCompositionAvailable() {
		t.Skip("finishing ffmpeg unavailable")
	}
	f := setup(t)
	p := f.project(t)
	var err error
	p, err = f.s.Mutate(context.Background(), f.team, p.ID, p.Version, func(v *Project) error {
		v.Brief.Finishing = &FinishingOptions{Subtitles: "none"}
		v.Models[0].GenerateAudio = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.s.Tick(context.Background())
	p = current(t, f, p)
	if p.Shots[0].Status != "video_running" || f.submits.Load() != 1 {
		t.Fatalf("finishing submit failed: %+v", p)
	}
	f.requestMu.Lock()
	defer f.requestMu.Unlock()
	body := f.requestBodies[0]
	if body["generate_audio"] != false {
		t.Fatalf("native audio not explicitly disabled: %#v", body)
	}
	content := body["content"].([]any)
	if !strings.Contains(content[0].(map[string]any)["text"].(string), "不要字幕") || p.Shots[0].Plan.Prompt != "product" {
		t.Fatalf("generation guard missing or stored plan modified: %#v", body)
	}
}

func TestFinishingRealFFmpegMixedTransitionsAndExplicitShortCaptions(t *testing.T) {
	if !FinishingCompositionAvailable() {
		t.Skip("finishing ffmpeg unavailable")
	}
	f := setup(t)
	dir := t.TempDir()
	p := &Project{TeamID: f.team, Brief: Brief{Mode: "auto", Duration: 8, Finishing: &FinishingOptions{Subtitles: "short"}}}
	for i, color := range []string{"red", "blue", "green", "white"} {
		path := filepath.Join(dir, fmt.Sprintf("source%d.mp4", i))
		finishingFFmpegFixture(t, "-f", "lavfi", "-i", "color=c="+color+":s=160x90:r=30", "-t", "2", "-c:v", "libx264", "-pix_fmt", "yuv420p", path)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		id, _, err := f.s.Blobs.Put(f.team, bytes.NewReader(raw), MaxVideo)
		if err != nil {
			t.Fatal(err)
		}
		transition := "fade"
		if i == 2 {
			transition = "cut"
		}
		subtitle := ""
		if i == 0 {
			subtitle = "SHORT"
		}
		p.Shots = append(p.Shots, Shot{OutputAssetID: id, Plan: ShotPlan{Duration: 2, Transition: transition, Subtitle: subtitle}})
	}
	segments, err := compositionTimeline(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.renderFinishingComposition(context.Background(), p, segments, dir, 160, 90); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "final.mp4")
	if err = validateFinishingOutput(context.Background(), output, 240); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		at               float64
		lowR, lowG, lowB byte
	}{{2.15, 30, 0, 30}, {4.1, 0, 90, 0}, {6.15, 30, 100, 30}, {7.9, 220, 220, 220}} {
		frame := finishingSampleFrame(t, output, tc.at, "scale=1:1")
		if len(frame) != 3 || frame[0] < tc.lowR || frame[1] < tc.lowG || frame[2] < tc.lowB {
			t.Fatalf("mixed boundary drift at %v: %v", tc.at, frame)
		}
	}
	caption, err := os.ReadFile(filepath.Join(dir, "captions.ass"))
	if err != nil || !strings.Contains(string(caption), "SHORT") {
		t.Fatalf("explicit caption absent: %s %v", caption, err)
	}
	frame := finishingSampleFrame(t, output, 1, "null")
	whitePixels := 0
	for i := 0; i+2 < len(frame); i += 3 {
		if frame[i] > 100 && frame[i+1] > 100 && frame[i+2] > 100 {
			whitePixels++
		}
	}
	if whitePixels < 10 {
		t.Fatalf("explicit short caption was not burned into image: %d white pixels", whitePixels)
	}
}
