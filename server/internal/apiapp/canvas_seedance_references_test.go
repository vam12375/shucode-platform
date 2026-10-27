package apiapp

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/media"
)

func TestCanvasPublicCapabilitiesFollowActualSKU(t *testing.T) {
	models := []media.Model{
		{ID: "ark", Name: "Unrelated display", Kind: "video", Adapter: "ark", UpstreamModel: "doubao-seedance-2-0-fast-260128", GenerateAudio: true, Resolutions: []string{"480p", "720p"}, VideoReference: &media.VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 8, PriceMicro: 100000}},
		{ID: "toapis", Name: "Seedance", Kind: "video", Adapter: "toapis", UpstreamModel: "seedance-2-mini", Resolutions: []string{"720p"}},
		{ID: "old", Name: "Seedance 2.0", Kind: "video", Adapter: "ark", UpstreamModel: "legacy"},
	}
	public := publicMediaModels(models)
	ark, ok := public[0]["canvasCapabilities"].(*media.CanvasCapabilities)
	if !ok || !ark.ReturnLastFrame || !ark.GenerateAudio || ark.MaxVideos != 3 {
		t.Fatal(public[0])
	}
	toapis, ok := public[1]["canvasCapabilities"].(*media.CanvasCapabilities)
	if !ok || toapis.ReturnLastFrame || toapis.GenerateAudio || toapis.MaxVideos != 0 {
		t.Fatal(public[1])
	}
	if public[2]["canvasCapabilities"] != nil {
		t.Fatal("display name inferred unsupported capability")
	}
	if got := public[0]["resolutions"].([]string); len(got) != 2 || got[1] != "720p" {
		t.Fatal(got)
	}
}

func TestCanvasPublicMediaUploadAcceptsProbedAudioAndVideo(t *testing.T) {
	f, token, _ := newUserConfigPolicyFixture(t)
	f.deps.Media.Blobs.Root = t.TempDir()
	f.deps.Media.ReferenceProbe = func(context.Context, string) (media.ReferenceMetadata, error) {
		return media.ReferenceMetadata{DurationSeconds: 3, Width: 720, Height: 1280}, nil
	}
	for _, tc := range []struct {
		name, kind string
		data       []byte
	}{
		{"reference.mp4", "video", append([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}, make([]byte, 512)...)},
		{"reference.wav", "audio", append([]byte("RIFF0000WAVE"), make([]byte, 512)...)},
	} {
		body := new(bytes.Buffer)
		writer := multipart.NewWriter(body)
		part, err := writer.CreateFormFile("file", tc.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(tc.data); err != nil {
			t.Fatal(err)
		}
		if err = writer.Close(); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/api/v1/media/assets", body)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", writer.FormDataContentType())
		w := httptest.NewRecorder()
		f.engine.ServeHTTP(w, r)
		var envelope struct {
			Data media.Asset `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || envelope.Data.Kind != tc.kind || envelope.Data.Internal || envelope.Data.DurationSeconds != 3 || envelope.Data.URL == "" {
			t.Fatalf("upload %s: %d %s", tc.name, w.Code, w.Body.String())
		}
	}
}

func TestCanvasPublicSeedanceDurationsUseConfiguredIntersection(t *testing.T) {
	models := publicMediaModels([]media.Model{
		{ID: "range", Kind: "video", Adapter: "ark", UpstreamModel: "doubao-seedance-2-0-fast-260128", MinDuration: 2, MaxDuration: 30},
		{ID: "sparse", Kind: "video", Adapter: "toapis", UpstreamModel: "seedance-2-mini", Durations: []int{2, 5, 20}},
		{ID: "legacy", Kind: "video", Adapter: "ark", UpstreamModel: "legacy", Durations: []int{2, 20}},
	})
	if models[0]["minDuration"] != 4 || models[0]["maxDuration"] != 15 || !reflect.DeepEqual(models[0]["durations"], []int{4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}) {
		t.Fatal(models[0])
	}
	if !reflect.DeepEqual(models[1]["durations"], []int{5}) || models[1]["minDuration"] != nil {
		t.Fatal("sparse configuration invented choices", models[1])
	}
	if !reflect.DeepEqual(models[2]["durations"], []int{2, 20}) {
		t.Fatal("legacy directory changed", models[2])
	}
}
