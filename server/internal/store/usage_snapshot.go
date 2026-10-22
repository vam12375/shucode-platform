package store

import "time"

// UsagePricingSnapshot 固定请求发生时的价格；历史空值不能用当前价格回填。
// CreditsMicro 始终是实际结算额，采购成本只是未与供应商账单核对的参考估算。
type UsagePricingSnapshot struct {
	SchemaVersion int                `json:"schemaVersion"`
	CapturedAt    time.Time          `json:"capturedAt"`
	Sale          UsagePriceSnapshot `json:"sale"`
	Cost          UsageCostSnapshot  `json:"cost"`
}

type UsagePriceSnapshot struct {
	InputPerMtok      string `json:"inputPerMtok"`
	OutputPerMtok     string `json:"outputPerMtok"`
	CacheReadPerMtok  string `json:"cacheReadPerMtok"`
	CacheWritePerMtok string `json:"cacheWritePerMtok"`
	Multiplier        string `json:"multiplier"`
	Currency          string `json:"currency"`
}

type UsageCostSnapshot struct {
	Status         string              `json:"status"` // estimated / unknown
	PricingVersion string              `json:"pricingVersion,omitempty"`
	ModelID        string              `json:"modelId,omitempty"`
	Rates          *UsagePriceSnapshot `json:"rates,omitempty"`
	EstimatedMicro *int64              `json:"estimatedMicro,omitempty"`
	UsageEstimated bool                `json:"usageEstimated,omitempty"`
	Reason         string              `json:"reason,omitempty"`
}

func cloneUsage(record UsageRecord) UsageRecord {
	if record.ReasoningBudgetTokens != nil {
		budget := *record.ReasoningBudgetTokens
		record.ReasoningBudgetTokens = &budget
	}
	if record.LatencyMs != nil {
		latency := *record.LatencyMs
		record.LatencyMs = &latency
	}
	if record.PriceSnapshot != nil {
		snapshot := *record.PriceSnapshot
		if snapshot.Cost.EstimatedMicro != nil {
			micro := *snapshot.Cost.EstimatedMicro
			snapshot.Cost.EstimatedMicro = &micro
		}
		if snapshot.Cost.Rates != nil {
			rates := *snapshot.Cost.Rates
			snapshot.Cost.Rates = &rates
		}
		record.PriceSnapshot = &snapshot
	}
	return record
}
