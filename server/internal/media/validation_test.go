package media

import "testing"

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
