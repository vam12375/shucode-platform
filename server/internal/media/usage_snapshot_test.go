package media

import (
	"testing"
	"time"
)

func TestUsageSnapshotForMediaKeepsFrozenUnitAndCost(t *testing.T) {
	s := &Service{}
	p := &Project{UserID: "u", TeamID: "t", CreatedAt: time.Date(2026, 10, 28, 1, 2, 3, 0, time.UTC)}

	image := s.usage(p, Shot{ReservationID: "image", PendingChargeMicro: 300_000}, Model{
		ID: "image-model", Kind: "image", PriceMicro: 300_000, CostMicro: 100_000, PriceVersion: "media-v1",
	})
	if image.PriceSnapshot == nil || image.PriceSnapshot.Sale.Unit != "image" || image.PriceSnapshot.Sale.Quantity != 1 || image.PriceSnapshot.Sale.PerUnit != "0.300000" {
		t.Fatalf("image snapshot = %+v", image.PriceSnapshot)
	}
	if image.PriceSnapshot.Cost.Status != "estimated" || image.PriceSnapshot.Cost.EstimatedMicro == nil || *image.PriceSnapshot.Cost.EstimatedMicro != 100_000 || image.PriceSnapshot.CapturedAt != p.CreatedAt {
		t.Fatalf("image cost snapshot = %+v", image.PriceSnapshot.Cost)
	}

	video := s.usage(p, Shot{ReservationID: "video", PendingChargeMicro: 1_000_000, Plan: ShotPlan{Duration: 5}}, Model{
		ID: "video-model", Kind: "video", PriceMicro: 200_000, CostMicro: 50_000, PriceVersion: "media-v1",
	})
	if video.PriceSnapshot == nil || video.PriceSnapshot.Sale.Unit != "second" || video.PriceSnapshot.Sale.Quantity != 5 || video.PriceSnapshot.Sale.PerUnit != "0.200000" {
		t.Fatalf("video snapshot = %+v", video.PriceSnapshot)
	}
	if video.PriceSnapshot.Cost.EstimatedMicro == nil || *video.PriceSnapshot.Cost.EstimatedMicro != 250_000 || video.PriceSnapshot.Cost.Rates.PerUnit != "0.050000" {
		t.Fatalf("video cost snapshot = %+v", video.PriceSnapshot.Cost)
	}
}

func TestUsageSnapshotForVideoReferenceUsesFrozenReferenceRates(t *testing.T) {
	s := &Service{}
	p := &Project{UserID: "u", TeamID: "t", Brief: Brief{GenerationMode: "video_reference"}}
	usage := s.usage(p, Shot{ReservationID: "reference", PendingChargeMicro: 2_500_000, Plan: ShotPlan{Duration: 5}}, Model{
		ID: "video-model", Kind: "video", PriceMicro: 200_000, CostMicro: 50_000, PriceVersion: "media-v1",
		VideoReference: &VideoReferenceCapability{PriceMicro: 500_000, CostMicro: 125_000, MaxDurationSeconds: 15, MaxImages: 8},
	})
	if usage.PriceSnapshot == nil || usage.PriceSnapshot.Sale.Unit != "second" || usage.PriceSnapshot.Sale.PerUnit != "0.500000" {
		t.Fatalf("reference sale snapshot = %+v", usage.PriceSnapshot)
	}
	if usage.PriceSnapshot.Cost.EstimatedMicro == nil || *usage.PriceSnapshot.Cost.EstimatedMicro != 625_000 {
		t.Fatalf("reference cost snapshot = %+v", usage.PriceSnapshot.Cost)
	}
}
