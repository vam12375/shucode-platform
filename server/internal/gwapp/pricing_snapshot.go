package gwapp

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/credits"
	"github.com/vam12375/shucode-platform/server/internal/pricing"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

type usagePricingCapture struct {
	at           time.Time
	modelID      string
	sale         store.UsagePriceSnapshot
	doc          *store.PricingDocSnapshot
	unknownCause string
	holidays     map[string]bool
}

func captureUsagePricing(ctx context.Context, deps Deps, model *store.PlatformModelRecord, at time.Time) usagePricingCapture {
	multiplier := parseSnapshotMultiplier(model.OffPeakMultiplier)
	factor := pricing.OffPeakMultiplierAt(at, multiplier, deps.Env.PeakHoursHolidays)
	capture := usagePricingCapture{
		at: at, modelID: model.ModelID, holidays: deps.Env.PeakHoursHolidays,
		sale: store.UsagePriceSnapshot{
			InputPerMtok: model.InputPricePerMtok, OutputPerMtok: model.OutputPricePerMtok,
			CacheReadPerMtok: model.CacheReadPricePerMtok, CacheWritePerMtok: model.CacheWritePricePerMtok,
			Multiplier: strconv.FormatFloat(factor, 'f', -1, 64), Currency: "CNY",
		},
	}
	// 成本库只是参考元数据；读取失败不改变额度执行，也不能把未知成本记成零。
	doc, err := deps.Store.GetModelPricing(ctx)
	if err != nil {
		deps.Logger.Warn("参考成本快照读取失败", "error", err)
		capture.unknownCause = "pricing_unavailable"
	} else {
		capture.doc = doc
		if doc == nil {
			capture.unknownCause = "not_configured"
		}
	}
	return capture
}

func parseSnapshotMultiplier(raw string) float64 {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 1
	}
	return value
}

func (capture usagePricingCapture) snapshot(target upstreamTarget, usage UsageCounts, status store.UsageStatus, errorType string) *store.UsagePricingSnapshot {
	snapshot := &store.UsagePricingSnapshot{
		SchemaVersion: 1, CapturedAt: capture.at, Sale: capture.sale,
		Cost: store.UsageCostSnapshot{Status: "unknown", Reason: capture.unknownCause},
	}
	if capture.doc == nil {
		return snapshot
	}
	snapshot.Cost.PricingVersion = capture.doc.Version
	var entry *store.PricingEntry
	for _, modelID := range []string{target.UpstreamModel, capture.modelID} {
		for i := range capture.doc.Entries {
			if capture.doc.Entries[i].ModelID == modelID {
				entry = &capture.doc.Entries[i]
				break
			}
		}
		if entry != nil {
			break
		}
	}
	if entry == nil {
		snapshot.Cost.Reason = "model_not_configured"
		return snapshot
	}
	snapshot.Cost.ModelID = entry.ModelID
	currency := strings.ToUpper(strings.TrimSpace(entry.Currency))
	if currency == "" {
		currency = "USD" // 成本库旧条目以 USD 为约定，不能与 CNY 销售额直接相减。
	}
	if currency != "CNY" && currency != "USD" {
		snapshot.Cost.Reason = "unsupported_currency"
		return snapshot
	}
	values := []string{entry.InputCostPerMillion, entry.OutputCostPerMillion, entry.CacheReadCostPerMillion, entry.CacheCreationCostPerMillion}
	parsed := make([]float64, len(values))
	for i, raw := range values {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			snapshot.Cost.Reason = "invalid_price"
			return snapshot
		}
		parsed[i] = value
	}
	multiplier := 1.0
	if entry.OffPeakMultiplier != "" {
		var err error
		multiplier, err = strconv.ParseFloat(entry.OffPeakMultiplier, 64)
		if err != nil || math.IsNaN(multiplier) || math.IsInf(multiplier, 0) || multiplier <= 0 {
			snapshot.Cost.Reason = "invalid_price"
			return snapshot
		}
	}
	factor := pricing.OffPeakMultiplierAt(capture.at, multiplier, capture.holidays)
	snapshot.Cost.Rates = &store.UsagePriceSnapshot{
		InputPerMtok: values[0], OutputPerMtok: values[1], CacheReadPerMtok: values[2], CacheWritePerMtok: values[3],
		Multiplier: strconv.FormatFloat(factor, 'f', -1, 64), Currency: currency,
	}
	// 错误/断连且未取得用量时，零 token 并不证明供应商成本为零。
	if status != store.UsageStatusOk && usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.CacheReadTokens == 0 && usage.CacheCreationTokens == 0 {
		snapshot.Cost.Reason = "usage_unavailable"
		return snapshot
	}
	micro, err := credits.ComputeUsageMicroCredits(credits.UsageTokenCounts{
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		CacheReadTokens: usage.CacheReadTokens, CacheCreationTokens: usage.CacheCreationTokens,
	}, credits.ModelPriceRates{
		InputPerMtok: parsed[0] * factor, OutputPerMtok: parsed[1] * factor,
		CacheReadPerMtok: parsed[2] * factor, CacheWritePerMtok: parsed[3] * factor,
	})
	if err != nil || micro < 0 {
		snapshot.Cost.Reason = "invalid_usage"
		return snapshot
	}
	snapshot.Cost.Status, snapshot.Cost.Reason = "estimated", ""
	snapshot.Cost.EstimatedMicro = &micro
	snapshot.Cost.UsageEstimated = strings.Contains(errorType, "usage_missing")
	return snapshot
}
