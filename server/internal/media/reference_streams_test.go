package media

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

// 单流 HTTP/账务单测注入媒体依赖；真实分轨集成测试保留生产预检，不使用此替身。
func referenceFixturePreflight(context.Context) error { return nil }

func referenceStreamFixture(t *testing.T, duration string, audio bool) []byte {
	t.Helper()
	if !CompositionAvailable() {
		t.Skip("ffmpeg unavailable")
	}
	name := filepath.Join(t.TempDir(), "stream.mp4")
	args := []string{"-nostdin", "-v", "error", "-f", "lavfi"}
	if audio {
		args = append(args, "-i", "sine=frequency=523:sample_rate=48000", "-t", duration, "-vn", "-c:a", "aac")
	} else {
		args = append(args, "-i", "testsrc2=size=160x90:rate=25", "-t", duration, "-an", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p")
	}
	if raw, err := exec.Command("ffmpeg", append(args, "-movflags", "+faststart", name)...).CombinedOutput(); err != nil {
		t.Fatalf("stream fixture: %v %s", err, raw)
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func referenceSplitParser(f *fixture, video, audio []byte, parsed *atomic.Int64, downloaded *atomic.Int64, mutate func(*http.Request) *http.Response) *SourceParser {
	return &SourceParser{Key: "fixture-parser-key", Client: &http.Client{Transport: referenceTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == SnapAnyExtractURL {
			parsed.Add(1)
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"medias":[{"media_type":"video","resource_url":"https://cdn.example.test/video.m4s","headers":{"Authorization":"forbidden","Cookie":"forbidden","Referer":"https://www.bilibili.com/","User-Agent":"fixture-agent"},"variants":[{"video_url":"https://cdn.example.test/video.m4s","audio_url":"https://cdn.example.test/audio.m4s","video_ext":"mp4","quality":80}]}]}`))}, nil
		}
		downloaded.Add(1)
		if mutate != nil {
			if response := mutate(r); response != nil {
				return response, nil
			}
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			return nil, netguardTestError("credential forwarded to stream")
		}
		raw := video
		if strings.HasSuffix(r.URL.Path, "audio.m4s") {
			raw = audio
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw)), ContentLength: int64(len(raw))}, nil
	})}}
}

type netguardTestError string

func (e netguardTestError) Error() string { return string(e) }

func TestReferenceSplitStreamsImportRealAudioAndVideoAndReplayOnce(t *testing.T) {
	video, audio := referenceStreamFixture(t, "1", false), referenceStreamFixture(t, "1", true)
	f := setup(t)
	var parsed, downloaded atomic.Int64
	f.s.SourceParser = referenceSplitParser(f, video, audio, &parsed, &downloaded, nil)
	jar, _ := cookiejar.New(nil)
	cdnURL, _ := url.Parse("https://cdn.example.test")
	jar.SetCookies(cdnURL, []*http.Cookie{{Name: "private-session", Value: "fixture-secret"}})
	f.s.SourceParser.Client.Jar = jar
	id := uuid.NewString()
	asset, err := f.s.ImportReference(context.Background(), f.team, f.user, id, "https://www.bilibili.com/video/BVfixture")
	if err != nil || asset.Mime != "video/mp4" || asset.Width != 160 || asset.Height != 90 || asset.DurationSeconds < .99 || asset.DurationSeconds > 1.1 {
		t.Fatalf("split import=%+v err=%v", asset, err)
	}
	raw, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type", "-of", "json", f.s.Blobs.Path(f.team, asset.ID)).Output()
	if err != nil || !bytes.Contains(raw, []byte(`"video"`)) || !bytes.Contains(raw, []byte(`"audio"`)) {
		t.Fatalf("missing stream: %s %v", raw, err)
	}
	if raw, err = exec.Command("ffmpeg", "-nostdin", "-v", "error", "-xerror", "-i", f.s.Blobs.Path(f.team, asset.ID), "-f", "null", "-").CombinedOutput(); err != nil {
		t.Fatalf("incomplete decode: %s %v", raw, err)
	}
	again, err := f.s.ImportReference(context.Background(), f.team, f.user, id, asset.SourceURL)
	if err != nil || again.ID != asset.ID || parsed.Load() != 1 || downloaded.Load() != 2 {
		t.Fatalf("replay=%+v err=%v parsed=%d downloaded=%d", again, err, parsed.Load(), downloaded.Load())
	}
	referenceBillingState(t, f, "0.500000", 1)
	assertReferenceStreamsClean(t, f)
}

func assertReferenceStreamsClean(t *testing.T, f *fixture) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(f.s.Blobs.Root, f.team, ".reference-streams-*"))
	if len(files) != 0 {
		t.Fatalf("temporary streams leaked: %v", files)
	}
}

func TestReferenceSplitStreamsRejectBadAudioMismatchRedirectAndCancellationWithoutReparse(t *testing.T) {
	video, audio := referenceStreamFixture(t, "1", false), referenceStreamFixture(t, "1", true)
	longAudio := referenceStreamFixture(t, "2", true)
	for _, kind := range []string{"invalid-audio", "mismatched-duration", "redirect", "too-large", "cancelled", "remote-manifest", "truncated-video", "truncated-audio"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			var parsed, downloaded atomic.Int64
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			track := audio
			picture := video
			if kind == "invalid-audio" {
				track = []byte("not audio")
			} else if kind == "mismatched-duration" {
				track = longAudio
			} else if kind == "remote-manifest" {
				track = []byte("#EXTM3U\n#EXTINF:1,\nhttps://127.0.0.1/private\n")
			} else if kind == "truncated-audio" {
				track = audio[:len(audio)/2]
			} else if kind == "truncated-video" {
				picture = video[:len(video)/2]
			}
			f.s.SourceParser = referenceSplitParser(f, picture, track, &parsed, &downloaded, func(r *http.Request) *http.Response {
				if strings.HasSuffix(r.URL.Path, "audio.m4s") {
					switch kind {
					case "redirect":
						return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://other.example.test/audio.m4s"}}, Body: io.NopCloser(strings.NewReader(""))}
					case "too-large":
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(audio)), ContentLength: MaxReferenceBytes}
					case "cancelled":
						cancel()
					}
				}
				return nil
			})
			id := uuid.NewString()
			if asset, err := f.s.ImportReference(ctx, f.team, f.user, id, "https://b23.tv/fixture"); err == nil || asset.ID != "" {
				t.Fatalf("unsafe %s import=%+v %v", kind, asset, err)
			}
			if _, err := f.s.ImportReference(context.Background(), f.team, f.user, id, "https://b23.tv/fixture"); err == nil || parsed.Load() != 1 || downloaded.Load() > 2 {
				t.Fatalf("failure repeated paid parse=%d download=%d err=%v", parsed.Load(), downloaded.Load(), err)
			}
			referenceBillingState(t, f, "0.000000", 0)
			assertReferenceStreamsClean(t, f)
		})
	}
}

func TestReferenceSplitStreamsRejectUnsafePairWithoutSilentResourceFallback(t *testing.T) {
	for i, audioURL := range []string{"http://cdn.example.test/audio.m4a", "https://127.0.0.1/audio.m4a", "https://169.254.169.254/audio.m4a", "https://localhost/audio.m4a", "https://cdn.example.test/audio.mpd", "https://user:secret@cdn.example.test/audio.m4a"} {
		p := &SourceParser{Key: "fixture", Client: &http.Client{Transport: referenceTransport(func(*http.Request) (*http.Response, error) {
			resourceURL := "https://cdn.example.test/video.mp4"
			if i%2 == 0 {
				resourceURL = "https://alias.example.test/the-same-silent-video.mp4"
			}
			raw, _ := json.Marshal(map[string]any{"medias": []any{map[string]any{"media_type": "video", "resource_url": resourceURL, "variants": []any{map[string]any{"video_url": "https://cdn.example.test/video.mp4", "audio_url": audioURL}}}}})
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw))}, nil
		})}}
		if _, err := p.Extract(context.Background(), "https://www.bilibili.com/video/fixture"); err == nil {
			t.Fatalf("unsafe audio pair fell back to silent video: %s", audioURL)
		}
	}
}

func TestReferenceImportMissingMediaDependenciesFailsBeforePaidParser(t *testing.T) {
	f := setup(t)
	var parsed atomic.Int64
	f.s.SourceParser = &SourceParser{Key: "fixture", Client: &http.Client{Transport: referenceTransport(func(*http.Request) (*http.Response, error) {
		parsed.Add(1)
		return &http.Response{StatusCode: 500, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})}}
	t.Setenv("PATH", t.TempDir())
	if _, err := f.s.ImportReference(context.Background(), f.team, f.user, uuid.NewString(), "https://b23.tv/fixture"); err == nil || err.Error() != "source_media_dependencies_missing" || parsed.Load() != 0 {
		t.Fatalf("missing dependency paid parser=%d err=%v", parsed.Load(), err)
	}
	referenceBillingState(t, f, "0.000000", 0)
}

func TestReferenceImportStorageFailureFailsBeforePaidParser(t *testing.T) {
	f, parsed, _ := referenceBillingFixture(t)
	f.s.Blobs.Root = filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(f.s.Blobs.Root, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ImportReference(context.Background(), f.team, f.user, uuid.NewString(), "https://b23.tv/fixture"); err == nil || err.Error() != "source_import_save_failed" || parsed.Load() != 0 {
		t.Fatalf("storage failure paid parser=%d err=%v", parsed.Load(), err)
	}
	referenceBillingState(t, f, "0.000000", 0)
}

func TestReferenceSplitStreamsBoundedChunkedDownloadAndPublicNetworkBoundary(t *testing.T) {
	for _, kind := range []string{"chunked-overflow", "cloud-metadata"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: referenceTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: -1, Body: io.NopCloser(bytes.NewReader(bytes.Repeat([]byte{0, 1, 2, 3}, 32)))}, nil
			})}
			rawURL := "https://cdn.example.test/video.m4s"
			if kind == "cloud-metadata" {
				rawURL = "https://169.254.169.254/video.m4s"
			}
			remaining := int64(64)
			if err := downloadReferenceStream(context.Background(), client, rawURL, nil, filepath.Join(t.TempDir(), "stream.bin"), &remaining); err == nil {
				t.Fatalf("unsafe download kind=%s", kind)
			}
			if kind == "cloud-metadata" && calls != 0 {
				t.Fatalf("private target reached transport: %d", calls)
			}
		})
	}
}

func TestResearchPlatformsCapabilityMatchesBriefAdmission(t *testing.T) {
	f := setup(t)
	p := f.project(t)
	models, err := f.s.Repo.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assets, err := f.s.Repo.Assets(context.Background(), f.team)
	if err != nil {
		t.Fatal(err)
	}
	for _, platforms := range [][]string{{"douyin"}, {"xiaohongshu"}, {"bilibili"}, {"douyin", "xiaohongshu", "bilibili"}} {
		brief := p.Brief
		brief.Platforms = platforms
		if err := ValidateBrief(brief, models, assets); err != nil {
			t.Fatalf("research platform rejected %v: %v", platforms, err)
		}
	}
	for _, platforms := range [][]string{nil, {"youtube"}, {"douyin", "douyin"}, {"douyin", "xiaohongshu", "bilibili", "douyin"}} {
		brief := p.Brief
		brief.Platforms = platforms
		if ValidateBrief(brief, models, assets) == nil {
			t.Fatalf("invalid research platforms accepted %v", platforms)
		}
	}
	capabilities := f.s.FinishingCapabilities(context.Background())
	if !reflect.DeepEqual(capabilities["researchPlatforms"], []string{"douyin", "xiaohongshu", "bilibili"}) || capabilities["creativeBrief"] != true {
		t.Fatalf("capabilities=%+v", capabilities)
	}
}
