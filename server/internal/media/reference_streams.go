package media

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const referenceStreamFormats = "mov,matroska,webm,wav,mp3,aac,flac,ogg"

func referenceStreamDependencies(ctx context.Context) error {
	if ctx.Err() != nil {
		return fmt.Errorf("source_import_cancelled")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return fmt.Errorf("source_media_dependencies_missing")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(probeCtx, "ffprobe", "-v", "quiet", "-version").Run(); err != nil {
		return fmt.Errorf("source_media_dependencies_missing")
	}
	raw, err := exec.CommandContext(probeCtx, "ffmpeg", "-hide_banner", "-encoders").Output()
	if err != nil || !strings.Contains(string(raw), "libx264") || !strings.Contains(string(raw), " aac ") {
		return fmt.Errorf("source_media_dependencies_missing")
	}
	return nil
}

func (s *Service) referenceImportStoragePreflight(team string) error {
	dir := filepath.Join(s.Blobs.Root, team)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("source_import_save_failed")
	}
	f, err := os.CreateTemp(dir, ".reference-preflight-*")
	if err != nil {
		return fmt.Errorf("source_import_save_failed")
	}
	closeErr := f.Close()
	removeErr := os.Remove(f.Name())
	if closeErr != nil || removeErr != nil {
		return fmt.Errorf("source_import_save_failed")
	}
	return nil
}

func validReferenceStreamURL(raw string, audio bool) bool {
	// 分轨容器可使用 m4s/m4a 扩展名，但 manifest、分片索引和网页始终不交给 FFmpeg。
	if len(raw) > 8192 || strings.ContainsAny(raw, "\\\r\n\t") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Port() != "" && u.Port() != "443" {
		return false
	}
	clone := *u
	clone.Path = "/media.mp4"
	if !validReferenceDownloadURL(clone.String()) {
		return false
	}
	switch strings.ToLower(filepath.Ext(u.Path)) {
	case ".m3u8", ".mpd", ".ts", ".jpg", ".jpeg", ".png", ".webp", ".html", ".htm", ".xml", ".json":
		return false
	case ".mp3", ".m4a", ".aac", ".wav", ".flac", ".ogg":
		return audio
	}
	return true
}

func referenceStreamError(ctx context.Context, code string) error {
	if ctx.Err() != nil {
		return fmt.Errorf("source_import_cancelled")
	}
	return fmt.Errorf("%s", code)
}

