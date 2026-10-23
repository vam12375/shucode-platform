package media

import (
	"slices"
	"testing"
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
		Durations:     []int{4, 5, 10, 15},
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

func TestAllowedDurationsExpandsRangeButKeepsLegacyEnumeration(t *testing.T) {
	ranged := fixtureVideoModel()
	ranged.Durations = nil
	ranged.MinDuration = 4
	ranged.MaxDuration = 6
	if got := ranged.AllowedDurations(); !slices.Equal(got, []int{4, 5, 6}) {
		t.Fatalf("AllowedDurations() = %v, want [4 5 6]", got)
	}
	legacy := fixtureVideoModel()
	if got := legacy.AllowedDurations(); !slices.Equal(got, legacy.Durations) {
		t.Fatalf("AllowedDurations() = %v, want legacy enumeration %v", got, legacy.Durations)
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
