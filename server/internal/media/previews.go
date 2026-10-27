package media

import (
	"context"
	"fmt"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const previewVersion = "jpeg-640-v1"

type previewResult struct {
	done chan struct{}
	err  error
}

// PreviewCache 仅拥有可重建的派生文件，不拥有资产、任务或费用事实。
type PreviewCache struct {
	mu       sync.Mutex
	inflight map[string]*previewResult
	slots    chan struct{}
	render   func(context.Context, string, string) error
}

func NewPreviewCache() *PreviewCache {
	return &PreviewCache{inflight: make(map[string]*previewResult), slots: make(chan struct{}, 2), render: renderPreview}
}

func (b Blobs) PreviewURL(team, id string) string {
	return strings.Replace(b.URL(team, id), "/api/v1/media/blobs/", "/api/v1/media/previews/", 1)
}

func (b Blobs) WithPreviewURL(team string, asset Asset) Asset {
	asset.ThumbnailURL, asset.PosterURL = "", ""
	if !asset.Internal {
		if asset.Kind == "image" {
			asset.ThumbnailURL = b.PreviewURL(team, asset.ID)
		}
		if asset.Kind == "video" {
			asset.PosterURL = b.PreviewURL(team, asset.ID)
		}
	}
	return asset
}

func (p *PreviewCache) Get(ctx context.Context, blobs Blobs, team string, asset Asset) (string, error) {
	if _, err := uuid.Parse(team); err != nil {
		return "", ErrInvalid
	}
	if _, err := uuid.Parse(asset.ID); err != nil {
		return "", ErrInvalid
	}
	if asset.Internal || (asset.Kind != "image" && asset.Kind != "video") {
		return "", ErrInvalid
	}
	source := blobs.Path(team, asset.ID)
	stat, err := os.Lstat(source)
	if err != nil {
		return "", err
	}
	if !stat.Mode().IsRegular() {
		return "", ErrInvalid
	}
	path := filepath.Join(blobs.Root, ".previews", previewVersion, team, asset.ID+".jpg")
	if validPreview(path) {
		return path, nil
	}
	p.mu.Lock()
	job := p.inflight[path]
	if job == nil {
		if len(p.inflight) >= 64 {
			p.mu.Unlock()
			return "", fmt.Errorf("media_preview_busy")
		}
		job = &previewResult{done: make(chan struct{})}
		p.inflight[path] = job
		// 独立等待者取消不能终止其他请求复用的编码；共享作业仍受30秒deadline限制。
		go p.generate(source, path, job)
	}
	p.mu.Unlock()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-job.done:
		return path, job.err
	}
}

func validPreview(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 4<<20 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	config, err := jpeg.DecodeConfig(f)
	return err == nil && config.Width > 0 && config.Height > 0 && config.Width <= 640 && config.Height <= 640
}

func (p *PreviewCache) generate(source, path string, job *previewResult) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	defer func() {
		p.mu.Lock()
		delete(p.inflight, path)
		close(job.done)
		p.mu.Unlock()
	}()
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	case <-ctx.Done():
		job.err = ctx.Err()
		return
	}
	if validPreview(path) {
		return
	}
	job.err = p.write(ctx, source, path)
}

func (p *PreviewCache) write(ctx context.Context, source, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".preview-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if err = f.Close(); err != nil {
		return err
	}
	if err = p.render(ctx, source, temporary); err != nil {
		return fmt.Errorf("media_preview_failed: %w", err)
	}
	if !validPreview(temporary) {
		return fmt.Errorf("media_preview_invalid")
	}
	// 原件是不可变blob；同目录原子发布避免请求读到FFmpeg尚未写完的JPEG。
	return os.Rename(temporary, path)
}

func renderPreview(ctx context.Context, source, output string) error {
	command := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-protocol_whitelist", "file,pipe", "-i", source, "-map", "0:v:0", "-frames:v", "1", "-an", "-map_metadata", "-1",
		"-vf", "scale=w='min(640,iw)':h='min(640,ih)':force_original_aspect_ratio=decrease", "-threads", "1", "-c:v", "mjpeg", "-q:v", "5", "-f", "image2", output)
	// 不公开FFmpeg stderr：其中可能含磁盘路径或源素材元数据。
	return command.Run()
}
