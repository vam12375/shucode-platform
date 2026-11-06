package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"testing"
)

func TestSecondsMixedFrameRatesKeepNativeAudioAndExactDuration(t *testing.T) {
	if !FinishingCompositionAvailable() {
		t.Skip("real ffmpeg unavailable")
	}
	f := setup(t)
	dir := t.TempDir()
	p := &Project{TeamID: f.team, Brief: Brief{Mode: "auto", GenerationPolicy: "whole-first", Duration: 6, Finishing: &FinishingOptions{NativeAudio: true, Subtitles: "none"}}}
	for i, rate := range []string{"24", "25"} {
		path := filepath.Join(dir, fmt.Sprintf("source-%d.mp4", i))
		frequency := 440 + i*440
		finishingFFmpegFixture(t, "-f", "lavfi", "-i", "testsrc2=s=160x90:r="+rate, "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:sample_rate=48000", frequency), "-t", "2.96", "-c:v", "libx264", "-c:a", "aac", path)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		id, _, err := f.s.Blobs.Put(f.team, bytes.NewReader(raw), MaxVideo)
		if err != nil {
			t.Fatal(err)
		}
		p.Shots = append(p.Shots, Shot{OutputAssetID: id, Plan: ShotPlan{Duration: 3}})
	}
	segments, err := compositionTimeline(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.renderFinishingComposition(context.Background(), p, segments, dir, 160, 90); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "final.mp4")
	if err := validateFinishingOutput(context.Background(), output, 6); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		samples := finishingSamples(t, output, float64(i*3)+.1, .3)
		if finishingAmplitude(samples, float64(440+i*440)) < .01 {
			t.Fatalf("native segment %d lost or shifted", i)
		}
	}
}

func TestSecondsPlanMatchesCanonicalHostHash(t *testing.T) {
	raw, err := os.ReadFile("testdata/video-seconds-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Plan          Plan   `json:"plan"`
		CandidateHash string `json:"candidateHash"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	hash, err := candidatePlanHash(fixture.Plan)
	if err != nil || hash != fixture.CandidateHash {
		t.Fatalf("hash %s: %v", hash, err)
	}
	var source struct {
		Plan map[string]any `json:"plan"`
	}
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	first := source.Plan["shots"].([]any)[0].(map[string]any)
	shots := []any{}
	for _, seconds := range []float64{3.35, 3.35, 3.3} {
		shot := maps.Clone(first)
		creative := maps.Clone(first["creative"].(map[string]any))
		creative["startSeconds"], creative["endSeconds"] = 0, seconds
		shot["creative"] = creative
		shot["beats"] = []any{map[string]any{"startSeconds": 0, "endSeconds": seconds, "description": "完整动作"}}
		shots = append(shots, shot)
	}
	source.Plan["shots"] = shots
	roundingRaw, _ := json.Marshal(source.Plan)
	var rounded Plan
	if err := json.Unmarshal(roundingRaw, &rounded); err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{101, 100, 99} {
		if c := rounded.Shots[i].Creative; c.InFrame != 0 || c.OutFrame != want || rounded.Shots[i].Beats[0].OutFrame != want {
			t.Fatalf("rounding shot %d: %+v", i, rounded.Shots[i])
		}
	}
	for _, value := range []string{
		`{"startSeconds":-0.001,"endSeconds":5,"description":"a"}`,
		`{"startSeconds":5,"endSeconds":2,"description":"a"}`,
		`{"startSeconds":0,"endSeconds":30.001,"description":"a"}`,
		`{"startSeconds":0,"endSeconds":5,"inFrame":0,"outFrame":150,"description":"a"}`,
		`{"startSeconds":null,"endSeconds":5,"description":"a"}`,
		`{"startSeconds":0,"description":"a"}`,
	} {
		var beat GenerationBeat
		if json.Unmarshal([]byte(value), &beat) == nil {
			t.Fatalf("invalid timing accepted: %s", value)
		}
	}
}

func TestSecondsSourceUsesPacketTimestampsWithoutStreamDuration(t *testing.T) {
	if !FinishingCompositionAvailable() {
		t.Skip("real ffmpeg unavailable")
	}
	path := filepath.Join(t.TempDir(), "source.mkv")
	finishingFFmpegFixture(t, "-f", "lavfi", "-i", "testsrc2=s=160x90:r=25", "-t", "2.96", "-c:v", "libx264", path)
	seconds, err := probeCompositionStreamSeconds(context.Background(), path, "v:0")
	if err != nil || !durationMatches(seconds, 2.96) {
		t.Fatalf("timestamp duration %v: %v", seconds, err)
	}
	if _, err := probeCompositionStreamSeconds(context.Background(), path, "a:0"); err == nil {
		t.Fatal("missing audio stream accepted")
	}
}

func TestSecondsOutputAcceptsDifferentFrameRates(t *testing.T) {
	if !FinishingCompositionAvailable() {
		t.Skip("real ffmpeg unavailable")
	}
	for _, rate := range []string{"24", "25", "30", "24000/1001", "vfr"} {
		t.Run(rate, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "source.mp4")
			inputRate := rate
			if rate == "vfr" {
				inputRate = "30"
			}
			args := []string{"-f", "lavfi", "-i", "testsrc2=s=160x90:r=" + inputRate, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "5"}
			if rate == "vfr" {
				args = append(args, "-vf", "select='if(lt(t,2),not(mod(n,2)),1)'", "-fps_mode", "vfr")
			}
			args = append(args, "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", path)
			finishingFFmpegFixture(t, args...)
			if err := validateFinishingOutput(context.Background(), path, 5); err != nil {
				t.Fatal(err)
			}
			f := setup(t)
			p := &Project{TeamID: f.team, CompositionAssetID: "seconds-evidence", Brief: Brief{Mode: "auto", Duration: 5, Finishing: &FinishingOptions{NativeAudio: true, Subtitles: "none"}}, Shots: []Shot{{Plan: ShotPlan{Duration: 5}}}}
			segments, err := compositionTimeline(p)
			if err != nil {
				t.Fatal(err)
			}
			report, err := f.s.inspectCompositionQuality(context.Background(), p, path, segments, 160, 90)
			if err != nil || report.Status == "failed" || len(report.Keyframes) != 3 {
				t.Fatalf("report=%+v err=%v", report, err)
			}
		})
	}
}

func TestSecondsOutputRejectsMissingVideoAndSignificantDurationErrors(t *testing.T) {
	if !FinishingCompositionAvailable() {
		t.Skip("real ffmpeg unavailable")
	}
	for _, seconds := range []float64{4.92, 4.5, 5.5} {
		t.Run(fmt.Sprint(seconds), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.mp4")
			// 音轨一直为5秒，不能用容器/音轨时长掩盖视频缺尾。
			finishingFFmpegFixture(t, "-f", "lavfi", "-i", "color=c=blue:s=160x90:r=25:d="+fmt.Sprint(seconds), "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=5", "-c:v", "libx264", "-c:a", "aac", path)
			err := validateFinishingOutput(context.Background(), path, 5)
			if (err == nil) != (seconds == 4.92) {
				t.Fatalf("duration %v: %v", seconds, err)
			}
		})
	}
}
