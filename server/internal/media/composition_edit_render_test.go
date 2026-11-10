package media

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestFinishingCaptionWindowSharesASSAndQualityReadability(t *testing.T) {
	caption := strings.Repeat("字", 13) + "\n" + strings.Repeat("字", 13)
	p := &Project{Brief: Brief{Finishing: &FinishingOptions{Subtitles: "short"}}, Shots: []Shot{{Plan: ShotPlan{Duration: 5, Subtitle: caption}}}}
	segments := []compositionSegment{{start: 0, end: 5, frames: 150}}
	start, end := finishingCaptionWindow(p, segments, 0)
	if start != .15 || end != 4.85 {
		t.Fatalf("wrong retained margins: %v %v", start, end)
	}
	ass := compositionASSScript(p, segments, 320, 180)
	if !strings.Contains(ass, "Dialogue: 1,0:00:00.15,0:00:04.85,Default") {
		t.Fatalf("ASS does not use actual window: %s", ass)
	}
	report := &VideoQualityReport{Status: "passed", TotalFrames: 150}
	appendCompositionReadability(report, p, segments)
	if report.Status != "warning" || len(report.Checks) != 1 || report.Checks[0].Code != "caption_readability" {
		t.Fatalf("26 CJK should exceed the actual 4.7s budget: %+v", report)
	}
	p.Shots[0].Plan.Subtitle = strings.Repeat("字", 13) + "\n" + strings.Repeat("字", 12)
	report = &VideoQualityReport{Status: "passed", TotalFrames: 150}
	appendCompositionReadability(report, p, segments)
	if report.Status != "passed" {
		t.Fatalf("25 CJK should fit the actual budget: %+v", report)
	}
	p.Shots = append(p.Shots, Shot{Plan: ShotPlan{Duration: 5, Subtitle: "遮挡字幕"}})
	p.Shots[0].Plan.Subtitle = ""
	p.Composition = &VideoCompositionState{Edit: VideoCompositionEdit{FPS: 30, Subtitles: "short", Clips: []VideoCompositionClip{{SourceShotIndex: 0, OutFrame: 135, Speed: 1}, {SourceShotIndex: 1, OutFrame: 15, Speed: 1}}, Brand: &VideoBrand{Preset: "clean", FontFamily: "sans", PrimaryColor: "#0070C0", BackgroundColor: "#112233", TextColor: "#FFFFFF", EndTitle: "门道"}}}
	segments, err := editableCompositionTimeline(p)
	if err != nil {
		t.Fatal(err)
	}
	start, end = finishingCaptionWindow(p, segments, 1)
	if start != 4.6 || end != 4 {
		t.Fatalf("tail did not clip the half-second window: %v %v", start, end)
	}
	if strings.Contains(compositionASSScript(p, segments, 320, 180), "遮挡字幕") {
		t.Fatal("fully occluded caption unexpectedly rendered")
	}
	report = &VideoQualityReport{Status: "passed", TotalFrames: 150}
	appendCompositionReadability(report, p, segments)
	if report.Status != "warning" || len(report.Checks) != 1 || report.Checks[0].Code != "caption_readability" {
		t.Fatalf("occluded caption did not warn: %+v", report)
	}
	p.Composition.Edit.Subtitles = "none"
	report = &VideoQualityReport{Status: "passed", TotalFrames: 150}
	appendCompositionReadability(report, p, segments)
	if report.Status != "passed" || strings.Contains(compositionASSScript(p, segments, 320, 180), "Dialogue: 1,") {
		t.Fatal("disabled caption used copy budget or rendered text")
	}
}

type staleQualityWriteKey struct{}

type blockedQualityWriteStore struct {
	store.PlatformStore
	team             string
	blocked, release chan struct{}
}

func (s *blockedQualityWriteStore) CompareAndSwapConfigValue(ctx context.Context, key string, old, next []byte) (bool, error) {
	if ctx.Value(staleQualityWriteKey{}) == true && key == assetKey(s.team) {
		close(s.blocked)
		select {
		case <-s.release:
		case <-ctx.Done():
		}
		return false, errors.New("stale quality writer failed after lease takeover")
	}
	return s.PlatformStore.CompareAndSwapConfigValue(ctx, key, old, next)
}

