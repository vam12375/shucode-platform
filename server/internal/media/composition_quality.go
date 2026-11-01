package media

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

var qualityBlackStart = regexp.MustCompile("black_start:([0-9.]+)")
var qualitySilenceDuration = regexp.MustCompile("silence_duration: ([0-9.]+)")

// 技术检查只记录真实成片证据；商品一致性和审美始终保留为尚未人工审核。
func (s *Service) inspectCompositionQuality(ctx context.Context, p *Project, path string, segments []compositionSegment, w, h int) (*VideoQualityReport, error) {
	frames := compositionTotalFrames(segments)
	report := &VideoQualityReport{Version: 1, Status: "passed", CheckedAt: time.Now().UTC(), FPS: 30, TotalFrames: frames, Width: w, Height: h, Checks: []VideoQualityCheck{}, Keyframes: []VideoQualityKeyframe{}, VisualReview: "unverified"}
	fail := func(code string) (*VideoQualityReport, error) {
		report.Status = "failed"
		report.Checks = append(report.Checks, VideoQualityCheck{Code: code, Status: "failed"})
		return report, fmt.Errorf("composition_output_invalid")
	}
	if frames <= 0 || w < 2 || h < 2 {
		return fail("frame_clock")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	raw, err := exec.CommandContext(probeCtx, "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe", "-show_entries", "stream=codec_type,width,height,avg_frame_rate:format=format_name", "-of", "json", path).Output()
	cancel()
	var metadata struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			Rate      string `json:"avg_frame_rate"`
		} `json:"streams"`
		Format struct {
			Name string `json:"format_name"`
		} `json:"format"`
	}
	if err != nil || json.Unmarshal(raw, &metadata) != nil || !strings.Contains(","+metadata.Format.Name+",", ",mp4,") {
		return fail("container")
	}
	videoFound := false
	for _, stream := range metadata.Streams {
		if stream.CodecType != "video" {
			continue
		}
		if stream.Width != w || stream.Height != h {
			return fail("dimensions")
		}
		var numerator, denominator float64
		if _, err := fmt.Sscanf(stream.Rate, "%f/%f", &numerator, &denominator); err != nil || denominator <= 0 || math.Abs(numerator/denominator-30) > .001 {
			return fail("frame_clock")
		}
		videoFound = true
	}
	if !videoFound {
		return fail("video_stream")
	}
	if err := validateFinishingOutput(ctx, path, frames); err != nil {
		return fail("decode")
	}
	for _, code := range []string{"container", "dimensions", "frame_clock", "audio_stream", "decode"} {
		report.Checks = append(report.Checks, VideoQualityCheck{Code: code, Status: "passed"})
	}
	analysisCtx, cancel := context.WithTimeout(ctx, composeTimeout(p.Brief.Resolution))
	analysis, err := exec.CommandContext(analysisCtx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "info", "-protocol_whitelist", "file,pipe", "-i", path, "-vf", "blackdetect=d=0.1:pix_th=0.10", "-af", "silencedetect=noise=-50dB:d=0.8", "-f", "null", "-").CombinedOutput()
	cancel()
	if err != nil {
		return fail("signal_analysis")
	}
	if match := qualityBlackStart.FindStringSubmatch(string(analysis)); len(match) == 2 {
		seconds, _ := strconv.ParseFloat(match[1], 64)
		frame := min(frames-1, max(0, int(math.Round(seconds*30))))
		report.Status = "warning"
		report.Checks = append(report.Checks, VideoQualityCheck{Code: "black_frames", Status: "warning", Frame: &frame})
	} else {
		report.Checks = append(report.Checks, VideoQualityCheck{Code: "black_frames", Status: "passed"})
	}
	options := effectiveFinishingOptions(p)
	expectSound := options.Voiceover || options.Music
	if p.Composition != nil && p.Composition.Edit.MusicVolume != nil && *p.Composition.Edit.MusicVolume == 0 && !options.Voiceover {
		expectSound = false
	}
	longSilence := false
	for _, match := range qualitySilenceDuration.FindAllStringSubmatch(string(analysis), -1) {
		seconds, _ := strconv.ParseFloat(match[1], 64)
		if seconds >= 1 {
			longSilence = true
		}
	}
	if expectSound && longSilence {
		report.Status = "warning"
		report.Checks = append(report.Checks, VideoQualityCheck{Code: "silent_audio", Status: "warning"})
	} else {
		report.Checks = append(report.Checks, VideoQualityCheck{Code: "silent_audio", Status: "passed"})
	}
	appendCompositionReadability(report, p, segments)
	keyframes, err := s.extractCompositionQualityFrames(ctx, p, path, segments)
	if err != nil {
		return fail("keyframes")
	}
	report.Keyframes = keyframes
	report.Checks = append(report.Checks, VideoQualityCheck{Code: "visual_review", Status: "unverified"})
	return report, nil
}

