package apiapp

import "testing"

func TestNormalizePricingEntryPreservesCurrencyAndOffPeak(t *testing.T) {
	entry := normalizePricingEntry(map[string]any{
		"modelId": "deepseek-flash", "displayName": "DeepSeek Flash",
		"inputCostPerMillion": "2", "outputCostPerMillion": "8",
		"cacheReadCostPerMillion": "0.04", "cacheCreationCostPerMillion": "0",
		"offPeakMultiplier": "0.5", "currency": "CNY",
	})
	if entry == nil || entry.OffPeakMultiplier != "0.500000" || entry.Currency != "CNY" {
		t.Fatalf("成本价扩展字段丢失: %+v", entry)
	}
}
