package media

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func makeWholeReferenceWorkerVideo(t *testing.T, seconds string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "worker-reference.mp4")
	cmd := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=blue:s=160x90:r=30", "-t", seconds, "-c:v", "libx264", "-pix_fmt", "yuv420p", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg unavailable for whole-reference worker test: %v (%s)", err, output)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestWholeReferenceWorkerSubmitsEachExecutionSegmentAndComposesOnce(t *testing.T) {
	if !CompositionAvailable() {
		t.Skip("composition dependencies unavailable")
	}
	f := setup(t)
	ctx := context.Background()
	// 两个执行段的供应商整数时长总和与原片四舍五入时长一致（15+5=20）；
	// 合成阶段仍按原片事实边界取帧，并将最终交付裁到 20 秒。
	f.video = makeWholeReferenceWorkerVideo(t, "15")
	brief, models, plan := wholeReferenceFixture()
	boundaries := []float64{0, 4, 8, 12, 14, 16, 20.155}
	brief.ReferenceDurationSeconds, brief.Duration = 20.155, 20
	brief.AssetIDs = []string{"replacement"}
	models[0].VideoReference.MaxSegments = 2
	plan.Shots = plan.Shots[:0]
	for i := 0; i < len(boundaries)-1; i++ {
		clip := "clip-a"
		if i >= 4 {
			clip = "clip-b"
		}
		plan.Shots = append(plan.Shots, ShotPlan{
			SourceStart: boundaries[i], SourceEnd: boundaries[i+1], Duration: 4,
			Framing: "全景", Camera: "推进", Action: "连续动作", Purpose: "复刻",
			Prompt: fmt.Sprintf("事实镜头 %d", i+1), AssetID: "replacement", ReferenceVideoAssetID: clip,
		})
	}
	// 上传夹具通过探针返回与每个事实范围一致的时长；文件内容仍经过真实 MP4 类型与字节校验。
	sourceVideo := f.video
	probeCalls := 0
	f.s.ReferenceProbe = func(context.Context, string) (ReferenceMetadata, error) {
		probeCalls++
		switch probeCalls {
		case 2:
			return ReferenceMetadata{DurationSeconds: 14, Width: 160, Height: 90}, nil
		case 4:
			return ReferenceMetadata{DurationSeconds: 6.155, Width: 160, Height: 90}, nil
		default:
			return ReferenceMetadata{DurationSeconds: 20.155, Width: 160, Height: 90}, nil
		}
	}
	sourceID := uuid.NewString()
	sourcePath := f.s.Blobs.Path(f.team, sourceID)
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, sourceVideo, 0600); err != nil {
		t.Fatal(err)
	}
	checksum, err := checksumFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range []Asset{
		{ID: sourceID, Name: "source.mp4", Kind: "video", Mime: "video/mp4", Size: int64(len(sourceVideo)), DurationSeconds: 20.155, Checksum: checksum},
		{ID: "replacement", Name: "replacement.png", Kind: "image"},
	} {
		if err := f.s.Repo.AddAsset(ctx, f.team, asset); err != nil {
			t.Fatal(err)
		}
	}
	clipA, err := f.s.UploadReferenceClip(ctx, f.team, sourceID, checksum, 0, 14, bytes.NewReader(sourceVideo))
	if err != nil {
		t.Fatal(err)
	}
	clipB, err := f.s.UploadReferenceClip(ctx, f.team, sourceID, checksum, 14, 20.155, bytes.NewReader(sourceVideo))
	if err != nil {
		t.Fatal(err)
	}
	for i := range plan.Shots {
		if i < 4 {
			plan.Shots[i].ReferenceVideoAssetID = clipA.ID
		} else {
			plan.Shots[i].ReferenceVideoAssetID = clipB.ID
		}
	}
	shots, quote, err := BuildShots(brief, plan, models)
	if err != nil || len(shots) != 2 || quote != 14_000_000 {
		t.Fatalf("unexpected execution plan: shots=%d quote=%d err=%v", len(shots), quote, err)
	}
	p := f.project(t)
	p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Brief, v.Models = brief, models
		v.Brief.ReferenceAssetID = sourceID
		v.SourceChecksum = checksum
		v.SourceAnalysis = &SourceAnalysis{SourceChecksum: checksum, Summary: "20.155 秒原片事实", References: plan.References}
		v.Plan, v.Shots, v.QuoteMicro = &plan, shots, quote
		v.Status = "review"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Action(ctx, f.team, p.ID, p.Version, "generate"); err != nil {
		t.Fatal(err)
	}
	for range 12 {
		f.s.Tick(ctx)
		current, getErr := f.s.Repo.Get(ctx, f.team, p.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if current.Status == "completed" || current.Status == "failed" {
			p = current
			break
		}
		p = current
	}
	if p.Status != "completed" || p.OutputAssetID == "" || len(p.Shots) != 2 || p.Shots[0].Status != "succeeded" || p.Shots[1].Status != "succeeded" {
		t.Fatalf("multi-segment lifecycle did not compose: status=%s output=%q shots=%+v error=%s", p.Status, p.OutputAssetID, p.Shots, p.Error)
	}
	if p.ChargedMicro != quote || f.submits.Load() != 2 {
		t.Fatalf("segments were not submitted/settled exactly once: charged=%d quote=%d submits=%d", p.ChargedMicro, quote, f.submits.Load())
	}
	team, err := f.s.Repo.Store.GetTeam(ctx, f.team)
	if err != nil || team.UsedCredits != "14.000000" || team.ReservedCredits != "0.000000" {
		t.Fatalf("unexpected final billing: team=%+v err=%v", team, err)
	}
	f.requestMu.Lock()
	defer f.requestMu.Unlock()
	if len(f.requestBodies) != 2 {
		t.Fatalf("expected two provider submissions, got %d", len(f.requestBodies))
	}
	for i, wantClip := range []string{clipA.ID, clipB.ID} {
		body := f.requestBodies[i]
		if body["duration"] != float64([]int{15, 5}[i]) {
			t.Fatalf("execution %d duration=%v", i, body["duration"])
		}
		content, ok := body["content"].([]any)
		if !ok || len(content) < 3 {
			t.Fatalf("execution %d missing reference content: %#v", i, body)
		}
		video := content[len(content)-1].(map[string]any)
		if video["role"] != "reference_video" || submittedAssetID(t, video, "video_url") != wantClip {
			t.Fatalf("execution %d clip=%#v want=%s", i, video, wantClip)
		}
	}
	assets, err := f.s.Repo.Assets(ctx, f.team)
	if err != nil {
		t.Fatal(err)
	}
	var final Asset
	for _, candidate := range assets {
		if candidate.ID == p.OutputAssetID {
			final = candidate
			break
		}
	}
	if final.ID == "" || final.Kind != "video" {
		t.Fatalf("final composition asset missing: %+v", final)
	}
	output, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", f.s.Blobs.Path(f.team, final.ID)).Output()
	if err != nil {
		t.Fatalf("final composition is not a readable video: %v", err)
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil || duration < 20.0 || duration > 20.4 {
		t.Fatalf("final composition duration=%q parsed=%f err=%v", strings.TrimSpace(string(output)), duration, err)
	}
}
