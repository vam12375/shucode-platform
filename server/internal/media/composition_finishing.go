package media

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
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
		for _, filter := range []string{"xfade", "tpad", "acrossfade", "asplit", "sidechaincompress", "loudnorm", "alimiter", "atempo"} {
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
	if !hasCinematicFinishing(p) || index+1 >= len(segments) || p.Shots[index+1].Plan.Transition != "fade" {
		return 0
	}
	return min(9, segments[index].frames/4, segments[index+1].frames/4)
}

func finishingPictureFilter(p *Project, segments []compositionSegment) string {
	filters := make([]string, 0, len(segments)*2)
	for i := range segments {
		filters = append(filters, fmt.Sprintf("[%d:v]settb=AVTB,setpts=PTS-STARTPTS[v%d]", i, i))
	}
	current := "v0"
	for i := 1; i < len(segments); i++ {
		next := fmt.Sprintf("joined%d", i)
		if frames := finishingOverlapFrames(p, segments, i-1); frames > 0 {
			// 前镜头额外停留的尾帧参与叠化，交付边界不向前移动，也不逐段淡黑。
			filters = append(filters, fmt.Sprintf("[%s][v%d]xfade=transition=fade:duration=%.9f:offset=%.9f[%s]", current, i, float64(frames)/30, segments[i].start, next))
		} else {
			filters = append(filters, fmt.Sprintf("[%s][v%d]concat=n=2:v=1:a=0[%s]", current, i, next))
		}
		current = next
	}
	filters = append(filters, fmt.Sprintf("[%s]format=yuv420p[picture]", current))
	return strings.Join(filters, ";")
}

func finishingNarrationRate(duration, target float64) (float64, error) {
	if math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 || math.IsNaN(target) || math.IsInf(target, 0) || target <= .15 {
		return 0, fmt.Errorf("narration_audio_invalid")
	}
	// 保留收尾呼吸；不能直接 atrim 长旁白，把用户最后一句无声截掉。
	rate := max(1, duration/(target-.15))
	if rate > 1.15 {
		return 0, fmt.Errorf("narration_too_long")
	}
	return rate, nil
}

func runFinishingCommand(ctx context.Context, dir, code string, args ...string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", append([]string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}, args...)...)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", code, err)
	}
	return nil
}

func (s *Service) renderFinishingComposition(ctx context.Context, p *Project, segments []compositionSegment, dir string, w, h int) error {
	if !FinishingCompositionAvailable() {
		return fmt.Errorf("composition_dependencies_missing")
	}
	frames := 0
	for _, segment := range segments {
		frames += segment.frames
	}
	duration := float64(frames) / 30
	narration := ""
	if p.Brief.Finishing.Voiceover {
		var err error
		narration, err = s.prepareNarration(ctx, p, dir)
		if err != nil {
			return err
		}
	}
	pictureArgs := []string{"-protocol_whitelist", "file,pipe", "-filter_complex_threads", "1"}
	for i, shot := range p.Shots {
		segment := segments[i]
		input := s.Blobs.Path(p.TeamID, shot.OutputAssetID)
		raw, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe", "-show_entries", "format=duration", "-of", "csv=p=0", input).Output()
		length, parseErr := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
		if err != nil || parseErr != nil || math.IsNaN(length) || math.IsInf(length, 0) || (length < segment.seconds()-.2 && !isWholeVideoReferenceExecution(p)) {
			return fmt.Errorf("video_result_too_short")
		}
		tail := finishingOverlapFrames(p, segments, i)
		clipFrames := segment.frames + tail
		clipDuration := float64(clipFrames) / 30
		name := fmt.Sprintf("clip-%03d.mp4", i)
		// 自动生成统一满幅；旧的 decrease+pad 会把横版素材嵌入竖片，留下明显黑边。
		shortfall := max(0, segment.seconds()-length)
		filter := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,setsar=1,fps=30,trim=duration=%.9f,setpts=PTS-STARTPTS,tpad=stop_mode=clone:stop_duration=%.9f", w, h, w, h, segment.seconds(), max(float64(tail)/30, shortfall))
		if err = runFinishingCommand(ctx, dir, "clip_normalization_failed", "-protocol_whitelist", "file,pipe", "-i", input, "-an", "-sn", "-dn", "-vf", filter, "-t", fmt.Sprintf("%.9f", clipDuration), "-frames:v", strconv.Itoa(clipFrames), "-c:v", "libx264", "-threads", "2", "-preset", "fast", "-crf", "18", "-pix_fmt", "yuv420p", name); err != nil {
			return err
		}
		pictureArgs = append(pictureArgs, "-i", name)
	}
	pictureArgs = append(pictureArgs, "-filter_complex", finishingPictureFilter(p, segments), "-map", "[picture]", "-an", "-t", fmt.Sprintf("%.9f", duration), "-frames:v", strconv.Itoa(frames), "-r", "30", "-c:v", "libx264", "-threads", "2", "-preset", "fast", "-crf", "18", "-pix_fmt", "yuv420p", "picture.mp4")
	if err := runFinishingCommand(ctx, dir, "composition_failed", pictureArgs...); err != nil {
		return err
	}
	master, err := s.finishingMasterAudio(ctx, p, dir, duration, narration)
	if err != nil {
		return err
	}
	args := []string{"-protocol_whitelist", "file,pipe", "-i", "picture.mp4", "-i", master, "-map", "0:v:0", "-map", "1:a:0"}
	if p.Brief.Finishing.Subtitles == "short" {
		if err = writeFinishingCaptions(p, segments, dir, w, h); err != nil {
			return err
		}
		args = append(args, "-vf", "subtitles=captions.ass", "-c:v", "libx264", "-threads", "2", "-preset", "fast", "-crf", "18", "-pix_fmt", "yuv420p")
	} else {
		args = append(args, "-c:v", "copy")
	}
	args = append(args, "-t", fmt.Sprintf("%.9f", duration), "-c:a", "aac", "-b:a", "192k", "-ar", "48000", "-ac", "2", "-movflags", "+faststart", "final.mp4")
	if err = runFinishingCommand(ctx, dir, "composition_failed", args...); err != nil {
		return err
	}
	return validateFinishingOutput(ctx, filepath.Join(dir, "final.mp4"), frames)
}

