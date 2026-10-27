// Package pricing:packages/db/src/pricing-doc.ts 的镜像。
// 版本是内容寻址的乐观锁依据:version = prc_<sha256 前 40 位>(按 modelId 排序后的
// 六字段稳定序列化)。此哈希必须与 TS 逐字节一致,否则控制台首次保存定价会误报 409。
package pricing

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/vam12375/shucode-platform/server/internal/contracts"
)

const ModelPricingKVKey = "model_pricing_v1"

// PricingEntry 字段名与 TS ModelPricingEntry 一致;ExtraKeys 保留未知字段
// (TS parsePricingDoc 是透传的 entries 对象数组,只有 6 个字段的子集参与版本计算)。
type PricingEntry struct {
	ModelID                     string `json:"modelId"`
	DisplayName                 string `json:"displayName"`
	InputCostPerMillion         string `json:"inputCostPerMillion"`
	OutputCostPerMillion        string `json:"outputCostPerMillion"`
	CacheReadCostPerMillion     string `json:"cacheReadCostPerMillion"`
	CacheCreationCostPerMillion string `json:"cacheCreationCostPerMillion"`
	// OffPeakMultiplier:空闲时段相对高峰价的倍率（DeepSeek 为 0.5）；空/1 = 无空闲折扣。
	OffPeakMultiplier string `json:"offPeakMultiplier,omitempty"`
	// Currency:成本币种（CNY/USD）；空 = 历史 USD 参考。
	Currency string         `json:"currency,omitempty"`
	Extra    map[string]any `json:"-"`
}

func (e PricingEntry) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, 8+len(e.Extra))
	for _, kv := range []struct {
		k string
		v string
	}{
		{"modelId", e.ModelID},
		{"displayName", e.DisplayName},
		{"inputCostPerMillion", e.InputCostPerMillion},
		{"outputCostPerMillion", e.OutputCostPerMillion},
		{"cacheReadCostPerMillion", e.CacheReadCostPerMillion},
		{"cacheCreationCostPerMillion", e.CacheCreationCostPerMillion},
	} {
		m[kv.k] = kv.v
	}
	if e.OffPeakMultiplier != "" {
		m["offPeakMultiplier"] = e.OffPeakMultiplier
	}
	if e.Currency != "" {
		m["currency"] = e.Currency
	}
	for k, v := range e.Extra {
		m[k] = v
	}
	return contracts.EncodeJSONCompact(m)
}

type Snapshot struct {
	Entries []PricingEntry `json:"entries"`
	Version string         `json:"version"`
}

func ComputeDocVersion(entries []PricingEntry) string {
	stable := make([]stableEntry, 0, len(entries))
	for _, e := range entries {
		stable = append(stable, stableEntry{
			modelID: e.ModelID,
			// 字段名与插入顺序必须与 TS 完全一致(modelId,displayName,input,output,cr,cw):
			// Go 结构体按声明顺序序列化,不能用 map(map 会被按 key 字典序重排导致哈希漂移)。
			value: stableEntryJSON{
				ModelID:     e.ModelID,
				DisplayName: e.DisplayName,
				Input:       e.InputCostPerMillion,
				Output:      e.OutputCostPerMillion,
				CR:          e.CacheReadCostPerMillion,
				CW:          e.CacheCreationCostPerMillion,
				OffPeak:     e.OffPeakMultiplier,
				Currency:    e.Currency,
			},
		})
	}
	// TS:sort((a,b) => a.modelId.localeCompare(b.modelId)) + Array.sort 稳定;
	// 近似:大小写无关比较、原串并列稳定(模型 ID 实际上是 ASCII 小写字母/数字/点/横线,
	// 该口径与 ICU 主权重完全一致;病态的纯标点差异 ID 不存在于真实目录)。
	sort.SliceStable(stable, func(i, j int) bool {
		return localeOrder(stable[i].modelID, stable[j].modelID)
	})
	encoded, err := contracts.EncodeJSONCompact(extractValues(stable))
	if err != nil {
		// 六字段均为字符串,序列化不可失败;防御性兜底。
		return "prc_invalid"
	}
	sum := sha256.Sum256(encoded)
	return "prc_" + fmt.Sprintf("%x", sum)[:40]
}

type stableEntry struct {
	modelID string
	value   stableEntryJSON
}

// stableEntryJSON 的 JSON 形态与 TS 的 stable 映射对象逐键一致。
type stableEntryJSON struct {
	ModelID     string `json:"modelId"`
	DisplayName string `json:"displayName"`
	Input       string `json:"input"`
	Output      string `json:"output"`
	CR          string `json:"cr"`
	CW          string `json:"cw"`
	// 追加字段用 omitempty：旧条目（无空闲倍率/币种）序列化结果不变，保持冻结 fixtures 的哈希。
	OffPeak  string `json:"offPeak,omitempty"`
	Currency string `json:"currency,omitempty"`
}

func extractValues(stable []stableEntry) []stableEntryJSON {
	out := make([]stableEntryJSON, 0, len(stable))
	for _, s := range stable {
		out = append(out, s.value)
	}
	return out
}

func localeOrder(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if la != lb {
		return la < lb
	}
	return a < b
}

// SortEntries 按模型 ID 首字母（大小写无关）就地排序，作为定价清单的统一展示顺序。
// 与 ComputeDocVersion 内部稳定排序使用同一比较口径，保证落库顺序与版本口径一致；
// 文档本身无序字段，排序不改变版本哈希。
func SortEntries(entries []PricingEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return localeOrder(entries[i].ModelID, entries[j].ModelID)
	})
}

// EmptyVersion 是 KV 从未写入时的乐观锁前置版本哨兵(与 TS EMPTY_PRICING_VERSION 同值)。
var EmptyVersion = ComputeDocVersion(nil)

// ParseDoc 镜像 parsePricingDoc:非对象/缺 entries 数组视为不存在(nil);
// entries 过滤为「modelId 与 displayName 均为字符串」的对象,保留未知字段。
func ParseDoc(value any) *Snapshot {
	if value == nil {
		return nil
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	rawEntries, ok := obj["entries"].([]any)
	if !ok {
		return nil
	}
	entries := make([]PricingEntry, 0, len(rawEntries))
	for _, raw := range rawEntries {
		entryObj, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		modelID, ok1 := entryObj["modelId"].(string)
		displayName, ok2 := entryObj["displayName"].(string)
		if !ok1 || !ok2 {
			continue
		}
		str := func(k string) string { s, _ := entryObj[k].(string); return s }
		entry := PricingEntry{
			ModelID:                     modelID,
			DisplayName:                 displayName,
			InputCostPerMillion:         str("inputCostPerMillion"),
			OutputCostPerMillion:        str("outputCostPerMillion"),
			CacheReadCostPerMillion:     str("cacheReadCostPerMillion"),
			CacheCreationCostPerMillion: str("cacheCreationCostPerMillion"),
			OffPeakMultiplier:           str("offPeakMultiplier"),
			Currency:                    str("currency"),
		}
		extra := make(map[string]any, len(entryObj)-2)
		for k, v := range entryObj {
			switch k {
			case "modelId", "displayName", "inputCostPerMillion", "outputCostPerMillion",
				"cacheReadCostPerMillion", "cacheCreationCostPerMillion",
				"offPeakMultiplier", "currency":
				continue
			default:
				extra[k] = v
			}
		}
		if len(extra) > 0 {
			entry.Extra = extra
		}
		entries = append(entries, entry)
	}
	return &Snapshot{Entries: entries, Version: ComputeDocVersion(entries)}
}
