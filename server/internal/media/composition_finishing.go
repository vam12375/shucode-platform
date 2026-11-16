package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var finishingCompositionCheck struct {
	sync.Once
	available bool
}

// FinishingCompositionAvailable 在任何镜头付费前检查后期实际依赖，避免提交后才发现缺滤镜。
func FinishingCompositionAvailable() bool {
	finishingCompositionCheck.Do(func() {
		if !CompositionAvailable() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		raw, err := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-filters").Output()
		if err != nil {
			return
		}
		for _, filter := range []string{"xfade", "tpad", "atempo"} {
			if !strings.Contains(string(raw), " "+filter+" ") {
				return
			}
		}
		finishingCompositionCheck.available = true
	})
	return finishingCompositionCheck.available
}

func hasCinematicFinishing(p *Project) bool {
	return p.Canvas == nil && p.Brief.Mode == "auto" && p.Brief.ReferenceAssetID == "" && p.Brief.Finishing != nil
}

func finishingOverlapFrames(p *Project, segments []compositionSegment, index int) int {
	if !hasCinematicFinishing(p) || index+1 >= len(segments) {
		return 0
	}
	next := finishingSourceClip(p, index+1)
	if p.Shots[next.SourceShotIndex].Plan.Transition != "fade" {
		return 0
	}
	return min(9, segments[index].frames/4, segments[index+1].frames/4)
}

func finishingPictureFilter(p *Project, segments []compositionSegment) string {
	filters := make([]string, 0, len(segments)*2)
	for i := range segments {
		// FFmpeg 7 的 setpts/concat 会丢失恒定帧率元数据，xfade 因 1/0 拒绝输入；显式恢复交付帧率。
		filters = append(filters, fmt.Sprintf("[%d:v]settb=AVTB,setpts=PTS-STARTPTS,fps=30[v%d]", i, i))
	}
	current := "v0"
	for i := 1; i < len(segments); i++ {
		next := fmt.Sprintf("joined%d", i)
		if frames := finishingOverlapFrames(p, segments, i-1); frames > 0 {
			// 前镜头额外停留的尾帧参与叠化，交付边界不向前移动，也不逐段淡黑。
			filters = append(filters, fmt.Sprintf("[%s][v%d]xfade=transition=fade:duration=%.9f:offset=%.9f[%s]", current, i, float64(frames)/30, segments[i].start, next))
		} else {
			filters = append(filters, fmt.Sprintf("[%s][v%d]concat=n=2:v=1:a=0,fps=30[%s]", current, i, next))
		}
		current = next
	}
	filters = append(filters, fmt.Sprintf("[%s]format=yuv420p[picture]", current))
	return strings.Join(filters, ";")
}

