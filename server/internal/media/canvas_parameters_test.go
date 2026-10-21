package media

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestCanvasImageCapabilities(t *testing.T) {
	for _, tc := range []struct {
		model       string
		resolutions []string
		refs        int
	}{
		{"doubao-seedream-5-0-pro-260628", []string{"1K", "2K"}, 8},
		{"doubao-seedream-5-0-260128", []string{"2K", "3K", "4K"}, 8},
		{"doubao-seedream-4-5-251128", []string{"2K", "4K"}, 8},
		{"doubao-seedream-4-0-250828", []string{"1K", "2K", "4K"}, 8},
		{"doubao-seedream-3-0-t2i-250415", []string{"1K"}, 0},
	} {
		t.Run(tc.model, func(t *testing.T) {
			m := Model{Kind: "image", UpstreamModel: tc.model}
			if !reflect.DeepEqual(ImageResolutions(m), tc.resolutions) || MaxReferenceImages(m) != tc.refs {
				t.Fatalf("capabilities: %v %d", ImageResolutions(m), MaxReferenceImages(m))
			}
		})
	}
}

func TestCanvasImageParametersReachProvider(t *testing.T) {
	var body map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"url": "https://example.test/image.jpg"}}})
	}))
	defer upstream.Close()
	provider := Provider{BaseURL: upstream.URL, Client: upstream.Client()}
	for _, tc := range []struct{ model, resolution, ratio, size, prompt string }{
		{"doubao-seedream-5-0-260128", "3K", "21:9", "3K", "draw（画面比例 21:9）"},
		{"doubao-seedream-5-0-pro-260628", "2K", "auto", "2K", "draw"},
		{"doubao-seedream-3-0-t2i-250415", "1K", "4:3", "1152x864", "draw"},
		{"doubao-seedream-3-0-t2i-250415", "1K", "auto", "1024x1024", "draw"},
	} {
		_, err := provider.CanvasImage(context.Background(), Model{UpstreamModel: tc.model}, CanvasRequest{Prompt: "draw", Resolution: tc.resolution, AspectRatio: tc.ratio}, []string{"https://example.test/first.jpg", "https://example.test/second.jpg"})
		if err != nil {
			t.Fatal(err)
		}
		if body["size"] != tc.size || body["prompt"] != tc.prompt {
			t.Fatalf("body: %#v", body)
		}
		if _, ok := body["sequential_image_generation"]; ok {
			t.Fatal("each billed output must be a single image")
		}
	}
}

func TestCanvasImageRatioAndModelValidation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := canvasRequest(t, f)
	models, _ := f.s.Repo.Models(ctx)
	models[len(models)-1].UpstreamModel = "doubao-seedream-5-0-260128"
	raw, _ := json.Marshal(models)
	_ = f.s.Repo.Store.SetConfigValue(ctx, ConfigKey, raw)
	for _, ratio := range []string{"auto", "1:1", "4:3", "3:4", "16:9", "9:16", "3:2", "2:3", "21:9"} {
		r.AspectRatio = ratio
		r.Resolution = "3K"
		if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err != nil {
			t.Fatalf("%s: %v", ratio, err)
		}
	}
	r.Resolution = "1K"
	if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err == nil {
		t.Fatal("unsupported image tier accepted")
	}
}
