package media

import (
	"context"
	"fmt"
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
func subtitleTime(seconds int) string {
	return fmt.Sprintf("%d:%02d:%02d.00", seconds/3600, (seconds/60)%60, seconds%60)
}
func assText(text string) string {
	r := strings.NewReplacer("\\", "\\\\", "{", "\\{", "}", "\\}", "\r", "", "\n", "\\N")
	return r.Replace(text)
}
func (s *Service) compose(ctx context.Context, p *Project) error {
	if !CompositionAvailable() {
		return fmt.Errorf("composition_dependencies_missing")
	}
	_, e := s.workerUpdate(ctx, p, func(v *Project) { v.Status = "composing" })
	if e != nil {
		return e
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
	runCtx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	w, h := 720, 1280
	if p.Brief.Resolution == "1080p" {
		w, h = 1080, 1920
	}
	if p.Brief.AspectRatio == "16:9" {
		w, h = h, w
	}
	if p.Brief.AspectRatio == "1:1" {
		h = w
	}
	list := []string{}
	ass := "[Script Info]\nScriptType: v4.00+\nPlayResX: " + fmt.Sprint(w) + "\nPlayResY: " + fmt.Sprint(h) + "\n[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\nStyle: Default,Noto Sans CJK SC,48,&H00FFFFFF,&H00FFFFFF,&H00000000,&H80000000,0,0,0,0,100,100,0,0,1,2,1,2,40,40,80,1\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n"
	offset := 0
	for i, shot := range p.Shots {
		name := fmt.Sprintf("clip-%03d.mp4", i)
		filter := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=30", w, h, w, h)
		// 统一片段规格后拼接。已有音轨保留；无音轨时补静音，避免 concat 丢失音视频同步。
		input := s.Blobs.Path(p.TeamID, shot.OutputAssetID)
		length, err := exec.CommandContext(runCtx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", input).Output()
		var seconds float64
		if err != nil {
			return fmt.Errorf("invalid_video_result")
		}
		if _, err = fmt.Sscanf(string(length), "%f", &seconds); err != nil || seconds < float64(shot.Plan.Duration)-0.2 {
			return fmt.Errorf("video_result_too_short")
		}
		probe := exec.CommandContext(runCtx, "ffprobe", "-v", "error", "-select_streams", "a", "-show_entries", "stream=index", "-of", "csv=p=0", input)
		audio, _ := probe.Output()
		args := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", input}
		if strings.TrimSpace(string(audio)) == "" {
			args = append(args, "-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo", "-map", "0:v:0", "-map", "1:a:0")
		} else {
			args = append(args, "-map", "0:v:0", "-map", "0:a:0")
		}
		args = append(args, "-t", fmt.Sprint(shot.Plan.Duration), "-vf", filter, "-c:v", "libx264", "-threads", "2", "-preset", "fast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-ar", "48000", "-ac", "2", "-shortest", name)
		cmd := exec.CommandContext(runCtx, "ffmpeg", args...)
		cmd.Dir = dir
		if e = cmd.Run(); e != nil {
			return fmt.Errorf("clip_normalization_failed: %w", e)
		}
		list = append(list, "file '"+name+"'")
		if shot.Plan.Subtitle != "" {
			ass += fmt.Sprintf("Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n", subtitleTime(offset), subtitleTime(offset+shot.Plan.Duration), assText(shot.Plan.Subtitle))
		}
		offset += shot.Plan.Duration
	}
	if e = os.WriteFile(filepath.Join(dir, "clips.txt"), []byte(strings.Join(list, "\n")), 0600); e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(dir, "captions.ass"), []byte(ass), 0600); e != nil {
		return e
	}
	cmd := exec.CommandContext(runCtx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "concat", "-safe", "1", "-i", "clips.txt", "-vf", "subtitles=captions.ass", "-c:v", "libx264", "-threads", "2", "-preset", "fast", "-pix_fmt", "yuv420p", "-c:a", "copy", "-movflags", "+faststart", "final.mp4")
	cmd.Dir = dir
	if e = cmd.Run(); e != nil {
		return fmt.Errorf("composition_failed: %w", e)
	}
	f, e := os.Open(filepath.Join(dir, "final.mp4"))
	if e != nil {
		return e
	}
	defer f.Close()
	id, size, e := s.Blobs.Put(p.TeamID, f, MaxVideo)
	if e != nil {
		return e
	}
	a := Asset{ID: id, Name: p.Brief.Product + ".mp4", Kind: "video", Mime: "video/mp4", Size: size, CreatedAt: time.Now().UTC()}
	if e = s.Repo.AddAsset(ctx, p.TeamID, a); e != nil {
		return e
	}
	_, e = s.workerUpdate(ctx, p, func(v *Project) {
		// 合成期间取消由平台事实决定，不能让旧 worker 快照把 cancelled 覆盖为 completed。
		if v.CancelRequested {
			v.Status = "cancelled"
			return
		}
		v.OutputAssetID = id
		v.Status = "completed"
		v.Error = ""
	})
	return e
}
