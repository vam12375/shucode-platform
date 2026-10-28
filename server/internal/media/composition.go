package media

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var compositionCheck struct {
	sync.Once
	available bool
}

func CompositionAvailable() bool {
	compositionCheck.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := exec.LookPath("ffprobe"); err != nil {
			return
		}
		filters, err := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-filters").Output()
		if err != nil || !strings.Contains(string(filters), " subtitles ") {
			return
		}
		encoders, err := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-encoders").Output()
		compositionCheck.available = err == nil && strings.Contains(string(encoders), "libx264") && strings.Contains(string(encoders), " aac ")
	})
	return compositionCheck.available
}
func subtitleTime(seconds float64) string {
	centiseconds := int(math.Round(seconds * 100))
	return fmt.Sprintf("%d:%02d:%02d.%02d", centiseconds/360000, (centiseconds/6000)%60, (centiseconds/100)%60, centiseconds%100)
}
func assText(text string) string {
	r := strings.NewReplacer("\\", "\\\\", "{", "\\{", "}", "\\}", "\r", "", "\n", "\\N")
	return r.Replace(text)
}

const defaultComposeTimeout = 8 * time.Minute
const fourKComposeTimeout = 30 * time.Minute

// 4k 片段需要两次全量 4k 编解码，给更长的合成预算；worker lease 必须覆盖它。
func composeTimeout(resolution string) time.Duration {
	if resolution == "4k" {
		return fourKComposeTimeout
	}
	return defaultComposeTimeout
}

func workerLeaseDuration(resolution string) time.Duration {
	if resolution == "4k" {
		return fourKComposeTimeout + 5*time.Minute
	}
	return 10 * time.Minute
}

// subtitleMetrics 按短边等比缩放 ASS 字幕；固定 48 号字在 4k 下会过小。
func subtitleMetrics(w, h int) (fontSize, marginLR, marginV int) {
	short := min(w, h)
	return max(24, short*48/720), max(20, short*40/720), max(40, short*80/720)
}

// 固定画幅使用同一官方尺寸表；未知比例与 auto 不能静默降为竖屏。
func outputSize(resolution, ratio string) (int, int) {
	if ratio == "auto" {
		return 0, 0
	}
	return videoTokenSize(resolution, ratio)
}

type compositionSegment struct {
	start, end      float64
	frames          int
	fadeIn, fadeOut float64
}

func (s compositionSegment) seconds() float64 { return float64(s.frames) / 30 }

func compositionTimeline(p *Project) ([]compositionSegment, error) {
	segments := make([]compositionSegment, len(p.Shots))
	offset, boundary := 0.0, 0
	for i, shot := range p.Shots {
		duration := float64(shot.Plan.Duration)
		start, end := offset, offset+duration
		if p.Brief.ReferenceAssetID != "" {
			duration = shot.Plan.SourceEnd - shot.Plan.SourceStart
			if math.IsNaN(shot.Plan.SourceStart) || math.IsInf(shot.Plan.SourceStart, 0) || math.Abs(shot.Plan.SourceStart-offset) > .001 || duration > float64(shot.Plan.Duration)+.001 {
				return nil, fmt.Errorf("invalid_source_timeline")
			}
			start, end = shot.Plan.SourceStart, shot.Plan.SourceEnd
		}
		if math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 {
			return nil, fmt.Errorf("invalid_source_timeline")
		}
		endFrame := int(math.Round(end * 30))
		if endFrame <= boundary {
			return nil, fmt.Errorf("invalid_source_timeline")
		}
		// 以全片边界分配帧数，避免每镜头独立向上取整导致分数秒片段累计拉长。
		segments[i] = compositionSegment{start: start, end: end, frames: endFrame - boundary}
		offset, boundary = end, endFrame
	}
	for i := 1; i < len(segments); i++ {
		if p.Brief.ReferenceAssetID != "" && p.Shots[i].Plan.Transition == "fade" {
			// 淡黑转场在各镜头内部完成；重叠拼接会缩短原片时间轴。
			duration := min(.15, segments[i-1].seconds()/2, segments[i].seconds()/2)
			segments[i-1].fadeOut, segments[i].fadeIn = duration, duration
		}
	}
	if len(segments) > 0 && p.Brief.ReferenceAssetID != "" && p.Shots[0].Plan.Transition == "fade" {
		segments[0].fadeIn = min(.15, segments[0].seconds()/2)
	}
	return segments, nil
}

