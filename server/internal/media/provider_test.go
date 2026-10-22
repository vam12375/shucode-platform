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
