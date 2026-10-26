package media

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
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
			if watermark, ok := body["watermark"].(bool); !ok || watermark {
				t.Fatalf("image request must explicitly disable visible watermark: %#v", body)
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

func TestCanvasImageDisablesWatermarkWithOptionalReferences(t *testing.T) {
	for _, tc := range []struct {
		name   string
		images []string
		want   any
	}{
		{name: "text-to-image"},
		{name: "single-reference", images: []string{"https://example.test/first.png"}, want: "https://example.test/first.png"},
		{name: "multiple-references", images: []string{"https://example.test/first.png", "https://example.test/second.png"}, want: []any{"https://example.test/first.png", "https://example.test/second.png"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/images/generations" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"url": "https://example.test/result.png"}}})
			}))
			defer server.Close()
			provider := Provider{BaseURL: server.URL, Client: server.Client()}
			result, err := provider.CanvasImage(context.Background(), Model{UpstreamModel: "image-model"}, CanvasRequest{Prompt: "draw", Resolution: "2K", AspectRatio: "auto"}, tc.images)
			if err != nil || result != "https://example.test/result.png" {
				t.Fatalf("result=%s err=%v", result, err)
			}
			if watermark, ok := body["watermark"].(bool); !ok || watermark {
				t.Fatalf("canvas image request must explicitly disable visible watermark: %#v", body)
			}
			image, exists := body["image"]
			if exists != (len(tc.images) > 0) || !reflect.DeepEqual(image, tc.want) {
				t.Fatalf("reference images = %#v (exists=%v), want %#v", image, exists, tc.want)
			}
			if body["model"] != "image-model" || body["prompt"] != "draw" || body["size"] != "2K" || body["response_format"] != "url" {
				t.Fatalf("unexpected generation parameters: %#v", body)
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

func TestVideoContentAddsReferenceAudioAfterVideo(t *testing.T) {
	images := []string{"https://example.test/first.png"}
	for _, tc := range []struct {
		name, audio string
		want        int
	}{
		{"with-audio", "https://example.test/source.wav", 4},
		// 音频缺失只降级为视频参考，不能产生空 audio_url 项。
		{"without-audio", "", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := videoContent("商品展示", images, "https://example.test/source.mp4", tc.audio)
			if len(content) != tc.want {
				t.Fatalf("content = %#v", content)
			}
			if tc.audio == "" {
				return
			}
			item := content[len(content)-1].(map[string]any)
			if item["type"] != "audio_url" || item["role"] != "reference_audio" || item["audio_url"].(map[string]string)["url"] != tc.audio {
				t.Fatalf("unexpected audio item: %#v", item)
			}
		})
	}
}

func TestVideoGenerationDisablesWatermarkAcrossEntryPoints(t *testing.T) {
	for _, tc := range []struct {
		name, entry, video string
		images, roles      []string
		audio              bool
	}{
		{name: "submit-first-frame", entry: "submit", images: []string{"https://example.test/first.png"}, roles: []string{"first_frame"}},
		{name: "submit-reference-without-video", entry: "submit-reference", images: []string{"https://example.test/first.png"}, roles: []string{"first_frame"}, audio: true},
		{name: "submit-reference-with-video", entry: "submit-reference", images: []string{"https://example.test/first.png"}, video: "https://example.test/source.mp4", roles: []string{"reference_image", "reference_video"}, audio: true},
		{name: "submit-multiple-images", entry: "submit-images", images: []string{"https://example.test/first.png", "https://example.test/last.png"}, roles: []string{"first_frame", "last_frame"}},
		{name: "submit-multiple-reference-images", entry: "submit-images", images: []string{"https://example.test/first.png", "https://example.test/second.png"}, video: "https://example.test/source.mp4", roles: []string{"reference_image", "reference_image", "reference_video"}, audio: true},
		{name: "canvas-text-to-video", entry: "canvas"},
		{name: "canvas-first-last-frames", entry: "canvas", images: []string{"https://example.test/first.png", "https://example.test/last.png"}, roles: []string{"first_frame", "last_frame"}, audio: true},
		{name: "canvas-video-reference", entry: "canvas", images: []string{"https://example.test/first.png", "https://example.test/second.png"}, video: "https://example.test/source.mp4", roles: []string{"reference_image", "reference_image", "reference_video"}, audio: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodPost || r.URL.Path != "/contents/generations/tasks" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"id": "task-without-watermark"})
			}))
			defer server.Close()
			p := Provider{BaseURL: server.URL, Client: server.Client()}
			m := Model{UpstreamModel: "video-model", GenerateAudio: tc.audio, VideoReference: &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 3}}
			b := Brief{Resolution: "720p", AspectRatio: "9:16"}
			s := ShotPlan{Prompt: "商品展示", Duration: 5}
			var id string
			var err error
			switch tc.entry {
			case "submit":
				id, err = p.Submit(context.Background(), m, b, s, tc.images[0])
			case "submit-reference":
				id, err = p.SubmitWithReference(context.Background(), m, b, s, tc.images[0], tc.video)
			case "submit-images":
				id, err = p.SubmitWithReferenceImages(context.Background(), m, b, s, tc.images, tc.video)
			case "canvas":
				id, err = p.CanvasVideo(context.Background(), m, CanvasRequest{Prompt: s.Prompt, Resolution: b.Resolution, AspectRatio: b.AspectRatio, Duration: s.Duration}, tc.images, tc.video)
			}
			if err != nil || id != "task-without-watermark" || requests != 1 {
				t.Fatalf("id=%s requests=%d err=%v", id, requests, err)
			}
			if watermark, ok := body["watermark"].(bool); !ok || watermark {
				t.Fatalf("video request must explicitly disable visible watermark: %#v", body)
			}
			if body["model"] != m.UpstreamModel || body["resolution"] != b.Resolution || body["ratio"] != b.AspectRatio || body["duration"] != float64(s.Duration) {
				t.Fatalf("unexpected generation parameters: %#v", body)
			}
			audio, hasAudio := body["generate_audio"]
			if hasAudio != tc.audio || (tc.audio && audio != true) {
				t.Fatalf("generate_audio=%v exists=%v, want enabled=%v", audio, hasAudio, tc.audio)
			}
			content, ok := body["content"].([]any)
			if !ok || len(content) != len(tc.roles)+1 {
				t.Fatalf("unexpected content: %#v", body["content"])
			}
			if text := content[0].(map[string]any); text["type"] != "text" || text["text"] != s.Prompt {
				t.Fatalf("unexpected prompt: %#v", text)
			}
			for i, role := range tc.roles {
				item := content[i+1].(map[string]any)
				kind, wantURL := "image_url", ""
				if role == "reference_video" {
					kind, wantURL = "video_url", tc.video
				} else {
					wantURL = tc.images[i]
				}
				if item["role"] != role || item["type"] != kind || item[kind].(map[string]any)["url"] != wantURL {
					t.Fatalf("unexpected media content %d: %#v", i, item)
				}
			}
		})
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
	if watermark, ok := body["watermark"].(bool); !ok || watermark {
		t.Fatalf("reference video request must explicitly disable visible watermark: %#v", body)
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