func (s *Service) compose(ctx context.Context, p *Project) error {
	claimed, e := s.workerUpdate(ctx, p, func(v *Project) {
		if v.CompositionAssetID == "" {
			v.CompositionAssetID = uuid.NewString()
		}
		v.Status = "composing"
	})
	if e != nil {
		return e
	}
	// 读取 CAS 后的项目快照，确保下面的 blob 写入使用已持久化的稳定身份。
	p = claimed
	if resumed, resumeErr := s.resumeComposition(ctx, p); resumed || resumeErr != nil {
		return resumeErr
	}
	if err := s.validateFinishing(ctx, p.Brief); err != nil {
		return err
	}
	if !CompositionAvailable() {
		return fmt.Errorf("composition_dependencies_missing")
	}
	segments, e := compositionTimeline(p)
	if e != nil || len(segments) == 0 {
		return fmt.Errorf("invalid_source_timeline")
	}
	// 容器 /tmp 是 tmpfs，大视频中间文件写持久媒体盘，避免合成占满 API 内存。
	if e = os.MkdirAll(s.Blobs.Root, 0700); e != nil {
		return e
	}
	dir, e := os.MkdirTemp(s.Blobs.Root, ".compose-*")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	runCtx, cancel := context.WithTimeout(ctx, composeTimeout(p.Brief.Resolution))
	defer cancel()
	w, h, e := s.compositionOutputSize(runCtx, p)
	if e != nil {
		return e
	}
	if hasCinematicFinishing(p) {
		e = s.renderFinishingComposition(runCtx, p, segments, dir, w, h)
	} else {
		e = s.renderLegacyComposition(runCtx, p, segments, dir, w, h)
	}
	if e != nil {
		return e
	}
	f, e := os.Open(filepath.Join(dir, "final.mp4"))
	if e != nil {
		return e
	}
	defer f.Close()
	id, size, e := s.Blobs.PutWithID(p.TeamID, p.CompositionAssetID, f, MaxVideo)
	if e != nil {
		return e
	}
	a := Asset{ID: id, Name: p.Brief.Product + ".mp4", Kind: "video", Mime: "video/mp4", Size: size, CreatedAt: time.Now().UTC()}
	if e = s.Repo.AddAsset(ctx, p.TeamID, a); e != nil {
		return e
	}
	return s.finishComposition(ctx, p, id)
}

// resumeComposition closes the crash window after the atomic blob rename. Once
// the stable output ID has a complete blob, the render itself is durable and
// recovery only needs to idempotently register the asset and finish the project.
func (s *Service) resumeComposition(ctx context.Context, p *Project) (bool, error) {
	if p.CompositionAssetID == "" {
		return false, nil
	}
	info, err := os.Stat(s.Blobs.Path(p.TeamID, p.CompositionAssetID))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxVideo {
		return true, fmt.Errorf("composition_output_invalid")
	}
	a := Asset{ID: p.CompositionAssetID, Name: p.Brief.Product + ".mp4", Kind: "video", Mime: "video/mp4", Size: info.Size(), CreatedAt: time.Now().UTC()}
	if err = s.Repo.AddAsset(ctx, p.TeamID, a); err != nil {
		return true, err
	}
	return true, s.finishComposition(ctx, p, p.CompositionAssetID)
}

func (s *Service) finishComposition(ctx context.Context, p *Project, id string) error {
	_, e := s.workerUpdate(ctx, p, func(v *Project) {
		// 合成期间取消由平台事实决定，不能让旧 worker 快照把 cancelled 覆盖为 completed。
		if v.CancelRequested {
			v.Status = "cancelled"
			return
		}
		v.OutputAssetID = id
		v.CompositionAssetID = ""
		v.Status = "completed"
		v.Error = ""
	})
	return e
}

