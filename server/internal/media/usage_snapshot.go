package media

import (
	"math"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/credits"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func mediaSnapshotTime(p *Project, fallback time.Time) time.Time {
	if !p.CreatedAt.IsZero() {
		return p.CreatedAt
	}
	return fallback
}

func mediaUnitUsage(record store.UsageRecord, p *Project, shot Shot, model Model) store.UsageRecord {
	unit := "image"
	quantity := int64(1)
	saleMicro, costMicro := model.PriceMicro, model.CostMicro
	if model.Kind == "video" {
		unit = "second"
		quantity = int64(shot.Plan.Duration)
		if videoReferenceRequested(p) && model.VideoReference != nil {
			saleMicro, costMicro = model.VideoReference.PriceMicro, model.VideoReference.CostMicro
		}
	}
	sale := mediaUnitRates(unit, quantity, saleMicro)
	cost := mediaUnitRates(unit, quantity, costMicro)
	record.PriceSnapshot = &store.UsagePricingSnapshot{
		SchemaVersion: 1,
		CapturedAt:    mediaSnapshotTime(p, record.CreatedAt),
		Sale:          sale,
		Cost: store.UsageCostSnapshot{
			Status: "unknown", PricingVersion: model.PriceVersion, ModelID: model.ID, Rates: &cost,
		},
	}
	if quantity <= 0 {
		record.PriceSnapshot.Cost.Reason = "invalid_usage"
		return record
	}
	if costMicro < 0 || costMicro > math.MaxInt64/quantity {
		record.PriceSnapshot.Cost.Reason = "invalid_price"
		return record
	}
	// 合法零成本也写入金额指针，和历史上完全没有快照区分开。
	estimated := costMicro * quantity
	record.PriceSnapshot.Cost.Status = "estimated"
	record.PriceSnapshot.Cost.EstimatedMicro = &estimated
	return record
}

func mediaUnitRates(unit string, quantity, micro int64) store.UsagePriceSnapshot {
	return store.UsagePriceSnapshot{
		Unit: unit, PerUnit: credits.FormatMicroCredits(micro), Quantity: quantity,
		InputPerMtok: "0.000000", OutputPerMtok: "0.000000",
		CacheReadPerMtok: "0.000000", CacheWritePerMtok: "0.000000",
		Multiplier: "1.000000", Currency: "CNY",
	}
}
