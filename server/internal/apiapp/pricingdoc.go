package apiapp

import (
	"context"
	_ "embed"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/vam12375/shucode-platform/server/internal/pricing"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// model-pricing.ts(api 服务)的镜像:定价库的 KV 副本与内置默认资产。

//go:embed assets/default-model-pricing.json
var defaultPricingAsset []byte

// normalizePricingEntry:数字/字符串价格归一成 6 位小数字符串;非法或缺关键字段返回 nil。
func normalizePricingEntry(raw any) *pricing.PricingEntry {
	record, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	modelID := trimmedString(record["modelId"])
	displayName := trimmedString(record["displayName"])
	if modelID == "" || displayName == "" {
		return nil
	}
	price := func(value any) (string, bool) {
		var numeric float64
		switch typed := value.(type) {
		case float64:
			numeric = typed
		case string:
			parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
			if err != nil {
				return "", false
			}
			numeric = parsed
		default:
			return "", false
		}
		if numeric < 0 {
			return "", false
		}
		return strconv.FormatFloat(numeric, 'f', 6, 64), true
	}
	input, okInput := price(record["inputCostPerMillion"])
	output, okOutput := price(record["outputCostPerMillion"])
	if !okInput || !okOutput {
		return nil
	}
	cacheRead, okRead := price(record["cacheReadCostPerMillion"])
	if !okRead {
		cacheRead = "0.000000"
	}
	cacheCreation, okCreation := price(record["cacheCreationCostPerMillion"])
	if !okCreation {
		cacheCreation = "0.000000"
	}
	offPeak := ""
	if rawMultiplier, exists := record["offPeakMultiplier"]; exists && rawMultiplier != nil && rawMultiplier != "" {
		parsed, okMultiplier := price(rawMultiplier)
		if !okMultiplier {
			return nil
		}
		offPeak = parsed
	}
	currency, _ := trimmedOptional(record["currency"], 8)
	return &pricing.PricingEntry{
		ModelID: modelID, DisplayName: displayName,
		InputCostPerMillion: input, OutputCostPerMillion: output,
		CacheReadCostPerMillion: cacheRead, CacheCreationCostPerMillion: cacheCreation,
		OffPeakMultiplier: offPeak, Currency: currency,
	}
}

func trimmedString(value any) string {
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func loadDefaultPricingEntries() []pricing.PricingEntry {
	var doc struct {
		Models []any `json:"models"`
	}
	if err := json.Unmarshal(defaultPricingAsset, &doc); err != nil {
		return nil
	}
	entries := make([]pricing.PricingEntry, 0, len(doc.Models))
	for _, raw := range doc.Models {
		if entry := normalizePricingEntry(raw); entry != nil {
			entries = append(entries, *entry)
		}
	}
	pricing.SortEntries(entries)
	return entries
}

func normalizeStoredEntries(entries []pricing.PricingEntry) []pricing.PricingEntry {
	out := make([]pricing.PricingEntry, 0, len(entries))
	for _, entry := range entries {
		normalized := normalizePricingEntry(map[string]any{
			"modelId": entry.ModelID, "displayName": entry.DisplayName,
			"inputCostPerMillion":         entry.InputCostPerMillion,
			"outputCostPerMillion":        entry.OutputCostPerMillion,
			"cacheReadCostPerMillion":     entry.CacheReadCostPerMillion,
			"cacheCreationCostPerMillion": entry.CacheCreationCostPerMillion,
			"offPeakMultiplier":           entry.OffPeakMultiplier,
			"currency":                    entry.Currency,
		})
		if normalized != nil {
			out = append(out, *normalized)
		}
	}
	return out
}

// loadPricingDoc:KV 无副本(或为空/损坏)时用内置默认初始化;返回条目与内容版本。
// 并发初始化竞争时以库中现状为准(不覆盖他人写入)。
// 读取路径按模型 ID 首字母排序：历史文档中新追加的条目会立即回到正确位置。
func loadPricingDoc(ctx context.Context, st store.PlatformStore) (*pricing.Snapshot, error) {
	stored, err := st.GetModelPricing(ctx)
	if err != nil {
		return nil, err
	}
	if stored != nil {
		entries := normalizeStoredEntries(stored.Entries)
		if len(entries) > 0 {
			pricing.SortEntries(entries)
			return &pricing.Snapshot{Entries: entries, Version: pricing.ComputeDocVersion(entries)}, nil
		}
	}
	seeded := loadDefaultPricingEntries()
	saved, err := st.SaveModelPricing(ctx, seeded, pricing.EmptyVersion)
	if err != nil {
		return nil, err
	}
	finalEntries := seeded
	if saved == nil {
		current, err := st.GetModelPricing(ctx)
		if err != nil {
			return nil, err
		}
		if current != nil {
			finalEntries = normalizeStoredEntries(current.Entries)
			pricing.SortEntries(finalEntries)
		}
	}
	return &pricing.Snapshot{Entries: finalEntries, Version: pricing.ComputeDocVersion(finalEntries)}, nil
}

func loadPricingEntries(ctx context.Context, st store.PlatformStore) ([]pricing.PricingEntry, error) {
	doc, err := loadPricingDoc(ctx, st)
	if err != nil {
		return nil, err
	}
	return doc.Entries, nil
}