func (s *Service) finishingMasterAudio(ctx context.Context, p *Project, dir string, duration float64, narration string) (string, error) {
	args := []string{"-protocol_whitelist", "file,pipe", "-filter_complex_threads", "1"}
	filters := []string{}
	index := 0
	voice, music := narration != "", p.Brief.Finishing.Music
	if voice {
		seconds, err := probeFinishingAudio(ctx, narration)
		if err != nil {
			return "", fmt.Errorf("narration_audio_invalid")
		}
		rate, err := finishingNarrationRate(seconds, duration)
		if err != nil {
			return "", err
		}
		args = append(args, "-i", narration)
		filters = append(filters, fmt.Sprintf("[%d:a]aresample=48000,aformat=sample_fmts=fltp:channel_layouts=stereo,atempo=%.9f,loudnorm=I=-16:TP=-1.5:LRA=7,aresample=48000,apad,atrim=duration=%.9f,asetpts=PTS-STARTPTS[voice]", index, rate, duration))
		index++
	}
	if music {
		bed, err := s.finishingMusicBed(ctx, dir, duration)
		if err != nil {
			return "", err
		}
		args = append(args, "-i", bed)
		filters = append(filters, fmt.Sprintf("[%d:a]loudnorm=I=-26:TP=-3:LRA=9,aresample=48000,afade=t=in:st=0:d=%.9f,afade=t=out:st=%.9f:d=%.9f[music]", index, min(.5, duration/4), duration-min(1.2, duration/4), min(1.2, duration/4)))
	}
	switch {
	case voice && music:
		// 旁白和单条配乐只在全片主时间轴混合一次，音乐不会因切镜重新起播。
		filters = append(filters, "[voice]asplit=2[spoken][sidechain]", "[music][sidechain]sidechaincompress=threshold=0.025:ratio=6:attack=20:release=450[ducked]", "[spoken][ducked]amix=inputs=2:duration=longest:dropout_transition=0:normalize=0,alimiter=limit=0.95:level=false:latency=true[mixed]")
	case voice:
		filters = append(filters, "[voice]alimiter=limit=0.95:level=false:latency=true[mixed]")
	case music:
		filters = append(filters, "[music]alimiter=limit=0.95:level=false:latency=true[mixed]")
	default:
		args = append(args, "-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo")
		filters = append(filters, fmt.Sprintf("[0:a]atrim=duration=%.9f[mixed]", duration))
	}
	// 重采样、变速与 limiter 会改变包时间戳；按 -t 截止曾提前少交付尾部 75ms。
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

func (s *Service) finishingMusicBed(ctx context.Context, dir string, duration float64) (string, error) {
	seconds, err := probeFinishingAudio(ctx, s.BackgroundMusicPath)
	if err != nil || seconds < 1 {
		return "", fmt.Errorf("background_music_unavailable")
	}
	seconds = min(seconds, duration)
	overlap := min(.35, seconds/4)
	count := max(1, int(math.Ceil((duration-seconds)/(seconds-overlap)))+1)
	if count > 160 {
		return "", fmt.Errorf("background_music_unavailable")
	}
	filter := fmt.Sprintf("[0:a]aresample=48000,aformat=sample_fmts=fltp:channel_layouts=stereo,atrim=duration=%.9f,asetpts=PTS-STARTPTS", seconds)
	if count == 1 {
		filter += "[bed]"
	} else {
		filter += fmt.Sprintf(",asplit=%d", count)
		for i := range count {
			filter += fmt.Sprintf("[part%d]", i)
		}
		current := "part0"
		for i := 1; i < count; i++ {
			next := fmt.Sprintf("loop%d", i)
			if i == count-1 {
				next = "bed"
			}
			filter += fmt.Sprintf(";[%s][part%d]acrossfade=d=%.9f:c1=qsin:c2=qsin[%s]", current, i, overlap, next)
			current = next
		}
	}
	path := filepath.Join(dir, "music.wav")
	if err = runFinishingCommand(ctx, dir, "audio_mix_failed", "-protocol_whitelist", "file,pipe", "-filter_complex_threads", "1", "-i", s.BackgroundMusicPath, "-filter_complex", filter, "-map", "[bed]", "-t", fmt.Sprintf("%.9f", duration), "-c:a", "pcm_s16le", "-ar", "48000", "-ac", "2", path); err != nil {
		return "", err
	}
	return path, nil
}

func writeFinishingCaptions(p *Project, segments []compositionSegment, dir string, w, h int) error {
	fontSize, marginLR, marginV := subtitleMetrics(w, h)
	// 新后期默认无字幕；显式 short 仅显示短句，不能假装是按旁白逐词对齐。
	ass := fmt.Sprintf("[Script Info]\nScriptType: v4.00+\nPlayResX: %d\nPlayResY: %d\n[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\nStyle: Default,Noto Sans CJK SC,%d,&H00FFFFFF,&H00FFFFFF,&H00000000,&H80000000,0,0,0,0,100,100,0,0,1,1,0,2,%d,%d,%d,1\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n", w, h, max(20, fontSize*3/4), marginLR, marginLR, marginV)
	for i, shot := range p.Shots {
		if shot.Plan.Subtitle != "" {
			ass += fmt.Sprintf("Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n", subtitleTime(segments[i].start+.15), subtitleTime(segments[i].end-.15), assText(shot.Plan.Subtitle))
		}
	}
	return os.WriteFile(filepath.Join(dir, "captions.ass"), []byte(ass), 0600)
}

func validateFinishingOutput(ctx context.Context, path string, frames int) error {
	raw, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe", "-show_entries", "stream=codec_type,codec_name,nb_frames,duration:format=duration", "-of", "json", path).Output()
	var metadata struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Frames    string `json:"nb_frames"`
			Duration  string `json:"duration"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err != nil || json.Unmarshal(raw, &metadata) != nil || len(metadata.Streams) != 2 {
		return fmt.Errorf("composition_output_invalid")
	}
	duration := float64(frames) / 30
	length, err := strconv.ParseFloat(metadata.Format.Duration, 64)
	if err != nil || math.IsNaN(length) || math.Abs(length-duration) > .05 {
		return fmt.Errorf("composition_output_invalid")
	}
	video, audio := false, false
	for _, stream := range metadata.Streams {
		if stream.CodecType == "video" && stream.CodecName == "h264" && stream.Frames == strconv.Itoa(frames) {
			video = true
		}
		if stream.CodecType == "audio" && stream.CodecName == "aac" {
			seconds, err := strconv.ParseFloat(stream.Duration, 64)
			audio = err == nil && !math.IsNaN(seconds) && math.Abs(seconds-duration) <= .05
		}
	}
	if !video || !audio {
		return fmt.Errorf("composition_output_invalid")
	}
	return runFinishingCommand(ctx, filepath.Dir(path), "composition_output_invalid", "-protocol_whitelist", "file,pipe", "-xerror", "-i", path, "-map", "0:v:0", "-map", "0:a:0", "-f", "null", "-")
}