func downloadReferenceStream(ctx context.Context, baseClient *http.Client, rawURL string, headers http.Header, path string, remaining *int64) error {
	if ctx.Err() != nil {
		return fmt.Errorf("source_import_cancelled")
	}
	if *remaining <= 0 {
		return fmt.Errorf("source_video_too_large")
	}
	if !validReferenceStreamURL(rawURL, true) {
		return fmt.Errorf("source_video_unavailable")
	}
	if baseClient == nil {
		baseClient = NewReferenceHTTPClient(90 * time.Second)
	}
	client := *baseClient
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("source_video_unavailable")
	}
	req.Header = headers.Clone()
	req.Header.Del("Authorization")
	req.Header.Del("Cookie")
	resp, err := client.Do(req)
	if err != nil {
		return referenceStreamError(ctx, "source_video_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("source_video_unavailable")
	}
	if resp.ContentLength > *remaining {
		return fmt.Errorf("source_video_too_large")
	}
	reader := bufio.NewReader(resp.Body)
	head, _ := reader.Peek(512)
	mime := http.DetectContentType(head)
	if strings.HasPrefix(mime, "text/") || strings.HasPrefix(mime, "image/") || strings.Contains(mime, "xml") || strings.Contains(mime, "html") || bytesLookLikeManifest(head) {
		return fmt.Errorf("invalid_source_video")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("source_import_save_failed")
	}
	n, copyErr := io.Copy(f, io.LimitReader(reader, *remaining+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		return referenceStreamError(ctx, "source_video_unavailable")
	}
	if n == 0 || n > *remaining {
		return fmt.Errorf("source_video_too_large")
	}
	*remaining -= n
	return referenceStreamErrorOrNil(ctx)
}

func referenceStreamErrorOrNil(ctx context.Context) error {
	if ctx.Err() != nil {
		return fmt.Errorf("source_import_cancelled")
	}
	return nil
}

func bytesLookLikeManifest(head []byte) bool {
	text := strings.TrimSpace(string(head))
	return strings.HasPrefix(text, "#EXTM3U") || strings.HasPrefix(text, "<") || strings.HasPrefix(text, "ffconcat") || strings.HasPrefix(text, "{")
}

type referenceStreamInfo struct {
	Duration float64
	Codec    string
	Width    int
	Height   int
	Frames   int64
}

func probeReferenceStream(ctx context.Context, path, kind string) (referenceStreamInfo, error) {
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe", "-format_whitelist", referenceStreamFormats, "-show_entries", "format=duration:stream=codec_type,codec_name,width,height,duration", "-of", "json", path)
	output := boundedReferenceOutput{limit: maxReferenceProbeOutput}
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if err := cmd.Run(); err != nil || output.exceeded {
		return referenceStreamInfo{}, referenceStreamError(ctx, "source_video_probe_failed")
	}
	var result struct {
		Format  struct{ Duration string }
		Streams []struct {
			CodecType string `json:"codec_type"`
			Codec     string `json:"codec_name"`
			Width     int
			Height    int
			Duration  string
		}
	}
	if json.Unmarshal(output.Bytes(), &result) != nil {
		return referenceStreamInfo{}, fmt.Errorf("source_video_probe_failed")
	}
	for _, stream := range result.Streams {
		if stream.CodecType != kind {
			continue
		}
		duration, err := strconv.ParseFloat(stream.Duration, 64)
		if err != nil {
			duration, err = strconv.ParseFloat(result.Format.Duration, 64)
		}
		if err != nil || !finitePositive(duration) || duration > MaxReferenceSeconds || kind == "video" && (stream.Width <= 0 || stream.Height <= 0 || stream.Width > 16384 || stream.Height > 16384) {
			return referenceStreamInfo{}, fmt.Errorf("source_video_limits_exceeded")
		}
		// 容器元数据不等于有效媒体；完整解码选定流，拒绝损坏或只有索引的伪成片。
		decode := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-xerror", "-err_detect", "explode", "-threads", "2", "-protocol_whitelist", "file,pipe", "-format_whitelist", referenceStreamFormats, "-i", path, "-map", "0:"+kind[:1]+":0", "-progress", "pipe:1", "-nostats", "-f", "null", "-")
		progress := boundedReferenceOutput{limit: maxReferenceProbeOutput}
		decode.Stdout, decode.Stderr = &progress, io.Discard
		if err := decode.Run(); err != nil || progress.exceeded {
			return referenceStreamInfo{}, referenceStreamError(ctx, "invalid_source_video")
		}
		var frames, elapsed int64
		for _, line := range strings.Split(progress.String(), "\n") {
			key, value, _ := strings.Cut(line, "=")
			if key == "frame" {
				frames, _ = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			} else if key == "out_time_us" {
				elapsed, _ = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			}
		}
		if elapsed <= 0 || kind == "video" && frames <= 0 {
			return referenceStreamInfo{}, fmt.Errorf("invalid_source_video")
		}
		return referenceStreamInfo{Duration: duration, Codec: stream.Codec, Width: stream.Width, Height: stream.Height, Frames: frames}, nil
	}
	return referenceStreamInfo{}, fmt.Errorf("invalid_source_video")
}

func (s *Service) prepareReferenceStreams(ctx context.Context, team, source string, client *http.Client, media referenceDownload) (Asset, error) {
	if !validReferenceStreamURL(media.URL, false) || !validReferenceStreamURL(media.AudioURL, true) {
		return Asset{}, fmt.Errorf("source_video_unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	dir, err := os.MkdirTemp(filepath.Join(s.Blobs.Root, team), ".reference-streams-*")
	if err != nil {
		return Asset{}, fmt.Errorf("source_import_save_failed")
	}
	defer os.RemoveAll(dir)
	videoPath, audioPath, outputPath := filepath.Join(dir, "picture.bin"), filepath.Join(dir, "sound.bin"), filepath.Join(dir, "merged.mp4")
	remaining := int64(MaxReferenceBytes)
	for _, item := range []struct{ url, path string }{{media.URL, videoPath}, {media.AudioURL, audioPath}} {
		if err := downloadReferenceStream(ctx, client, item.url, media.Headers, item.path, &remaining); err != nil {
			return Asset{}, err
		}
	}
	video, err := probeReferenceStream(ctx, videoPath, "video")
	if err != nil {
		return Asset{}, err
	}
	audio, err := probeReferenceStream(ctx, audioPath, "audio")
	if err != nil {
		return Asset{}, err
	}
	tolerance := math.Max(.25, math.Min(.5, video.Duration*.01))
	if math.Abs(video.Duration-audio.Duration) > tolerance {
		return Asset{}, fmt.Errorf("source_stream_duration_mismatch")
	}
	args := []string{"-nostdin", "-v", "error", "-xerror", "-threads", "2"}
	for _, path := range []string{videoPath, audioPath} {
		args = append(args, "-protocol_whitelist", "file,pipe", "-format_whitelist", referenceStreamFormats, "-i", path)
	}
	args = append(args, "-map", "0:v:0", "-map", "1:a:0", "-map_metadata", "-1", "-map_chapters", "-1")
	if video.Codec == "h264" {
		args = append(args, "-c:v", "copy")
	} else {
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-pix_fmt", "yuv420p", "-threads", "2", "-fps_mode", "passthrough")
	}
	if audio.Codec == "aac" {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, "-c:a", "aac", "-b:a", "192k")
	}
	args = append(args, "-fs", strconv.FormatInt(MaxReferenceBytes+1, 10), "-movflags", "+faststart", "-f", "mp4", outputPath)
	merge := exec.CommandContext(ctx, "ffmpeg", args...)
	merge.Stdout, merge.Stderr = io.Discard, io.Discard
	if err := merge.Run(); err != nil {
		return Asset{}, referenceStreamError(ctx, "source_stream_merge_failed")
	}
	info, err := os.Stat(outputPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return Asset{}, fmt.Errorf("source_stream_merge_failed")
	}
	if info.Size() > MaxReferenceBytes {
		return Asset{}, fmt.Errorf("source_video_too_large")
	}
	mergedVideo, err := probeReferenceStream(ctx, outputPath, "video")
	if err != nil {
		return Asset{}, err
	}
	mergedAudio, err := probeReferenceStream(ctx, outputPath, "audio")
	if err != nil {
		return Asset{}, err
	}
	if mergedVideo.Frames != video.Frames || math.Abs(mergedVideo.Duration-video.Duration) > tolerance || math.Abs(mergedAudio.Duration-audio.Duration) > tolerance {
		return Asset{}, fmt.Errorf("source_stream_duration_mismatch")
	}
	file, err := os.Open(outputPath)
	if err != nil {
		return Asset{}, fmt.Errorf("source_import_save_failed")
	}
	defer file.Close()
	asset, err := s.prepareReference(ctx, team, "原片.mp4", file, source)
	if err == nil && ctx.Err() != nil {
		_ = os.Remove(s.Blobs.Path(team, asset.ID))
		return Asset{}, fmt.Errorf("source_import_cancelled")
	}
	return asset, err
}
