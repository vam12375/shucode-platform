package media

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func seedream55FixtureModel() Model {
	return Model{ID: "seedream55", Name: "Seedream 5.5", Kind: "image", Adapter: "ark", ProviderID: "ark", UpstreamModel: "doubao-seedream-5-5-261028", PriceVersion: "fixture", PriceMicro: 328_572, CostMicro: 230_000,
		ImagePricing: &ImagePricing{InputPriceMicro: 71_429, InputCostMicro: 50_000, OutputTiers: []ImagePriceTier{{MaxPixels: 2_610_000, PriceMicro: 185_715, CostMicro: 130_000}, {MaxPixels: 4_630_000, PriceMicro: 328_572, CostMicro: 230_000}}}}
}

func TestSeedream55CapabilitiesInputAndPricing(t *testing.T) {
	m := seedream55FixtureModel()
	if ValidateModels([]Model{m}) != nil || ImageCapabilitiesFor(m).MaxLayerOutputs != 100 || MaxReferenceImages(m) != 10 || len(ImageResolutions(m)) != 3 {
		t.Fatal("missing model capabilities")
	}
	base := CanvasRequest{Kind: "image", Resolution: "2K", AspectRatio: "auto", Count: 1}
	for _, tc := range []struct {
		name    string
		options *ImageOptions
		images  int
		valid   bool
	}{
		{"text", nil, 0, true}, {"ten-images", nil, 10, true}, {"too-many-images", nil, 11, false},
		{"transparent-text", &ImageOptions{Background: "transparent", OutputFormat: "png"}, 0, true},
		{"transparent-jpeg", &ImageOptions{Background: "transparent", OutputFormat: "jpeg"}, 1, false},
		{"custom-size", &ImageOptions{Size: "2880x1440"}, 1, true}, {"small-size", &ImageOptions{Size: "512x512"}, 0, false},
		{"wide-size", &ImageOptions{Size: "16000x64"}, 0, false}, {"max-layers", &ImageOptions{LayerDecomposition: true, MaxImages: 100, TextRecognition: true, Size: "auto"}, 1, true},
		{"excess-layers", &ImageOptions{LayerDecomposition: true, MaxImages: 101}, 1, false}, {"multi-input-layers", &ImageOptions{LayerDecomposition: true}, 2, false},
		{"layers-background", &ImageOptions{LayerDecomposition: true, Background: "auto"}, 1, false}, {"layers-custom-size", &ImageOptions{LayerDecomposition: true, Size: "1024x1024"}, 1, false},
		{"ocr-without-layers", &ImageOptions{TextRecognition: true}, 1, false}, {"unsupported-optimization", &ImageOptions{OptimizePromptMode: "fast"}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			r.ImageOptions = tc.options
			if (validateSeedream55Input(m, r, tc.images) == nil) != tc.valid {
				t.Fatalf("valid=%v", tc.valid)
			}
		})
	}
	r := base
	r.ImageOptions = &ImageOptions{LayerDecomposition: true, MaxImages: 100}
	if got := seedream55Quote(m, r, 1); got != 32_928_629 {
		t.Fatal(got)
	}
	r.ImageOptions = &ImageOptions{Size: "1024x1024"}
	if got := seedream55Quote(m, r, 2); got != 328_573 {
		t.Fatal(got)
	}
	r.ImageOptions = &ImageOptions{LayerDecomposition: true}
	r.Count = 2
	if validateSeedream55Input(m, r, 1) == nil {
		t.Fatal("multiple layer requests accepted")
	}
	other := fixtureVideoModel()
	if validateSeedream55Input(other, r, 1) == nil {
		t.Fatal("image options leaked to video")
	}
}

