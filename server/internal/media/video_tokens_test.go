package media

import "testing"

func TestVideoReferenceQuoteUsesFifteenSecondUpperBound(t *testing.T) {
	ref := &VideoReferenceCapability{
		MaxDurationSeconds:        15,
		MaxImages:                 4,
		TokenPriceMicroPerMillion: 40_000_000,
	}
	// 480p 16:9: 20 × 864 × 496 × 24 / 1024 = 200,880 tokens；40 credit/M。
	if got := videoReferenceQuoteMicro(ref, "480p", "16:9", 5); got != 8_035_200 {
		t.Fatalf("480p quote = %d, want 8035200", got)
	}
	// 720p 9:16: 20 × 720 × 1280 × 24 / 1024 = 432,000 tokens；40 credit/M。
	if got := videoReferenceQuoteMicro(ref, "720p", "9:16", 5); got != 17_280_000 {
		t.Fatalf("720p quote = %d, want 17280000", got)
	}
	// 官方480p 1:1为640×640；不能按16:9短边裁成496×496估算。
	if got := videoReferenceQuoteMicro(ref, "480p", "1:1", 5); got != 7_680_000 {
		t.Fatalf("480p square quote = %d, want 7680000", got)
	}

	capped := *ref
	capped.MaxDurationSeconds = 90
	if got := videoReferenceQuoteMicro(&capped, "480p", "16:9", 5); got != 8_035_200 {
		t.Fatalf("input duration above 15s was not capped: %d", got)
	}
}

func TestVideoReferenceQuoteFallsBackToPerSecond(t *testing.T) {
	ref := &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 4, PriceMicro: 2_000_000}
	if got := videoReferenceQuoteMicro(ref, "720p", "16:9", 5); got != 10_000_000 {
		t.Fatalf("legacy quote = %d, want 10000000", got)
	}
}

func TestAutoReferenceQuoteUsesMaximumVerifiedPixelArea(t *testing.T) {
	ref := &VideoReferenceCapability{MaxDurationSeconds: 15, TokenPriceMicroPerMillion: 40_000_000}
	for _, resolution := range []string{"480p", "720p", "1080p", "4k"} {
		maximum := int64(0)
		for _, ratio := range fixedVideoRatios {
			quote := videoReferenceQuoteMicro(ref, resolution, ratio, 5)
			if quote > maximum {
				maximum = quote
			}
		}
		if quote := videoReferenceQuoteMicro(ref, resolution, "auto", 5); quote != maximum || quote == 0 {
			t.Fatalf("%s adaptive quote=%d want maximum=%d", resolution, quote, maximum)
		}
	}
	if estimateVideoTokens("720p", "unsupported", 15, 5) != 0 {
		t.Fatal("unknown ratio silently used a supported size")
	}
}

func TestVideoTokenChargeRoundsUp(t *testing.T) {
	if got := videoTokenChargeMicro(1, 1_000_000); got != 1 {
		t.Fatalf("charge = %d, want 1", got)
	}
	if got := videoTokenChargeMicro(90_396, 40_000_000); got != 3_615_840 {
		t.Fatalf("charge = %d, want 3615840", got)
	}
}

func TestUsageSnapshotUsesActualVideoTokens(t *testing.T) {
	s := &Service{}
	p := &Project{UserID: "user", TeamID: "team", Brief: Brief{GenerationMode: "video_reference"}}
	m := Model{
		ID: "v", Kind: "video", PriceVersion: "v1",
		VideoReference: &VideoReferenceCapability{
			MaxDurationSeconds:        15,
			MaxImages:                 4,
			TokenPriceMicroPerMillion: 40_000_000,
			TokenCostMicroPerMillion:  28_000_000,
		},
	}
	shot := Shot{
		ReservationID:            "reservation",
		ProviderUsageTokens:      90_396,
		TokenChargeMicro:         3_615_840,
		TokenRateMicroPerMillion: 40_000_000,
		TokenCostMicroPerMillion: 28_000_000,
	}
	usage := s.usage(p, shot, m)
	if usage.OutputTokens != 90_396 || usage.CreditsMicro != 3_615_840 {
		t.Fatalf("usage tokens=%d credits=%d", usage.OutputTokens, usage.CreditsMicro)
	}
	if usage.PriceSnapshot == nil || usage.PriceSnapshot.Sale.OutputPerMtok != "40.000000" {
		t.Fatalf("sale snapshot = %+v", usage.PriceSnapshot)
	}
	if usage.PriceSnapshot.Cost.Rates == nil || usage.PriceSnapshot.Cost.Rates.OutputPerMtok != "28.000000" {
		t.Fatalf("cost snapshot = %+v", usage.PriceSnapshot.Cost)
	}
	if usage.PriceSnapshot.Cost.PricingVersion != "v1" || usage.PriceSnapshot.Cost.Status != "estimated" || usage.PriceSnapshot.Cost.EstimatedMicro == nil || *usage.PriceSnapshot.Cost.EstimatedMicro != 2_531_088 {
		t.Fatalf("cost metadata = %+v", usage.PriceSnapshot.Cost)
	}
}

func TestMediaVideoBillingUnitsQuoteIndependentOfDuration(t *testing.T) {
	ext := Model{Kind: "video", BillingUnit: BillingUnitRequest, PriceMicro: 8_750_000}
	if got := mediaVideoQuoteMicro(ext, "720p", "16:9", 4); got != ext.PriceMicro {
		t.Fatalf("ext 4s quote=%d", got)
	}
	if got := mediaVideoQuoteMicro(ext, "720p", "16:9", 15); got != ext.PriceMicro {
		t.Fatalf("ext 15s quote=%d", got)
	}
	ref := &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 8, PriceMicro: ext.PriceMicro}
	if got := mediaVideoReferenceQuoteMicro(ext, ref, "720p", "16:9", 4); got != ext.PriceMicro {
		t.Fatalf("ext reference 4s quote=%d", got)
	}
	if got := mediaVideoReferenceQuoteMicro(ext, ref, "720p", "16:9", 15); got != ext.PriceMicro {
		t.Fatalf("ext reference 15s quote=%d", got)
	}
	standard := Model{Kind: "video", BillingUnit: BillingUnitToken, TokenPriceMicroPerMillion: 70_000_000}
	if mediaVideoQuoteMicro(standard, "720p", "16:9", 4) >= mediaVideoQuoteMicro(standard, "720p", "16:9", 15) {
		t.Fatal("token estimate should grow with duration")
	}
}

func TestResultUsageTokensUsesTotalForToAPIsTokenBilling(t *testing.T) {
	result := ProviderResult{}
	result.Usage.CompletionTokens, result.Usage.TotalTokens = 100, 250
	if got := resultUsageTokens(result, true); got != 250 {
		t.Fatalf("total billing tokens=%d", got)
	}
	if got := resultUsageTokens(result, false); got != 100 {
		t.Fatalf("completion billing tokens=%d", got)
	}
	result.Usage.TotalTokens = 0
	if got := resultUsageTokens(result, true); got != 0 {
		t.Fatalf("missing total must not fall back to completion: %d", got)
	}
}