func runFinishingCommand(ctx context.Context, dir, code string, args ...string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", append([]string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}, args...)...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", code, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (s *Service) renderFinishingComposition(ctx context.Context, p *Project, segments []compositionSegment, dir string, w, h int) error {
	if !FinishingCompositionAvailable() {
		return fmt.Errorf("composition_dependencies_missing")
	}
	options := effectiveFinishingOptions(p)
	frames := compositionTotalFrames(segments)
	duration := float64(frames) / 30
	pictureArgs := []string{"-protocol_whitelist", "file,pipe", "-filter_complex_threads", "1"}
	native := []string{}
	for i := range segments {
		clip := finishingSourceClip(p, i)
		if clip.SourceShotIndex < 0 || clip.SourceShotIndex >= len(p.Shots) {
			return fmt.Errorf("composition_source_invalid")
		}
		shot := p.Shots[clip.SourceShotIndex]
		segment := segments[i]
		input := s.Blobs.Path(p.TeamID, shot.OutputAssetID)
		length, err := probeCompositionStreamSeconds(ctx, input, "v:0")
		if err != nil || !compositionSourceCovers(length, float64(clip.OutFrame)/30) {
			if p.Composition != nil {
				return fmt.Errorf("composition_source_invalid")
			}
			return fmt.Errorf("video_result_too_short")
		}
		tail := finishingOverlapFrames(p, segments, i)
		clipFrames := segment.frames + tail
		clipDuration := float64(clipFrames) / 30
		name := fmt.Sprintf("clip-%03d.mp4", i)
		// 自动生成统一满幅；旧的 decrease+pad 会把横版素材嵌入竖片，留下明显黑边。
		shortfall := max(0, (float64(clip.OutFrame)/30-length)/clip.Speed)
		// tpad 之后帧率元数据会变成 1/0，xfade 要求恒定帧率；最后再强制一次 30fps。
		filter := fmt.Sprintf("setpts=PTS-STARTPTS,trim=start=%.9f:end=%.9f,setpts=(PTS-STARTPTS)/%.9f,scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,setsar=1,fps=30,tpad=stop_mode=clone:stop_duration=%.9f,fps=30", float64(clip.InFrame)/30, float64(clip.OutFrame)/30, clip.Speed, w, h, w, h, float64(tail)/30+shortfall)
		if err = runFinishingCommand(ctx, dir, "clip_normalization_failed", "-xerror", "-protocol_whitelist", "file,pipe", "-i", input, "-an", "-sn", "-dn", "-vf", filter, "-t", fmt.Sprintf("%.9f", clipDuration), "-frames:v", strconv.Itoa(clipFrames), "-c:v", "libx264", "-threads", "2", "-preset", "fast", "-crf", "18", "-pix_fmt", "yuv420p", name); err != nil {
			return err
		}
		pictureArgs = append(pictureArgs, "-i", name)
		if options.NativeAudio {
			// 原声按交付段长度（不含叠化尾帧）切出并对齐源片入出帧与变速；供应商未返回音轨时用静音占位，保持时间轴不漂移。
			audioName := fmt.Sprintf("clip-%03d.wav", i)
			if err = s.extractFinishingNativeAudio(ctx, dir, input, audioName, clip, segment.seconds()); err != nil {
				return err
			}
			native = append(native, audioName)
		}
	}
	pictureArgs = append(pictureArgs, "-filter_complex", finishingPictureFilter(p, segments), "-map", "[picture]", "-an", "-t", fmt.Sprintf("%.9f", duration), "-frames:v", strconv.Itoa(frames), "-r", "30", "-c:v", "libx264", "-threads", "2", "-preset", "fast", "-crf", "18", "-pix_fmt", "yuv420p", "picture.mp4")
	if err := runFinishingCommand(ctx, dir, "composition_failed", pictureArgs...); err != nil {
		return err
	}
	master, err := s.finishingMasterAudio(ctx, p, dir, duration, native)
	if err != nil {
		return err
	}
	args := []string{"-protocol_whitelist", "file,pipe", "-filter_complex_threads", "1", "-i", "picture.mp4", "-i", master}
	logo, err := s.prepareCompositionBrandLogo(ctx, p, options.Brand, dir, w, h)
	if err != nil {
		return err
	}
	if logo != "" {
		args = append(args, "-loop", "1", "-i", logo)
	}
	if options.Subtitles == "short" || compositionHasTail(options.Brand) {
		if err = writeFinishingCaptions(p, segments, dir, w, h); err != nil {
			return err
		}
		if logo != "" {
			// overlay 的 enable 变量 n 在 ffmpeg 6.1/7.1 数的不是主输入帧，尾卡 Logo 会整段丢失；
			// 与 ASS 尾卡一致改用秒级时间戳启用。
			tailStart := float64(max(0, frames-30)) / 30
			filter := fmt.Sprintf("[0:v]subtitles=captions.ass[branded];[2:v]scale=%d:%d:force_original_aspect_ratio=decrease,format=rgba[logo];[branded][logo]overlay=x=(W-w)/2:y=H*0.15:enable='gte(t,%.9f)':shortest=1[delivery]", max(2, w/5), max(2, h/6), tailStart)
			args = append(args, "-filter_complex", filter, "-map", "[delivery]")
		} else {
			args = append(args, "-map", "0:v:0", "-vf", "subtitles=captions.ass")
		}
		args = append(args, "-c:v", "libx264", "-threads", "2", "-preset", "fast", "-crf", "18", "-pix_fmt", "yuv420p")
	} else {
		args = append(args, "-map", "0:v:0", "-c:v", "copy")
	}
	args = append(args, "-map", "1:a:0", "-t", fmt.Sprintf("%.9f", duration), "-frames:v", strconv.Itoa(frames), "-c:a", "aac", "-b:a", "192k", "-ar", "48000", "-ac", "2", "-movflags", "+faststart", "final.mp4")
	if err = runFinishingCommand(ctx, dir, "composition_failed", args...); err != nil {
		return err
	}
	return validateFinishingOutput(ctx, filepath.Join(dir, "final.mp4"), duration)
}

// extractFinishingNativeAudio 把单个镜头的原声切到交付段：入出帧与变速和画面同源，缺音轨时补静音。
func (s *Service) extractFinishingNativeAudio(ctx context.Context, dir, input, name string, clip VideoCompositionClip, seconds float64) error {
	raw, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe", "-select_streams", "a:0", "-show_entries", "stream=codec_type", "-of", "csv=p=0", input).Output()
	hasAudio := err == nil && strings.Contains(string(raw), "audio")
	args := []string{"-protocol_whitelist", "file,pipe"}
	if hasAudio {
		filter := fmt.Sprintf("atrim=start=%.9f:end=%.9f,asetpts=PTS-STARTPTS,atempo=%.9f,aresample=48000,aformat=sample_fmts=fltp:channel_layouts=stereo,apad,atrim=duration=%.9f", float64(clip.InFrame)/30, float64(clip.OutFrame)/30, clip.Speed, seconds)
		args = append(args, "-i", input, "-vn", "-sn", "-dn", "-af", filter)
	} else {
		args = append(args, "-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo", "-af", fmt.Sprintf("atrim=duration=%.9f", seconds))
	}
	args = append(args, "-t", fmt.Sprintf("%.9f", seconds), "-c:a", "pcm_s16le", "-ar", "48000", "-ac", "2", name)
	return runFinishingCommand(ctx, dir, "native_audio_failed", args...)
}

func (s *Service) finishingMasterAudio(ctx context.Context, p *Project, dir string, duration float64, native []string) (string, error) {
	args := []string{"-protocol_whitelist", "file,pipe", "-filter_complex_threads", "1"}
	filters := []string{}
	index := 0
	if len(native) > 0 {
		// 原声在独立生成段边界可能出现样本突跳；只在接缝两侧做5ms去点击，不交叠对白或缩短时间轴。
		var nativeSegments []compositionSegment
		if p.Brief.GenerationPolicy == "whole-first" && len(native) > 1 {
			var err error
			nativeSegments, err = compositionTimeline(p)
			if err != nil || len(nativeSegments) != len(native) {
				return "", fmt.Errorf("native_audio_timeline_invalid")
			}
		}
		labels := make([]string, 0, len(native))
		for i, name := range native {
			args = append(args, "-i", name)
			label := fmt.Sprintf("n%d", i)
			seam := ""
			if len(nativeSegments) > 0 {
				if i > 0 {
					seam += ",afade=t=in:st=0:d=0.005"
				}
				if i+1 < len(native) {
					seam += fmt.Sprintf(",afade=t=out:st=%.9f:d=0.005", nativeSegments[i].seconds()-.005)
				}
			}
			filters = append(filters, fmt.Sprintf("[%d:a]aresample=48000,aformat=sample_fmts=fltp:channel_layouts=stereo%s[%s]", index, seam, label))
			labels = append(labels, "["+label+"]")
			index++
		}
		filters = append(filters, fmt.Sprintf("%sconcat=n=%d:v=0:a=1,aresample=48000,apad,atrim=duration=%.9f,asetpts=PTS-STARTPTS[native]", strings.Join(labels, ""), len(native), duration))
	}
	// loudnorm曾将很轻的模型底噪抬到目标响度；只保留原声采样，不增益、不叠加音轨。
	if len(native) > 0 {
		filters = append(filters, "[native]anull[mixed]")
	} else {
		args = append(args, "-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo")
		filters = append(filters, fmt.Sprintf("[0:a]atrim=duration=%.9f[mixed]", duration))
	}
	// 重采样、变速会改变包时间戳；按 -t 截止曾提前少交付尾部 75ms。
	// 用固定采样数限定最终主轨，不让滤镜延迟截掉成片末尾。
	samples := int(math.Round(duration * 48000))
	filters = append(filters, fmt.Sprintf("[mixed]aresample=48000,apad=whole_len=%d,atrim=end_sample=%d,asetpts=N/SR/TB[master]", samples, samples))
	path := filepath.Join(dir, "master.wav")
	args = append(args, "-filter_complex", strings.Join(filters, ";"), "-map", "[master]", "-c:a", "pcm_s16le", "-ar", "48000", "-ac", "2", path)
	if err := runFinishingCommand(ctx, dir, "audio_mix_failed", args...); err != nil {
		return "", err
	}
	return path, nil
}

func writeFinishingCaptions(p *Project, segments []compositionSegment, dir string, w, h int) error {
	return writeCompositionCaptions(p, segments, dir, w, h)
}

func validateFinishingOutput(ctx context.Context, path string, duration float64) error {
	raw, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe", "-show_entries", "stream=codec_type,codec_name,duration:format=duration", "-of", "json", path).Output()
	var metadata struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Duration  string `json:"duration"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err != nil || json.Unmarshal(raw, &metadata) != nil || len(metadata.Streams) != 2 {
		return fmt.Errorf("composition_output_invalid")
	}
	length, err := strconv.ParseFloat(metadata.Format.Duration, 64)
	if err != nil || !durationMatches(length, duration) {
		return fmt.Errorf("composition_output_invalid")
	}
	video, audio := false, false
	for _, stream := range metadata.Streams {
		if stream.CodecType == "video" && stream.CodecName == "h264" {
			seconds, probeErr := probeCompositionStreamSeconds(ctx, path, "v:0")
			video = probeErr == nil && durationMatches(seconds, duration)
		}
		if stream.CodecType == "audio" && stream.CodecName == "aac" {
			seconds, probeErr := probeCompositionStreamSeconds(ctx, path, "a:0")
			audio = probeErr == nil && durationMatches(seconds, duration)
		}
	}
	if !video || !audio {
		return fmt.Errorf("composition_output_invalid")
	}
	return runFinishingCommand(ctx, filepath.Dir(path), "composition_output_invalid", "-protocol_whitelist", "file,pipe", "-xerror", "-i", path, "-map", "0:v:0", "-map", "0:a:0", "-f", "null", "-")
}
