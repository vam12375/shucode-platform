package media

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestImageWithoutReferenceOmitsImage(t *testing.T) {
	for _, input := range []string{"", "https://example.test/reference.png"} {
		t.Run(input, func(t *testing.T) {
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"url": "https://example.test/frame.png"}}})
			}))
			defer server.Close()
			provider := Provider{BaseURL: server.URL, Client: server.Client()}
			if _, err := provider.Image(context.Background(), Model{UpstreamModel: "image-model"}, "人物首帧", input); err != nil {
				t.Fatal(err)
			}
			value, exists := body["image"]
			if input == "" && exists {
				t.Fatalf("text-to-image sent an empty reference: %#v", body)
			}
			if input != "" && value != input {
				t.Fatalf("reference = %v", value)
			}
		})
	}
}

func TestVideoSubmitRequiresFirstFrameBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		json.NewEncoder(w).Encode(map[string]string{"id": "unexpected"})
	}))
	defer server.Close()
	p := Provider{BaseURL: server.URL, Client: server.Client()}
	if _, err := p.Submit(context.Background(), Model{}, Brief{}, ShotPlan{}, ""); err == nil || requests != 0 {
		t.Fatalf("err=%v requests=%d", err, requests)
	}
}

func TestProviderSubmitsMultipleReferenceImagesAndOneVideo(t *testing.T) {
	var body map[string]any
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		json.NewEncoder(w).Encode(map[string]string{"id": "task-reference"})
	}))
	defer server.Close()
	p := Provider{BaseURL: server.URL, Client: server.Client()}
	m := Model{UpstreamModel: "video-model", VideoReference: &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 3}}
	images := []string{"https://example.test/first.png", "https://example.test/second.png", "https://example.test/third.png"}
	id, err := p.SubmitWithReferenceImages(context.Background(), m, Brief{Resolution: "480p", AspectRatio: "16:9"}, ShotPlan{Prompt: "整片保留动作并替换角色", Duration: 9}, images, "https://example.test/full.mp4")
	if err != nil || id != "task-reference" || requests != 1 {
		t.Fatalf("id=%s requests=%d err=%v", id, requests, err)
	}
	content := body["content"].([]any)
	if len(content) != 5 || body["duration"] != float64(9) {
		t.Fatalf("unexpected whole-reference request: %#v", body)
	}
	for i, want := range images {
		item := content[i+1].(map[string]any)
		if item["role"] != "reference_image" || item["image_url"].(map[string]any)["url"] != want {
			t.Fatalf("reference image %d = %#v", i, item)
		}
	}
	video := content[4].(map[string]any)
	if video["role"] != "reference_video" || video["video_url"].(map[string]any)["url"] != "https://example.test/full.mp4" {
		t.Fatalf("reference video = %#v", video)
	}
}

func TestProviderRejectsReferenceImagesOutsideCapabilityBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		json.NewEncoder(w).Encode(map[string]string{"id": "unexpected"})
	}))
	defer server.Close()
	p := Provider{BaseURL: server.URL, Client: server.Client()}
	for name, tc := range map[string]struct {
		images []string
		cap    *VideoReferenceCapability
	}{
		"missing":     {images: nil, cap: &VideoReferenceCapability{MaxImages: 2}},
		"empty":       {images: []string{""}, cap: &VideoReferenceCapability{MaxImages: 2}},
		"unsupported": {images: []string{"https://example.test/a.png"}},
		"over-limit":  {images: []string{"https://example.test/a.png", "https://example.test/b.png"}, cap: &VideoReferenceCapability{MaxImages: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := p.SubmitWithReferenceImages(context.Background(), Model{VideoReference: tc.cap}, Brief{}, ShotPlan{}, tc.images, "https://example.test/full.mp4")
			if err == nil || requests != 0 {
				t.Fatalf("err=%v requests=%d", err, requests)
			}
		})
	}
}

func TestProviderQueryParsesUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"status":  "succeeded",
			"content": map[string]string{"video_url": "https://example.test/result.mp4"},
			"usage":   map[string]int64{"completion_tokens": 108900, "total_tokens": 108900},
		})
	}))
	defer server.Close()
	p := Provider{BaseURL: server.URL, Client: server.Client()}
	result, err := p.Query(context.Background(), "task-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || result.Usage.CompletionTokens != 108900 || result.Usage.TotalTokens != 108900 {
		t.Fatalf("result = %+v", result)
	}
}

func TestVideoSubmitBodyGenerateAudio(t *testing.T) {
	m := Model{UpstreamModel: "doubao-seedance-2-0-mini-260615", GenerateAudio: true}
	b := Brief{Resolution: "720p", AspectRatio: "16:9"}
	s := ShotPlan{Prompt: "商品展示", Duration: 5}

	body := videoSubmitBody(m, b, s, "https://example.test/frame.png")
	if body["generate_audio"] != true {
		t.Fatalf("generate_audio = %v, want true", body["generate_audio"])
	}
	if body["model"] != m.UpstreamModel || body["resolution"] != "720p" || body["duration"] != 5 {
		t.Fatalf("unexpected submit body: %#v", body)
	}

	m.GenerateAudio = false
	body = videoSubmitBody(m, b, s, "https://example.test/frame.png")
	if _, exists := body["generate_audio"]; exists {
		t.Fatal("generate_audio must be omitted when the model switch is off")
	}
}
