package media

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func wanFixtureModel(upstream string) Model {
	m := toapisModel()
	m.UpstreamModel = upstream
	m.MinDuration, m.MaxDuration = 2, 30
	m.Resolutions = []string{"480p", "720p", "1080p"}
	m.GenerateAudio = true
	m.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 10, MaxAudioSeconds: 15, CostMicro: 100_000, PriceMicro: 200_000}
	return m
}

func TestWanCatalogAndLimits(t *testing.T) {
	for _, upstream := range []string{"wan3.0-video", "wan3.0-video-prime"} {
		m := wanFixtureModel(upstream)
		if err := ValidateModels([]Model{m}); err != nil {
			t.Fatalf("%s catalog: %v", upstream, err)
		}
		if !slices.Equal(ModelAspectRatios(m), []string{"auto", "16:9", "9:16", "1:1", "4:3", "3:4"}) {
			t.Fatal("unsupported aspect ratios advertised")
		}
		c := CanvasCapabilitiesFor(m)
		if c.MaxImages != 10 || c.MaxVideos != 5 || c.MaxAudios != 5 || c.MaxTotalReferences != 20 || c.MaxVideoSeconds != 15 || c.MaxAudioSeconds != 15 || !c.GenerateAudio || c.ReturnLastFrame {
			t.Fatalf("wrong capabilities: %+v", c)
		}
		m.VideoReference = nil
		if CanvasCapabilitiesFor(m).MaxVideos != 0 {
			t.Fatal("unpriced video reference advertised")
		}
	}
	for name, change := range map[string]func(*Model){
		"unknown":          func(m *Model) { m.UpstreamModel = "wan3.0" },
		"resolution":       func(m *Model) { m.Resolutions = []string{"4k"} },
		"duration":         func(m *Model) { m.MinDuration = 1 },
		"long-reference":   func(m *Model) { m.VideoReference.MaxDurationSeconds = 16 },
		"token-billing":    func(m *Model) { m.BillingUnit = BillingUnitToken; m.TokenPriceMicroPerMillion = 200_000 },
		"request-billing":  func(m *Model) { m.BillingUnit = BillingUnitRequest },
		"reference-images": func(m *Model) { m.VideoReference.MaxImages = 11 },
	} {
		t.Run(name, func(t *testing.T) {
			m := wanFixtureModel("wan3.0-video")
			change(&m)
			if ValidateModels([]Model{m}) == nil {
				t.Fatal("invalid Wan configuration accepted")
			}
		})
	}
}

func TestWanDocumentedWireParameters(t *testing.T) {
	for _, upstream := range []string{"wan3.0-video", "wan3.0-video-prime"} {
		for _, mode := range []string{"text", "frames", "reference-video", "multimodal"} {
			t.Run(upstream+"/"+mode, func(t *testing.T) {
				var body map[string]any
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "POST" || r.URL.Path != "/v1/videos/generations" {
						t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					_, _ = w.Write([]byte(`{"id":"tsk_vid_wan_fixture","status":"queued"}`))
				}))
				defer server.Close()
				m := wanFixtureModel(upstream)
				p := Provider{Adapter: "toapis", BaseURL: server.URL, Client: server.Client()}
				off := false
				r := CanvasRequest{Kind: "video", Prompt: "fixture", Resolution: "720p", AspectRatio: "auto", Duration: 30, GenerateAudio: &off}
				var images []string
				video := ""
				if mode == "frames" || mode == "multimodal" {
					images = []string{"https://media.example.test/first.png", "https://media.example.test/last.png"}
				}
				if mode == "reference-video" || mode == "multimodal" {
					video = "https://media.example.test/clip.mp4"
				}
				var id string
				var err error
				if mode == "multimodal" {
					r.ReferenceMode = "multimodal"
					id, err = p.CanvasMultimodalVideo(context.Background(), m, r, images, []string{video}, []string{"https://media.example.test/audio.mp3"})
				} else {
					id, err = p.CanvasVideo(context.Background(), m, r, images, video)
				}
				if err != nil || id != "tsk_vid_wan_fixture" {
					t.Fatalf("submit %s: %v", id, err)
				}
				if body["model"] != upstream || body["ratio"] != "adaptive" || body["audio"] != false || body["watermark"] != false || body["prompt_extend"] != false || body["duration"] != float64(30) {
					t.Fatalf("incorrect Wan payload: %+v", body)
				}
				for _, field := range []string{"generate_audio", "aspect_ratio", "video_with_roles"} {
					if _, exists := body[field]; exists {
						t.Fatalf("unsupported field %s", field)
					}
				}
				if mode == "frames" {
					frames := body["image_with_roles"].([]any)
					if len(frames) != 2 || frames[0].(map[string]any)["role"] != "first_frame" || frames[1].(map[string]any)["role"] != "last_frame" {
						t.Fatal("incorrect frame roles")
					}
				} else if _, exists := body["image_with_roles"]; exists {
					t.Fatal("references mixed with frames")
				}
				if video != "" {
					videos := body["video_list"].([]any)
					if len(videos) != 1 || videos[0].(map[string]any)["video_url"] != video {
						t.Fatal("reference video lost")
					}
				}
				if mode == "multimodal" {
					if len(body["reference_images"].([]any)) != 2 || len(body["audio_with_roles"].([]any)) != 1 {
						t.Fatal("multimodal references lost")
					}
				}
			})
		}
	}
}

func TestWanRejectsUnsupportedInputBeforeSubmission(t *testing.T) {
	m := wanFixtureModel("wan3.0-video")
	p := Provider{Adapter: "toapis", BaseURL: "http://127.0.0.1:1"}
	base := CanvasRequest{Kind: "video", Prompt: "fixture", Resolution: "720p", AspectRatio: "16:9", Duration: 5}
	for name, change := range map[string]func(*CanvasRequest){
		"short":      func(r *CanvasRequest) { r.Duration = 1 },
		"long":       func(r *CanvasRequest) { r.Duration = 31 },
		"automatic":  func(r *CanvasRequest) { r.Duration = -1 },
		"ratio":      func(r *CanvasRequest) { r.AspectRatio = "21:9" },
		"resolution": func(r *CanvasRequest) { r.Resolution = "4k" },
	} {
		t.Run(name, func(t *testing.T) {
			r := base
			change(&r)
			if _, err := p.CanvasVideo(context.Background(), m, r, nil); err != ErrInvalid {
				t.Fatalf("expected input rejection: %v", err)
			}
			r.ReferenceMode = "multimodal"
			if _, err := p.CanvasMultimodalVideo(context.Background(), m, r, nil, nil, nil); err != ErrInvalid {
				t.Fatalf("expected multimodal rejection: %v", err)
			}
		})
	}
	images := []string{"https://media.example.test/a.png", "https://media.example.test/b.png", "https://media.example.test/c.png"}
	if _, err := p.CanvasVideo(context.Background(), m, base, images); err != ErrInvalid {
		t.Fatalf("more than two frames accepted: %v", err)
	}
	base.ReferenceMode = "frames"
	if _, err := p.CanvasVideo(context.Background(), m, base, images[:1], "https://media.example.test/a.mp4"); err != ErrInvalid {
		t.Fatalf("mixed frame/reference mode accepted: %v", err)
	}
}