func (s *Service) cleanupCancelledComposition(ctx context.Context, p *Project) error {
	if p.CompositionAssetID == "" {
		return nil
	}
	id := p.CompositionAssetID
	if err := s.Repo.RemoveAsset(ctx, p.TeamID, id); err != nil {
		return err
	}
	if err := os.Remove(s.Blobs.Path(p.TeamID, id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	_, err := s.workerUpdate(ctx, p, func(v *Project) {
		if v.Status != "cancelled" || v.OutputAssetID != "" || v.CompositionAssetID != id {
			return
		}
		v.CompositionAssetID = ""
	})
	return err
}

func (s *Service) renderLegacyComposition(runCtx context.Context, p *Project, segments []compositionSegment, dir string, w, h int) error {
	var e error
	list := []string{}
	fontSize, marginLR, marginV := subtitleMetrics(w, h)
	ass := fmt.Sprintf("[Script Info]\nScriptType: v4.00+\nPlayResX: %d\nPlayResY: %d\n[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\nStyle: Default,Noto Sans CJK SC,%d,&H00FFFFFF,&H00FFFFFF,&H00000000,&H80000000,0,0,0,0,100,100,0,0,1,2,1,2,%d,%d,%d,1\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n", w, h, fontSize, marginLR, marginLR, marginV)
	for i, shot := range p.Shots {
		segment := segments[i]
		duration := segment.seconds()
		name := fmt.Sprintf("clip-%03d.mp4", i)
		filter := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=30", w, h, w, h)
		audioFilter := fmt.Sprintf("apad,atrim=duration=%.9f,asetpts=PTS-STARTPTS", duration)
		if segment.fadeIn > 0 {
			filter += fmt.Sprintf(",fade=t=in:st=0:d=%.9f", segment.fadeIn)
			audioFilter += fmt.Sprintf(",afade=t=in:st=0:d=%.9f", segment.fadeIn)
		}
		if segment.fadeOut > 0 {
			filter += fmt.Sprintf(",fade=t=out:st=%.9f:d=%.9f", duration-segment.fadeOut, segment.fadeOut)
			audioFilter += fmt.Sprintf(",afade=t=out:st=%.9f:d=%.9f", duration-segment.fadeOut, segment.fadeOut)
		}
		// 统一片段规格后拼接。已有音轨保留；无音轨时补静音，避免 concat 丢失音视频同步。
		input := s.Blobs.Path(p.TeamID, shot.OutputAssetID)
		length, err := exec.CommandContext(runCtx, "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe", "-show_entries", "format=duration", "-of", "csv=p=0", input).Output()
		var seconds float64
		if err != nil {
			return fmt.Errorf("invalid_video_result")
		}
		if _, err = fmt.Sscanf(string(length), "%f", &seconds); err != nil || seconds < float64(shot.Plan.Duration)-0.2 {
			return fmt.Errorf("video_result_too_short")
		}
		probe := exec.CommandContext(runCtx, "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe", "-select_streams", "a", "-show_entries", "stream=index", "-of", "csv=p=0", input)
		audio, _ := probe.Output()
		args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-protocol_whitelist", "file,pipe", "-i", input}
		if strings.TrimSpace(string(audio)) == "" {
			args = append(args, "-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo", "-map", "0:v:0", "-map", "1:a:0")
		} else {
			args = append(args, "-map", "0:v:0", "-map", "0:a:0")
		}
		args = append(args, "-t", fmt.Sprintf("%.9f", duration), "-frames:v", fmt.Sprint(segment.frames), "-vf", filter, "-af", audioFilter, "-c:v", "libx264", "-threads", "2", "-preset", "fast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-ar", "48000", "-ac", "2", "-shortest", name)
		cmd := exec.CommandContext(runCtx, "ffmpeg", args...)
		cmd.Dir = dir
		if e = cmd.Run(); e != nil {
			return fmt.Errorf("clip_normalization_failed: %w", e)
		}
		list = append(list, "file '"+name+"'", fmt.Sprintf("duration %.9f", duration))
		if shot.Plan.Subtitle != "" {
			ass += fmt.Sprintf("Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n", subtitleTime(segment.start), subtitleTime(segment.end), assText(shot.Plan.Subtitle))
		}
	}
	if e = os.WriteFile(filepath.Join(dir, "clips.txt"), []byte(strings.Join(list, "\n")), 0600); e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(dir, "captions.ass"), []byte(ass), 0600); e != nil {
		return e
	}
	totalFrames := 0
	for _, segment := range segments {
		totalFrames += segment.frames
	}
	cmd := exec.CommandContext(runCtx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-protocol_whitelist", "file,pipe", "-f", "concat", "-safe", "1", "-i", "clips.txt", "-t", fmt.Sprintf("%.9f", float64(totalFrames)/30), "-frames:v", fmt.Sprint(totalFrames), "-vf", "subtitles=captions.ass", "-c:v", "libx264", "-threads", "2", "-preset", "fast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-ar", "48000", "-ac", "2", "-movflags", "+faststart", "final.mp4")
	cmd.Dir = dir
	if e = cmd.Run(); e != nil {
		return fmt.Errorf("composition_failed: %w", e)
	}
	return nil
}

// Capabilities 声明客户端可使用的高级能力。
//
// 这些能力需要客户端与服务端同步演进：服务端严格解码未知字段，
// 客户端只有在能力位为 true 时才会发送对应字段。未实现的能力必须保持 false，
// 不能为了界面提前打开，否则用户填写的意图会被静默丢弃或直接 400。
func Capabilities() map[string]bool {
	return map[string]bool{
		// 创作意图（受众/语气/钩子/行动号召/禁用表述/脚本/旁白语言）已由 Brief 接收。
		"creativeBrief": true,
		// 配音、配乐、字幕样式与多方案变体尚未实现，禁止提前置真。
		"planVariants":  false,
		"voiceover":     false,
		"bgm":           false,
		"subtitleStyle": false,
	}
}
