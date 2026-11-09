package media

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestArkSeedance25TaskParametersAcrossEntryPoints(t *testing.T) {
	for _, test := range []struct {
		name, entry, video, wantRatio string
		images                        []string
		wantReference                 bool
	}{
		{name: "text", entry: "canvas", wantRatio: "9:16"},
		{name: "first-frame", entry: "canvas", images: []string{"https://example.test/first.png"}, wantRatio: "adaptive"},
		{name: "production-frames", entry: "production", images: []string{"https://example.test/first.png", "https://example.test/last.png"}, wantRatio: "adaptive"},
		{name: "production-reference", entry: "production", images: []string{"https://example.test/product.png"}, video: "https://example.test/source.mp4", wantRatio: "9:16", wantReference: true},
		{name: "canvas-reference", entry: "canvas", images: []string{"https://example.test/product.png"}, video: "https://example.test/source.mp4", wantRatio: "9:16", wantReference: true},
		{name: "multimodal-images", entry: "multimodal", images: []string{"https://example.test/product.png"}, wantRatio: "9:16", wantReference: true},
		{name: "multimodal-video", entry: "multimodal", video: "https://example.test/source.mp4", wantRatio: "9:16", wantReference: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/contents/generations/tasks" {
					t.Errorf("unexpected endpoint: %s %s", r.Method, r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"id": "task_fixture"})
			}))
			defer server.Close()
			provider := Provider{Adapter: "ark", BaseURL: server.URL, Client: server.Client()}
			model := Model{Kind: "video", Adapter: "ark", UpstreamModel: "doubao-seedance-2-5-260628", GenerateAudio: true, VideoReference: &VideoReferenceCapability{MaxDurationSeconds: 30, MaxImages: 8, TokenPriceMicroPerMillion: 1}}
			request := CanvasRequest{Kind: "video", Prompt: "参考素材生成新视频", Resolution: "720p", AspectRatio: "9:16", Duration: 30}
			var id string
			var err error
			switch test.entry {
			case "production":
				id, err = provider.SubmitWithReferenceImages(context.Background(), model, Brief{Resolution: request.Resolution, AspectRatio: request.AspectRatio}, ShotPlan{Prompt: request.Prompt, Duration: request.Duration}, test.images, test.video)
			case "multimodal":
				request.ReferenceMode = "multimodal"
				var videos []string
				if test.video != "" {
					videos = []string{test.video}
				}
				id, err = provider.CanvasMultimodalVideo(context.Background(), model, request, test.images, videos, nil)
			default:
				id, err = provider.CanvasVideo(context.Background(), model, request, test.images, test.video)
			}
			if err != nil || id != "task_fixture" {
				t.Fatalf("id=%s err=%v", id, err)
			}
			if body["model"] != model.UpstreamModel || body["duration"] != float64(30) || body["ratio"] != test.wantRatio {
				t.Fatalf("parameters: %#v", body)
			}
			value, exists := body["omni_reference_task_type"]
			if exists != test.wantReference || test.wantReference && value != "reference" {
				t.Fatalf("task type=%v exists=%v", value, exists)
			}
		})
	}
}

func TestArkSeedance25TaskOptionsKeepOtherModelsUnchanged(t *testing.T) {
	for _, model := range []Model{
		{Adapter: "ark", UpstreamModel: "doubao-seedance-2-0-260128"},
		{Adapter: "toapis", UpstreamModel: "seedance-2-5"},
	} {
		body := map[string]any{"ratio": "9:16"}
		applyArkSeedance25TaskOptions(body, model, []string{"https://example.test/first.png"}, true)
		if len(body) != 1 || body["ratio"] != "9:16" {
			t.Fatalf("unexpected options for %s: %#v", model.UpstreamModel, body)
		}
	}
}
