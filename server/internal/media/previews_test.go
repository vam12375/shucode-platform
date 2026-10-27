package media

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

func previewJPEG(t *testing.T, path string, width, height int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}

func previewFixture(t *testing.T) (Blobs, string, Asset) {
	t.Helper()
	b := Blobs{Root: t.TempDir(), Origin: "https://api.example.test", SigningKey: "fixture"}
	team := uuid.NewString()
	id, size, err := b.Put(team, bytes.NewBufferString("original unchanged"), MaxUpload)
	if err != nil {
		t.Fatal(err)
	}
	return b, team, Asset{ID: id, Kind: "image", Size: size}
}

func TestPreviewCacheDeduplicatesPersistsAndDoesNotModifySource(t *testing.T) {
	b, team, asset := previewFixture(t)
	cache := NewPreviewCache()
	var calls atomic.Int32
	cache.render = func(ctx context.Context, source, output string) error {
		calls.Add(1)
		previewJPEG(t, output, 640, 320)
		return nil
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := cache.Get(context.Background(), b, team, asset); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("encoded %d times", calls.Load())
	}
	restarted := NewPreviewCache()
	restarted.render = func(context.Context, string, string) error {
		t.Error("persistent hit re-encoded")
		return errors.New("unexpected")
	}
	if _, err := restarted.Get(context.Background(), b, team, asset); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(b.Path(team, asset.ID))
	if string(raw) != "original unchanged" {
		t.Fatal("source was changed")
	}
	if err := os.Remove(b.Path(team, asset.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(context.Background(), b, team, asset); err == nil {
		t.Fatal("deleted source served from preview cache")
	}
}

func TestPreviewFailureAndCancellationDoNotPublishPartialFiles(t *testing.T) {
	b, team, asset := previewFixture(t)
	cache := NewPreviewCache()
	cache.render = func(ctx context.Context, source, output string) error {
		return os.WriteFile(output, []byte("partial not jpeg"), 0600)
	}
	if _, err := cache.Get(context.Background(), b, team, asset); err == nil {
		t.Fatal("invalid image published")
	}
	started, finish := make(chan struct{}), make(chan struct{})
	cache.render = func(ctx context.Context, source, output string) error {
		close(started)
		<-finish
		previewJPEG(t, output, 20, 10)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := cache.Get(ctx, b, team, asset); result <- err }()
	<-started
	cancel()
	if !errors.Is(<-result, context.Canceled) {
		t.Fatal("canceled waiter not released")
	}
	close(finish)
	if _, err := cache.Get(context.Background(), b, team, asset); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(b.Root, ".previews", "jpeg-640-v1", team, ".preview-*"))
	if len(files) != 0 {
		t.Fatal("temporary preview files leaked")
	}
}

func TestPreviewRejectsPathsAudioAndSymlinks(t *testing.T) {
	b, team, asset := previewFixture(t)
	cache := NewPreviewCache()
	for _, invalid := range []Asset{{ID: "../anything", Kind: "image"}, {ID: asset.ID, Kind: "audio"}, {ID: asset.ID, Kind: "image", Internal: true}} {
		if _, err := cache.Get(context.Background(), b, team, invalid); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
	if err := os.Remove(b.Path(team, asset.ID)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), b.Path(team, asset.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(context.Background(), b, team, asset); err == nil {
		t.Fatal("source symlink accepted")
	}
}

func TestPreviewRealFFmpegImageAndVideo(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	for _, size := range []image.Point{{X: 1200, Y: 600}, {X: 40, Y: 20}} {
		b, team, asset := previewFixture(t)
		var raw bytes.Buffer
		if err := png.Encode(&raw, image.NewRGBA(image.Rect(0, 0, size.X, size.Y))); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(b.Path(team, asset.ID), raw.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		path, err := NewPreviewCache().Get(context.Background(), b, team, asset)
		if err != nil {
			t.Fatal(err)
		}
		f, _ := os.Open(path)
		config, err := jpeg.DecodeConfig(f)
		f.Close()
		if err != nil || config.Width != min(size.X, 640) || config.Height != min(size.Y, 320) {
			t.Fatalf("preview=%+v error=%v", config, err)
		}
	}
	b, team, asset := previewFixture(t)
	asset.Kind = "video"
	raw, err := os.ReadFile("testdata/blue.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(b.Path(team, asset.ID), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = NewPreviewCache().Get(context.Background(), b, team, asset); err != nil {
		t.Fatal(err)
	}
}