func TestSeedream55LayersSettleActualPixelsAndRecoverDownloads(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	m := seedream55FixtureModel()
	raw, _ := json.Marshal([]Model{m})
	if err := f.s.Repo.Store.SetConfigValue(ctx, ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	var submits, downloads atomic.Int64
	var body map[string]any
	var small, large bytes.Buffer
	_ = png.Encode(&small, image.NewNRGBA(image.Rect(0, 0, 1024, 1024)))
	_ = png.Encode(&large, image.NewNRGBA(image.Rect(0, 0, 2048, 2048)))
	id, size, err := f.s.Blobs.Put(f.team, bytes.NewReader(small.Bytes()), MaxUpload)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Repo.AddAsset(ctx, f.team, Asset{ID: id, Kind: "image", Mime: "image/png", Size: size}); err != nil {
		t.Fatal(err)
	}
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			submits.Add(1)
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = json.NewEncoder(w).Encode(map[string]any{"usage": map[string]int{"input_images": 1, "generated_images": 3}, "data": []any{
				map[string]any{"url": upstream.URL + "/base.png", "size": "2048x2048", "output_format": "png", "z_index": 0},
				map[string]any{"url": upstream.URL + "/layer1.png", "size": "1024x1024", "output_format": "png", "z_index": 1, "bounding_box": map[string]any{"absolute": []int{0, 0, 1024, 1024}, "normalized": []int{0, 0, 500, 500}}, "text_recognition": map[string]any{"text": "门道", "lines": []any{}}},
				map[string]any{"url": upstream.URL + "/layer2.png", "size": "1024x1024", "output_format": "png", "z_index": 2},
			}})
			return
		}
		if downloads.Add(1) == 2 {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/base.png" {
			_, _ = w.Write(large.Bytes())
		} else {
			_, _ = w.Write(small.Bytes())
		}
	}))
	defer upstream.Close()
	f.s.Provider = func(context.Context, Model) (Provider, error) {
		return Provider{Adapter: "ark", BaseURL: upstream.URL, Client: upstream.Client()}, nil
	}
	f.s.Blobs.Client = upstream.Client()
	r := CanvasRequest{Kind: "image", ModelID: m.ID, Prompt: "拆分", AssetIDs: []string{id}, Resolution: "2K", AspectRatio: "auto", Count: 1, ImageOptions: &ImageOptions{LayerDecomposition: true, MaxImages: 4, TextRecognition: true}, BoardID: "b", NodeID: "n", WorkspacePath: "/fixture"}
	r.ExpectedQuoteMicro = seedream55Quote(m, r, 1)
	p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	if p.Status != "completed" || p.ChargedMicro != 771_431 || submits.Load() != 1 || downloads.Load() != 4 {
		t.Fatalf("status=%s charge=%d submits=%d downloads=%d", p.Status, p.ChargedMicro, submits.Load(), downloads.Load())
	}
	if body["layer_decomposition"] != true || body["background"] != nil || body["size"] != "2K" {
		t.Fatal(body)
	}
	if p.Shots[0].ImageCostMicro != 540_000 || len(p.View().Shots[0].ImageOutputs) != 3 || p.View().Shots[0].ImageOutputs[1].TextRecognition.Text != "门道" || p.View().Shots[0].ImageOutputs[0].ResultURL != "" {
		t.Fatal(p.View())
	}
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if team.UsedCredits != "0.771431" || team.ReservedCredits != "0.000000" {
		t.Fatal(team)
	}
	if len(generatedArchiveSources(*p)) != 3 {
		t.Fatal("layer assets not archived")
	}
	if _, err = f.s.CreateCanvas(ctx, f.team, f.user, "", p.ID, r); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		f.s.Tick(ctx)
	}
	if submits.Load() != 1 {
		t.Fatal("replay generated another paid image")
	}
}

func TestSeedance25FlashLimitsDoNotInheritBase(t *testing.T) {
	m := fixtureVideoModel()
	m.UpstreamModel = "doubao-seedance-2-5-flash-261115"
	m = NormalizeSeedanceDuration(m)
	m.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 30, MaxImages: 8, TokenPriceMicroPerMillion: 1}
	c := CanvasCapabilitiesFor(m)
	if c.MaxImages != 10 || c.MaxVideos != 5 || c.MaxAudios != 5 || c.MaxTotalReferences != 20 || c.MaxVideoSeconds != 30 || c.MaxAudioSeconds != 30 || !m.DurationSupported(30) || m.DurationSupported(31) {
		t.Fatal(c, m)
	}
	for _, resolution := range []string{"480p", "720p", "1080p", "4k"} {
		m.Resolutions = []string{resolution}
		if (ValidateModels([]Model{m}) == nil) != (resolution == "480p" || resolution == "720p") {
			t.Fatal(resolution)
		}
	}
	r := CanvasRequest{Kind: "video", ReferenceMode: "multimodal", Duration: 30, AssetIDs: make([]string, 10), VideoReferences: make([]CanvasVideoReference, 5), AudioAssetIDs: make([]string, 5)}
	if validateCanvasOptions(m, r) != nil {
		t.Fatal("valid 20 references rejected")
	}
	r.AssetIDs = append(r.AssetIDs, "overflow")
	if validateCanvasOptions(m, r) == nil {
		t.Fatal("overflow accepted")
	}
	r.AssetIDs = r.AssetIDs[:10]
	r.AudioAssetIDs = append(r.AudioAssetIDs, "overflow")
	if validateCanvasOptions(m, r) == nil {
		t.Fatal("audio overflow accepted")
	}
	m.UpstreamModel = "doubao-seedance-2-5-260628"
	if CanvasCapabilitiesFor(m).MaxImages != 30 {
		t.Fatal("base capabilities regressed")
	}
}
