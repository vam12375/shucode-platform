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

func TestValidateModelsArkSeedance25ReferenceAndResolutionLimits(t *testing.T) {
	for _, upstream := range []string{"doubao-seedance-2-5-260628", "doubao-seedance-2-5-pro-260128"} {
		for _, tc := range []struct {
			name       string
			resolution string
			reference  int
			accept     bool
		}{
			{"480p", "480p", 30, true},
			{"720p", "720p", 30, true},
			{"1080p", "1080p", 30, true},
			{"4k", "4k", 30, false},
			{"reference-too-short", "720p", 1, false},
			{"reference-too-long", "720p", 31, false},
		} {
			t.Run(upstream+"/"+tc.name, func(t *testing.T) {
				m := fixtureVideoModel()
				m.UpstreamModel = upstream
				m.MinDuration, m.MaxDuration = 4, 30
				m.Resolutions = []string{tc.resolution}
				m.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: tc.reference, MaxImages: 8, PriceMicro: 1}
				if err := ValidateModels([]Model{m}); (err == nil) != tc.accept {
					t.Fatalf("accept=%v, got err=%v", tc.accept, err)
				}
			})
		}
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
	for _, test := range []struct {
		model Model
		max   int
	}{
		{Model{Kind: "video", Adapter: "ark", UpstreamModel: "doubao-seedance-2-0-mini-260615", Durations: []int{4, 5, 10, 15}}, 15},
		{Model{Kind: "video", Adapter: "toapis", UpstreamModel: "seedance-2-5", Durations: []int{4, 5, 10, 15}}, 30},
	} {
		got := NormalizeSeedanceDuration(test.model)
		if got.MinDuration != 4 || got.MaxDuration != test.max || got.Durations != nil || !got.DurationSupported(7) {
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

func TestValidateBriefDurationBounds(t *testing.T) {
	m := fixtureVideoModel()
	base := Brief{Mode: "auto", ReferenceMode: "multimodal", Product: "产品", AssetIDs: []string{"asset-a"}, Platforms: []string{"douyin"}, VideoModel: m.ID, Resolution: "720p", AspectRatio: "16:9", Duration: 15, MaxCreditsMicro: 10_000_000}
	assets := []Asset{{ID: "asset-a", Kind: "image"}}
	// 以 15 秒为基线：范围内的时长结论必须与基线一致，范围外必须是 ErrInvalid。
	baseline := ValidateBrief(base, []Model{m}, assets)
	for _, d := range []int{MinAutoBriefSeconds, 123, MaxBriefSeconds} {
		b := base
		b.Duration = d
		if got := ValidateBrief(b, []Model{m}, assets); got != baseline {
			t.Fatalf("ValidateBrief(duration=%d) = %v, want baseline %v", d, got, baseline)
		}
	}
	for _, d := range []int{0, 5, MinAutoBriefSeconds - 1, MaxBriefSeconds + 1} {
		b := base
		b.Duration = d
		if got := ValidateBrief(b, []Model{m}, assets); got != ErrInvalid {
			t.Fatalf("ValidateBrief(duration=%d) = %v, want ErrInvalid", d, got)
		}
	}
}

func TestValidateBriefNativeAudioRequiresModelCapability(t *testing.T) {
	m := fixtureVideoModel()
	assets := []Asset{{ID: "asset-a", Kind: "image"}}
	base := Brief{Mode: "auto", ReferenceMode: "multimodal", Product: "产品", AssetIDs: []string{"asset-a"}, Platforms: []string{"douyin"}, VideoModel: m.ID, Resolution: "720p", AspectRatio: "16:9", Duration: 15, MaxCreditsMicro: 10_000_000, Finishing: &FinishingOptions{Subtitles: "none", NativeAudio: true}}
	baseline := base
	baseline.Finishing = &FinishingOptions{Subtitles: "none"}
	want := ValidateBrief(baseline, []Model{m}, assets)
	// 模型未声明原声时拒绝保留原声；声明后结论与不保留原声一致。
	if got := ValidateBrief(base, []Model{m}, assets); got != ErrInvalid {
		t.Fatalf("ValidateBrief(nativeAudio without capability) = %v, want ErrInvalid", got)
	}
	m.GenerateAudio = true
	if got := ValidateBrief(base, []Model{m}, assets); got != want {
		t.Fatalf("ValidateBrief(nativeAudio with capability) = %v, want %v", got, want)
	}
}

func seedance25ExtModel() Model {
	return Model{
		ID: "toapis-seedance-2.5-ext-720p", Name: "ToAPIs Seedance 2.5 Ext 720p", Kind: "video",
		ProviderID: "toapis", UpstreamModel: "seedance-2.5-ext", Adapter: "toapis",
		Resolutions: []string{"720p"}, MinDuration: 4, MaxDuration: 30,
		BillingUnit: BillingUnitRequest, PriceMicro: 8_750_000, CostMicro: 8_750_000, PriceVersion: "v1",
		RequestPriceTiers: []RequestPriceTier{
			{MinDuration: 4, MaxDuration: 14, PriceMicro: 8_750_000, CostMicro: 8_750_000},
			{MinDuration: 15, MaxDuration: 30, PriceMicro: 17_500_000, CostMicro: 17_500_000},
		},
	}
}

func TestValidateRequestPriceTiers(t *testing.T) {
	m := seedance25ExtModel()
	if err := ValidateModels([]Model{m}); err != nil {
		t.Fatalf("valid request tiers rejected: %v", err)
	}
	for _, test := range []struct {
		name string
		edit func(*Model)
	}{
		{"gap", func(m *Model) { m.RequestPriceTiers[0].MaxDuration = 13 }},
		{"overlap", func(m *Model) { m.RequestPriceTiers[1].MinDuration = 14 }},
		{"incomplete", func(m *Model) { m.RequestPriceTiers = m.RequestPriceTiers[:1] }},
		{"starts-late", func(m *Model) { m.RequestPriceTiers[0].MinDuration, m.RequestPriceTiers[0].MaxDuration = 5, 14 }},
		{"zero-price", func(m *Model) { m.RequestPriceTiers[1].PriceMicro = 0 }},
		{"token-unit", func(m *Model) {
			m.BillingUnit = BillingUnitToken
			m.TokenPriceMicroPerMillion = 70_000_000
			m.PriceMicro = 0
		}},
		{"above-capability", func(m *Model) {
			m.MinDuration, m.MaxDuration = 4, 31
			m.RequestPriceTiers[1].MaxDuration = 31
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := seedance25ExtModel()
			test.edit(&m)
			if ValidateModels([]Model{m}) == nil {
				t.Fatal("invalid request tiers accepted")
			}
		})
	}
}

func TestSeedance25DurationRangeIsThirtySeconds(t *testing.T) {
	for _, test := range []struct {
		adapter, upstream string
	}{
		{"toapis", "seedance-2-5"},
		{"toapis", "seedance-2.5-ext"},
		{"ark", "doubao-seedance-2-5-pro-260128"},
		{"ark", "doubao-seedance-2-5-260128"},
	} {
		model := Model{Kind: "video", Adapter: test.adapter, UpstreamModel: test.upstream, Durations: []int{4, 5, 10, 15}}
		got := NormalizeSeedanceDuration(model)
		if got.MinDuration != 4 || got.MaxDuration != 30 || got.Durations != nil || !got.DurationSupported(20) {
			t.Fatalf("%s normalized = %+v", test.upstream, got)
		}
	}
	model := Model{Kind: "video", Adapter: "toapis", UpstreamModel: "seedance-2-mini", Durations: []int{4, 5, 10, 15}}
	if got := NormalizeSeedanceDuration(model); got.MaxDuration != 15 {
		t.Fatalf("Seedance 2.0 max = %d, want 15", got.MaxDuration)
	}
}