func TestCompositionQualityFailedOldWorkerKeepsPendingEvidenceReusedByNewWorker(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	// 新报告不再写帧，手工构造历史worker的迟到写入以继续验证cleanup并发保护。
	id, _, err := f.s.Blobs.Put(f.team, strings.NewReader("historical-output"), MaxVideo)
	if err != nil {
		t.Fatal(err)
	}
	p := &Project{ID: uuid.NewString(), TeamID: f.team, Status: "composing", CompositionAssetID: id, LeaseOwner: "worker-a", LeaseUntil: time.Now().Add(-time.Minute), Brief: Brief{Mode: "auto", Duration: 5, Finishing: &FinishingOptions{Subtitles: "none"}}, Shots: []Shot{{Plan: ShotPlan{Duration: 5}}}}
	if err := f.s.Repo.Save(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	barrier := &blockedQualityWriteStore{PlatformStore: f.s.Repo.Store, team: f.team, blocked: make(chan struct{}), release: make(chan struct{})}
	f.s.Repo.Store = barrier
	segments := []compositionSegment{{start: 0, end: 5, frames: 150}}
	failed := make(chan error, 1)
	frames := compositionQualityFrames(p, segments)
	go func() {
		staleCtx := context.WithValue(ctx, staleQualityWriteKey{}, true)
		for _, frame := range frames {
			_, _, err := f.s.Blobs.PutWithID(f.team, frame.AssetID, strings.NewReader("historical-frame"), MaxUpload)
			if err == nil {
				err = f.s.Repo.AddAsset(staleCtx, f.team, Asset{ID: frame.AssetID, Kind: "image", Internal: true})
			}
			if err != nil {
				failed <- errors.Join(err, f.s.cleanupUnboundCompositionQuality(ctx, p, id))
				return
			}
		}
		failed <- nil
	}()
	released := false
	defer func() {
		if !released {
			close(barrier.release)
		}
	}()
	select {
	case <-barrier.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("old worker did not reach blocked evidence admission")
	}
	next, err := f.s.Mutate(ctx, p.TeamID, p.ID, p.Version, func(v *Project) error {
		v.LeaseOwner, v.LeaseUntil = "worker-b", time.Now().Add(time.Minute)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	workerB := *f.s
	workerB.WorkerID = "worker-b"
	for _, frame := range frames {
		if _, _, err := workerB.Blobs.PutWithID(f.team, frame.AssetID, strings.NewReader("historical-frame"), MaxUpload); err != nil {
			t.Fatal(err)
		}
		if err := workerB.Repo.AddAsset(ctx, f.team, Asset{ID: frame.AssetID, Kind: "image", Internal: true}); err != nil {
			t.Fatal(err)
		}
	}
	report := &VideoQualityReport{Keyframes: compositionQualityFrames(next, segments)}
	close(barrier.release)
	released = true
	select {
	case err := <-failed:
		if err == nil {
			t.Fatal("old worker failure was not injected")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old worker cleanup did not finish")
	}
	for _, frame := range report.Keyframes {
		if info, err := os.Stat(f.s.Blobs.Path(p.TeamID, frame.AssetID)); err != nil || info.Size() <= 0 {
			t.Fatalf("old failure cleanup deleted reused pending evidence: %v", err)
		}
	}
	if err := workerB.finishComposition(ctx, next, id, report); err != nil {
		t.Fatal(err)
	}
	completed, err := workerB.Repo.Get(ctx, p.TeamID, p.ID)
	if err != nil || completed.Status != "completed" || completed.OutputAssetID != id || completed.QualityReport == nil || len(completed.QualityReport.Keyframes) != 3 {
		t.Fatalf("new worker could not complete delivery after stale cleanup: %+v %v", completed, err)
	}
}

func TestBrandInitialFinishingValidationRejectsInvalidRecipe(t *testing.T) {
	b := Brief{Mode: "auto", Finishing: &FinishingOptions{Subtitles: "none", Brand: &VideoBrand{Preset: "clean", PrimaryColor: "#0070C0", BackgroundColor: "#112233", TextColor: "#FFFFFF", FontFamily: "sans"}}}
	if err := validateFinishingOptions(b); err != nil {
		t.Fatal(err)
	}
	b.Finishing.Brand.PrimaryColor = "#fff;movie=secret"
	if err := validateFinishingOptions(b); err == nil {
		t.Fatal("invalid initial brand reached paid generation")
	}
}

func TestBrandLogoNormalizesAllSupportedFormatsAndRejectsForeignAsset(t *testing.T) {
	if !BrandCompositionAvailable() {
		t.Skip("real branded renderer unavailable")
	}
	for _, format := range []string{"png", "jpeg", "webp", "gif"} {
		t.Run(format, func(t *testing.T) {
			f := setup(t)
			dir, ctx := t.TempDir(), context.Background()
			path := filepath.Join(dir, "source."+format)
			finishingFFmpegFixture(t, "-f", "lavfi", "-i", "color=c=yellow:s=80x40", "-frames:v", "1", "-threads", "1", path)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			id, size, err := f.s.Blobs.Put(f.team, bytes.NewReader(raw), MaxUpload)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.s.Repo.AddAsset(ctx, f.team, Asset{ID: id, Kind: "image", Mime: "image/" + format, Size: size}); err != nil {
				t.Fatal(err)
			}
			p := &Project{TeamID: f.team, Brief: Brief{AssetIDs: []string{id}}}
			brand := &VideoBrand{LogoAssetID: id}
			normalized, err := f.s.prepareCompositionBrandLogo(ctx, p, brand, dir, 320, 180)
			if err != nil || normalized != filepath.Join(dir, "brand-logo.png") || !compositionImageReadable(ctx, normalized) {
				t.Fatalf("static logo normalization: %s, %v", normalized, err)
			}
			p.TeamID = uuid.NewString()
			if _, err := f.s.prepareCompositionBrandLogo(ctx, p, brand, dir, 320, 180); err == nil {
				t.Fatal("foreign logo accepted")
			}
		})
	}
}

func TestCompositionQualityCleanupPreservesDeliveredAndStaleEvidence(t *testing.T) {
	for _, state := range []string{"cancelled", "delivered", "stale"} {
		t.Run(state, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			baseline, err := f.s.Repo.Assets(ctx, f.team)
			if err != nil {
				t.Fatal(err)
			}
			id := uuid.NewString()
			p := &Project{ID: uuid.NewString(), TeamID: f.team, Status: "cancelled", CompositionAssetID: id, Brief: Brief{Duration: 5}, Shots: []Shot{{Plan: ShotPlan{Duration: 5}}}}
			segments := []compositionSegment{{start: 0, end: 5, frames: 150}}
			frames := compositionQualityFrames(p, segments)
			for _, frame := range frames {
				if _, _, err := f.s.Blobs.PutWithID(f.team, frame.AssetID, strings.NewReader("frame"), MaxUpload); err != nil {
					t.Fatal(err)
				}
				if err := f.s.Repo.AddAsset(ctx, f.team, Asset{ID: frame.AssetID, Internal: true, Kind: "image"}); err != nil {
					t.Fatal(err)
				}
			}
			latest := *p
			if state == "delivered" {
				latest.OutputAssetID = id
			} else if state == "stale" {
				latest.CompositionAssetID = uuid.NewString()
			}
			if err := f.s.Repo.Save(ctx, &latest, nil); err != nil {
				t.Fatal(err)
			}
			if err := f.s.cleanupCompositionQuality(ctx, p, id); err != nil {
				t.Fatal(err)
			}
			assets, err := f.s.Repo.Assets(ctx, f.team)
			if err != nil {
				t.Fatal(err)
			}
			want := len(baseline) + 3
			if state == "cancelled" {
				want = len(baseline)
			}
			if len(assets) != want {
				t.Fatalf("evidence after %s cleanup=%d want %d", state, len(assets), want)
			}
			for _, frame := range frames {
				_, err := os.Stat(f.s.Blobs.Path(f.team, frame.AssetID))
				if state == "cancelled" && !os.IsNotExist(err) || state != "cancelled" && err != nil {
					t.Fatalf("wrong evidence file state: %v", err)
				}
			}
			if err := f.s.cleanupUnboundCompositionQuality(ctx, p, id); err != nil {
				t.Fatal(err)
			}
			assets, err = f.s.Repo.Assets(ctx, f.team)
			if state == "stale" {
				want = len(baseline)
			}
			if err != nil || len(assets) != want {
				t.Fatalf("unbound %s cleanup=%d want %d, %v", state, len(assets), want, err)
			}
		})
	}
}

func TestEditableCompositionTimelineUsesSourceFramesAndPlaybackOrder(t *testing.T) {
	p := &Project{Composition: &VideoCompositionState{Edit: VideoCompositionEdit{FPS: 30, Clips: []VideoCompositionClip{
		{SourceShotIndex: 1, InFrame: 30, OutFrame: 90, Speed: 2},
		{SourceShotIndex: 0, InFrame: 0, OutFrame: 180, Speed: 1},
	}}}}
	segments, err := editableCompositionTimeline(p)
	if err != nil || len(segments) != 2 || segments[0].frames != 30 || segments[1].frames != 180 || segments[1].start != 1 || segments[1].end != 7 {
		t.Fatalf("wrong compiled clock: %+v, %v", segments, err)
	}
}

func TestBrandRealFFmpegRecipesRenderLogoAndKeepTailInsideTotalFrames(t *testing.T) {
	if !BrandCompositionAvailable() {
		t.Skip("real branded renderer unavailable")
	}
	for _, preset := range []string{"clean", "editorial", "bold"} {
		t.Run(preset, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			dir := t.TempDir()
			source, logo := filepath.Join(dir, "source.mp4"), filepath.Join(dir, "logo.png")
			finishingFFmpegFixture(t, "-f", "lavfi", "-i", "color=c=red:s=320x180:r=30", "-t", "5", "-c:v", "libx264", "-pix_fmt", "yuv420p", source)
			finishingFFmpegFixture(t, "-f", "lavfi", "-i", "color=c=yellow:s=80x40", "-frames:v", "1", "-threads", "1", logo)
			videoRaw, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			videoID, _, err := f.s.Blobs.Put(f.team, bytes.NewReader(videoRaw), MaxVideo)
			if err != nil {
				t.Fatal(err)
			}
			logoRaw, err := os.ReadFile(logo)
			if err != nil {
				t.Fatal(err)
			}
			logoID, size, err := f.s.Blobs.Put(f.team, bytes.NewReader(logoRaw), MaxUpload)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.s.Repo.AddAsset(ctx, f.team, Asset{ID: logoID, Kind: "image", Mime: "image/png", Size: size}); err != nil {
				t.Fatal(err)
			}
			brand := &VideoBrand{Preset: preset, PrimaryColor: "#00FF00", BackgroundColor: "#112233", TextColor: "#FFFFFF", FontFamily: "serif", LogoAssetID: logoID, EndTitle: "门道", EndSubtitle: "产品"}
			p := &Project{TeamID: f.team, Brief: Brief{Mode: "auto", Duration: 5, AssetIDs: []string{logoID}, Finishing: &FinishingOptions{Subtitles: "none", Brand: brand}}, Shots: []Shot{{Plan: ShotPlan{Duration: 5}, Status: "succeeded", OutputAssetID: videoID}}}
			segments, err := compositionTimeline(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.s.renderFinishingComposition(ctx, p, segments, dir, 320, 180); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(dir, "final.mp4")
			if err := validateFinishingOutput(ctx, output, 5); err != nil {
				t.Fatal(err)
			}
			before := finishingSampleFrame(t, output, 3.8, "crop=4:4:2:140,scale=1:1")
			if before[0] < 220 || before[2] > 20 {
				t.Fatalf("tail leaked early: %v", before)
			}
			background := finishingSampleFrame(t, output, 4.5, "crop=4:4:2:140,scale=1:1")
			if background[0] > 30 || background[1] < 20 || background[2] < 35 {
				t.Fatalf("tail background absent: %v", background)
			}
			logoPixel := finishingSampleFrame(t, output, 4.5, "crop=4:4:158:35,scale=1:1")
			if logoPixel[0] < 200 || logoPixel[1] < 200 || logoPixel[2] > 40 {
				t.Fatalf("logo absent: %v", logoPixel)
			}
			if evidence := os.Getenv("MENDAO_COMPOSITION_EVIDENCE_DIR"); evidence != "" {
				destination := filepath.Join(evidence, preset)
				if err := os.MkdirAll(destination, 0700); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"final.mp4", "captions.ass"} {
					raw, err := os.ReadFile(filepath.Join(dir, name))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(destination, name), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func TestCompositionSourceClockNormalizesActualFrameRate(t *testing.T) {
	if !FinishingCompositionAvailable() {
		t.Skip("real ffmpeg unavailable")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "source-25fps.mp4")
	finishingFFmpegFixture(t, "-f", "lavfi", "-i", "color=c=blue:s=160x90:r=25", "-t", "2", "-c:v", "libx264", "-pix_fmt", "yuv420p", path)
	seconds, err := probeCompositionStreamSeconds(context.Background(), path, "v:0")
	if err != nil || seconds != 2 {
		t.Fatalf("source seconds=%v, %v", seconds, err)
	}
	if _, err := probeCompositionStreamSeconds(context.Background(), filepath.Join(dir, "missing.mp4"), "v:0"); err == nil {
		t.Fatal("missing source accepted")
	}
}

func TestCompositionReadabilityWarningsDoNotClaimVisualApproval(t *testing.T) {
	text := "过长字幕无法在半秒时窗完整阅读"
	p := &Project{Brief: Brief{Finishing: &FinishingOptions{Subtitles: "short", Brand: &VideoBrand{EndTitle: "这一段尾卡内容过长需要精简"}}}, Shots: []Shot{{Plan: ShotPlan{Subtitle: text}}}}
	segments := []compositionSegment{{start: 0, end: 5, frames: 150}}
	report := &VideoQualityReport{Status: "passed", TotalFrames: 150, VisualReview: "unverified"}
	appendCompositionReadability(report, p, segments)
	if report.Status != "warning" || len(report.Checks) < 1 {
		t.Fatalf("missing warning: %+v", report)
	}
	for _, check := range report.Checks {
		if check.Frame == nil || *check.Frame >= 150 {
			t.Fatalf("invalid frame: %+v", check)
		}
	}
	if report.VisualReview != "unverified" {
		t.Fatal("technical warning changed visual review")
	}
}

func TestBrandCaptionsKeepTextLiteralAndUseSingleTailClock(t *testing.T) {
	for _, preset := range []string{"clean", "editorial", "bold"} {
		t.Run(preset, func(t *testing.T) {
			p := &Project{Brief: Brief{Mode: "auto", Finishing: &FinishingOptions{Subtitles: "short", Brand: &VideoBrand{Preset: preset, PrimaryColor: "#0070C0", BackgroundColor: "#112233", TextColor: "#FFFFFF", FontFamily: "sans", EndTitle: "门道 {literal}\\N", EndSubtitle: "第二行"}}}, Shots: []Shot{{Plan: ShotPlan{Duration: 3, Subtitle: "first"}}, {Plan: ShotPlan{Duration: 3, Subtitle: "last"}}}}
			segments, err := compositionTimeline(p)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if err := writeFinishingCaptions(p, segments, dir, 1920, 1080); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "captions.ass"))
			if err != nil || !strings.Contains(string(raw), "0:00:05.00,0:00:06.00") || strings.Contains(string(raw), "门道 {literal}") || !strings.Contains(string(raw), "第二行") {
				t.Fatalf("tail/literal caption error: %s, %v", raw, err)
			}
		})
	}
}

func TestEditableCompositionRealFFmpegRendersReorderedTrimmedClipsAndQuality(t *testing.T) {
	if !FinishingCompositionAvailable() || !BrandCompositionAvailable() {
		t.Skip("real branded renderer unavailable")
	}
	f := setup(t)
	ctx := context.Background()
	root := t.TempDir()
	p := &Project{TeamID: f.team, CompositionAssetID: "366db428-71b6-45eb-bb88-e636042232ff", Brief: Brief{Mode: "auto", Duration: 6, Finishing: &FinishingOptions{Subtitles: "none"}}}
	for i, color := range []string{"red", "blue"} {
		path := filepath.Join(root, color+".mp4")
		finishingFFmpegFixture(t, "-f", "lavfi", "-i", "color=c="+color+":s=160x90:r=30", "-t", "3", "-c:v", "libx264", "-pix_fmt", "yuv420p", path)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		id, _, err := f.s.Blobs.Put(f.team, bytes.NewReader(raw), MaxVideo)
		if err != nil {
			t.Fatal(err)
		}
		p.Shots = append(p.Shots, Shot{Status: "succeeded", OutputAssetID: id, Plan: ShotPlan{Duration: 3, Subtitle: []string{"first", "second"}[i]}})
	}
	p.Composition = &VideoCompositionState{Edit: VideoCompositionEdit{FPS: 30, Subtitles: "none", Clips: []VideoCompositionClip{{SourceShotIndex: 1, InFrame: 0, OutFrame: 90, Speed: 1}, {SourceShotIndex: 0, InFrame: 30, OutFrame: 90, Speed: 1}}, Brand: &VideoBrand{Preset: "bold", PrimaryColor: "#00FF00", BackgroundColor: "#112233", TextColor: "#FFFFFF", FontFamily: "sans", EndTitle: "门道"}}}
	segments, err := editableCompositionTimeline(p)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "render")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := f.s.renderFinishingComposition(ctx, p, segments, dir, 160, 90); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "final.mp4")
	first := finishingSampleFrame(t, output, .5, "scale=1:1")
	second := finishingSampleFrame(t, output, 3.5, "scale=1:1")
	if first[2] < 220 || first[0] > 20 || second[0] < 220 || second[2] > 20 {
		t.Fatalf("source order/trim incorrect: %v %v", first, second)
	}
	beforeAssets, err := f.s.Repo.Assets(ctx, p.TeamID)
	if err != nil {
		t.Fatal(err)
	}
	beforeFiles := compositionQualityTestFiles(t, f.s.Blobs.Root)
	report, err := f.s.inspectCompositionQuality(ctx, p, output, segments, 160, 90)
	if err != nil || report.TotalFrames != 150 || report.VisualReview != "unverified" || len(report.Keyframes) != 6 {
		t.Fatalf("report: %+v, %v", report, err)
	}
	wantFrames := []int{4, 45, 86, 94, 120, 146}
	for i, frame := range report.Keyframes {
		wantSource := []int{1, 0}[i/3]
		if frame.Frame != wantFrames[i] || frame.SourceShotIndex == nil || *frame.SourceShotIndex != wantSource || frame.AssetID == "" {
			t.Fatalf("wrong evidence position/source: %+v", frame)
		}
		if _, err := os.Stat(f.s.Blobs.Path(f.team, frame.AssetID)); !os.IsNotExist(err) {
			t.Fatalf("report created evidence blob: %+v %v", frame, err)
		}
	}
	if report.Status != "passed" {
		t.Fatalf("explicit silent output should not warn: %+v", report)
	}
	repeated, err := f.s.inspectCompositionQuality(ctx, p, output, segments, 160, 90)
	if err != nil || !reflect.DeepEqual(report.Keyframes, repeated.Keyframes) {
		t.Fatalf("same delivery changed evidence IDs: %+v %v", repeated, err)
	}
	p.CompositionAssetID = uuid.NewString()
	replacement, err := f.s.inspectCompositionQuality(ctx, p, output, segments, 160, 90)
	if err != nil || len(replacement.Keyframes) != len(report.Keyframes) {
		t.Fatalf("replacement report: %+v %v", replacement, err)
	}
	for i, frame := range replacement.Keyframes {
		if frame.AssetID == report.Keyframes[i].AssetID || frame.Frame != report.Keyframes[i].Frame {
			t.Fatal("replacement delivery reused prior evidence identity")
		}
	}
	if _, err := f.s.inspectCompositionQuality(ctx, p, output, segments, 90, 160); err == nil {
		t.Fatal("wrong expected dimensions accepted")
	}
	assets, err := f.s.Repo.Assets(ctx, p.TeamID)
	if err != nil || !reflect.DeepEqual(assets, beforeAssets) {
		t.Fatalf("quality inspection changed assets: %+v %v", assets, err)
	}
	if files := compositionQualityTestFiles(t, f.s.Blobs.Root); !reflect.DeepEqual(files, beforeFiles) {
		t.Fatalf("quality inspection changed files: %v -> %v", beforeFiles, files)
	}
}

func compositionQualityTestFiles(t *testing.T, root string) []string {
	t.Helper()
	files := []string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil {
			files = append(files, path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return files
}