func appendCompositionReadability(report *VideoQualityReport, p *Project, segments []compositionSegment) {
	options := effectiveFinishingOptions(p)
	if compositionHasTail(options.Brand) && narrationCopyWidth(options.Brand.EndTitle+options.Brand.EndSubtitle) > 5.5 {
		frame := max(0, report.TotalFrames-30)
		report.Status = "warning"
		report.Checks = append(report.Checks, VideoQualityCheck{Code: "end_card_readability", Status: "warning", Frame: &frame})
	}
	if options.Subtitles != "short" {
		return
	}
	for index, segment := range segments {
		text := finishingSubtitle(p, index)
		if text == "" {
			continue
		}
		start, end := finishingCaptionWindow(p, segments, index)
		lines := strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
		tooLong := end <= start || narrationCopyWidth(text) > math.Floor(max(0, end-start)*5.5) || len(lines) > 2
		for _, line := range lines {
			tooLong = tooLong || narrationCopyWidth(line) > 18
		}
		if tooLong {
			frame := min(report.TotalFrames-1, max(0, int(math.Round(segment.start*30))))
			report.Status = "warning"
			report.Checks = append(report.Checks, VideoQualityCheck{Code: "caption_readability", Status: "warning", Frame: &frame})
		}
	}
}

func compositionQualityFrames(p *Project, segments []compositionSegment) []VideoQualityKeyframe {
	frames := make([]VideoQualityKeyframe, 0, len(segments)*3)
	start := 0
	for index, segment := range segments {
		source := finishingSourceClip(p, index).SourceShotIndex
		for _, frame := range []int{start + min(4, segment.frames/4), start + segment.frames/2, start + segment.frames - 1} {
			sourceIndex := source
			id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(p.CompositionAssetID+"/quality/"+strconv.Itoa(frame))).String()
			frames = append(frames, VideoQualityKeyframe{Frame: frame, AssetID: id, SourceShotIndex: &sourceIndex})
		}
		start += segment.frames
	}
	return frames
}

// 取消只清理本轮尚未交付的稳定证据身份；旧成片仍被当前报告引用的图片必须保留。
func (s *Service) cleanupCompositionQuality(ctx context.Context, p *Project, id string) error {
	if id == "" || p.CompositionAssetID != id {
		return nil
	}
	latest, err := s.Repo.Get(ctx, p.TeamID, p.ID)
	if err != nil || latest == nil {
		return err
	}
	if latest.Status != "cancelled" || latest.CompositionAssetID != id || latest.OutputAssetID == id || !sameCompositionState(latest.Composition, p.Composition) {
		return nil
	}
	segments, err := compositionTimeline(p)
	if err != nil {
		return err
	}
	return s.removeCompositionQualityFrames(ctx, p, latest, segments)
}

// 失去当前pending身份后才能回收未绑定证据，避免新worker恢复相同稳定成片时被旧worker误删。
func (s *Service) cleanupUnboundCompositionQuality(ctx context.Context, p *Project, id string) error {
	if id == "" || p.CompositionAssetID != id {
		return nil
	}
	latest, err := s.Repo.Get(ctx, p.TeamID, p.ID)
	if err != nil || latest == nil {
		return err
	}
	if latest.OutputAssetID == id || latest.CompositionAssetID == id {
		return nil
	}
	segments, err := compositionTimeline(p)
	if err != nil {
		return err
	}
	return s.removeCompositionQualityFrames(ctx, p, latest, segments)
}

func (s *Service) removeCompositionQualityFrames(ctx context.Context, p, latest *Project, segments []compositionSegment) error {
	protected := map[string]bool{}
	if latest.QualityReport != nil {
		for _, frame := range latest.QualityReport.Keyframes {
			protected[frame.AssetID] = true
		}
	}
	for _, frame := range compositionQualityFrames(p, segments) {
		if protected[frame.AssetID] {
			continue
		}
		if err := s.Repo.RemoveAsset(ctx, p.TeamID, frame.AssetID); err != nil {
			return err
		}
		if err := os.Remove(s.Blobs.Path(p.TeamID, frame.AssetID)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *Service) extractCompositionQualityFrames(ctx context.Context, p *Project, path string, segments []compositionSegment) ([]VideoQualityKeyframe, error) {
	if err := os.MkdirAll(s.Blobs.Root, 0700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(s.Blobs.Root, ".quality-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	frames := compositionQualityFrames(p, segments)
	selects := make([]string, 0, len(frames))
	for _, frame := range frames {
		selects = append(selects, fmt.Sprintf("eq(n,%d)", frame.Frame))
	}
	filter := "select='" + strings.Join(selects, "+") + "',scale=w='min(1280,iw)':h=-2"
	if err := runFinishingCommand(ctx, dir, "composition_quality_failed", "-protocol_whitelist", "file,pipe", "-i", path, "-map", "0:v:0", "-vf", filter, "-fps_mode", "vfr", "-frames:v", strconv.Itoa(len(frames)), "-q:v", "3", "-threads", "1", "frame-%03d.jpg"); err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if complete {
			return
		}
		// 稳定帧可能已被接管worker复用，pre-stat不能证明独占；仅退休pending身份后统一回收。
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = s.cleanupUnboundCompositionQuality(cleanup, p, p.CompositionAssetID)
	}()
	for i := range frames {
		file, err := os.Open(filepath.Join(dir, fmt.Sprintf("frame-%03d.jpg", i+1)))
		if err != nil {
			return nil, err
		}
		id := frames[i].AssetID
		_, size, err := s.Blobs.PutWithID(p.TeamID, id, file, MaxUpload)
		file.Close()
		if err != nil {
			return nil, err
		}
		asset := Asset{ID: id, Name: fmt.Sprintf("review-frame-%d.jpg", frames[i].Frame), Kind: "image", Mime: "image/jpeg", Size: size, Internal: true, CreatedAt: time.Now().UTC()}
		if err := s.Repo.AddAsset(ctx, p.TeamID, asset); err != nil {
			return nil, err
		}
	}
	complete = true
	return frames, nil
}
