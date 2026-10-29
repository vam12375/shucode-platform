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
		unit = model.EffectiveBillingUnit()
		if unit == BillingUnitSecond {
			quantity = int64(shot.Plan.Duration)
		}
		if unit == BillingUnitToken {
			unit = "token"
			saleMicro, costMicro = model.TokenPriceMicroPerMillion, model.TokenCostMicroPerMillion
		}
		if videoReferenceRequested(p) && model.VideoReference != nil && unit != BillingUnitRequest {
			if model.VideoReference.TokenPriceMicroPerMillion > 0 && shot.ProviderUsageTokens > 0 {
				unit = "token"
				saleMicro, costMicro = model.VideoReference.TokenPriceMicroPerMillion, model.VideoReference.TokenCostMicroPerMillion
			} else {
				saleMicro, costMicro = model.VideoReference.PriceMicro, model.VideoReference.CostMicro
			}
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
	if unit == "token" {
		quantity = shot.ProviderUsageTokens
		record.PriceSnapshot.Sale.Unit = "token"
		record.PriceSnapshot.Sale.OutputPerMtok = credits.FormatMicroCredits(saleMicro)
		record.PriceSnapshot.Sale.PerUnit = ""
		record.PriceSnapshot.Sale.Quantity = quantity
		record.PriceSnapshot.Cost.Rates.OutputPerMtok = credits.FormatMicroCredits(costMicro)
		record.PriceSnapshot.Cost.Rates.PerUnit = ""
		record.PriceSnapshot.Cost.Rates.Quantity = quantity
		if quantity <= 0 {
			record.PriceSnapshot.Cost.Reason = "usage_unavailable"
			return record
		}
		estimated := videoTokenChargeMicro(quantity, costMicro)
		record.PriceSnapshot.Cost.Status = "estimated"
		record.PriceSnapshot.Cost.EstimatedMicro = &estimated
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
	perUnit := credits.FormatMicroCredits(micro)
	perRequest := ""
	if unit == BillingUnitRequest {
		perUnit = ""
		perRequest = credits.FormatMicroCredits(micro)
	}
	return store.UsagePriceSnapshot{
		Unit: unit, PerUnit: perUnit, PerRequest: perRequest, Quantity: quantity,
		InputPerMtok: "0.000000", OutputPerMtok: "0.000000",
		CacheReadPerMtok: "0.000000", CacheWritePerMtok: "0.000000",
		Multiplier: "1.000000", Currency: "CNY",
	}
}
