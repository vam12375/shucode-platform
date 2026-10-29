package media

import (
	"slices"
	"testing"
	"time"
)

func fixtureVideoModel() Model {
	return Model{
		ID:            "seedance-2.0-mini-720p",
		Name:          "Seedance 2.0 mini 720p",
		Kind:          "video",
		ProviderID:    "provider",
		UpstreamModel: "doubao-seedance-2-0-mini-260615",
		Adapter:       "ark",
		Resolutions:   []string{"720p"},
		MinDuration:   4,
		MaxDuration:   15,
		PriceMicro:    498_000,
		PriceVersion:  "v1",
	}
}

func TestValidateModelsAcceptsSeedanceMini(t *testing.T) {
	m := fixtureVideoModel()
	m.Resolutions = []string{"480p", "720p"}
	m.CostMicro = 232_000
	m.GenerateAudio = true
	if err := ValidateModels([]Model{m}); err != nil {
		t.Fatalf("ValidateModels() = %v, want nil", err)
	}
}

func TestAutoMultimodalBuildShotsUsesGlobalImagesWithoutFirstFrame(t *testing.T) {
	m := fixtureVideoModel()
	b := Brief{Mode: "auto", ReferenceMode: "multimodal", Product: "产品", AssetIDs: []string{"asset-a", "asset-a", "asset-b"}, Platforms: []string{"douyin"}, VideoModel: m.ID, Resolution: "720p", AspectRatio: "16:9", Duration: 5, MaxCreditsMicro: 10_000_000}
	plan := Plan{References: []Reference{{URL: "https://example.test/reference", Title: "参考", Evidence: "事实", SelectionReason: "相关", CapturedAt: time.Now()}}, Shots: []ShotPlan{{SourceURL: "https://example.test/reference", SourceStart: 0, SourceEnd: 5, Duration: 5, Framing: "中景", Camera: "推进", Action: "展示", Purpose: "转化", Prompt: "展示产品"}}}
	shots, _, err := BuildShots(b, plan, []Model{m})
	if err != nil || len(shots) != 1 || shots[0].Plan.AssetID != "" || shots[0].Plan.ImagePrompt != "" {
		t.Fatalf("auto global references were treated as first frame: shots=%+v err=%v", shots, err)
	}
}

func TestValidateModelsRejectsAudioForImage(t *testing.T) {
	m := fixtureVideoModel()
	m.Kind = "image"
	m.Resolutions = nil
	m.Durations = nil
	m.GenerateAudio = true
	if err := ValidateModels([]Model{m}); err == nil {
		t.Fatal("ValidateModels() accepted generateAudio for an image model, want error")
	}
}

func TestValidateModelsRejectsNegativeCost(t *testing.T) {
	m := fixtureVideoModel()
	m.CostMicro = -1
	if err := ValidateModels([]Model{m}); err == nil {
		t.Fatal("ValidateModels() accepted a negative cost, want error")
	}
}

func TestValidateModelsAccepts4K(t *testing.T) {
	m := fixtureVideoModel()
	m.Resolutions = []string{"4k"}
	if err := ValidateModels([]Model{m}); err != nil {
		t.Fatalf("ValidateModels() = %v, want nil", err)
	}
}

func TestValidateModelsRejectsUnsupportedResolution(t *testing.T) {
	m := fixtureVideoModel()
	m.Resolutions = []string{"8k"}
	if err := ValidateModels([]Model{m}); err == nil {
		t.Fatal("ValidateModels() accepted 8k, want error")
	}
}

func TestValidateModelsAcceptsDurationRange(t *testing.T) {
	m := fixtureVideoModel()
	m.Durations = nil
	m.MinDuration = 4
	m.MaxDuration = 15
	if err := ValidateModels([]Model{m}); err != nil {
		t.Fatalf("ValidateModels() = %v, want nil", err)
	}
	for _, d := range []int{4, 7, 12, 15} {
		if !m.DurationSupported(d) {
			t.Fatalf("DurationSupported(%d) = false, want true", d)
		}
	}
	for _, d := range []int{1, 3, 16, 30} {
		if m.DurationSupported(d) {
			t.Fatalf("DurationSupported(%d) = true, want false", d)
		}
	}
}

func TestAllowedDurationsExpandsContinuousRange(t *testing.T) {
	ranged := fixtureVideoModel()
	ranged.MaxDuration = 6
	if got := ranged.AllowedDurations(); !slices.Equal(got, []int{4, 5, 6}) {
		t.Fatalf("AllowedDurations() = %v, want [4 5 6]", got)
	}
}

func TestNormalizeSeedanceDurationUpgradesLegacyList(t *testing.T) {
	for _, model := range []Model{
		{Kind: "video", Adapter: "ark", UpstreamModel: "doubao-seedance-2-0-mini-260615", Durations: []int{4, 5, 10, 15}},
		{Kind: "video", Adapter: "toapis", UpstreamModel: "seedance-2-5", Durations: []int{4, 5, 10, 15}},
	} {
		got := NormalizeSeedanceDuration(model)
		if got.MinDuration != 4 || got.MaxDuration != 15 || got.Durations != nil || !got.DurationSupported(7) {
			t.Fatalf("normalized Seedance model = %+v", got)
		}
	}
	legacy := Model{Kind: "video", Adapter: "ark", UpstreamModel: "legacy", Durations: []int{4, 5, 10, 15}}
	if got := NormalizeSeedanceDuration(legacy); !slices.Equal(got.Durations, legacy.Durations) || got.MinDuration != 0 || got.MaxDuration != 0 {
		t.Fatalf("unrelated legacy model changed: %+v", got)
	}
}

func TestValidateModelsRejectsInvalidDurationRange(t *testing.T) {
	for _, bounds := range [][2]int{{0, 15}, {4, 0}, {15, 4}, {1, 15}, {4, 31}} {
		m := fixtureVideoModel()
		m.Durations = nil
		m.MinDuration = bounds[0]
		m.MaxDuration = bounds[1]
		if err := ValidateModels([]Model{m}); err == nil {
			t.Fatalf("ValidateModels() accepted duration range %v, want error", bounds)
		}
	}
}
