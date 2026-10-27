package pricing

import (
	"encoding/json"
	"os"
	"testing"
)

type fixturesFile struct {
	PricingVersions []struct {
		Entries []struct {
			ModelID                     string `json:"modelId"`
			DisplayName                 string `json:"displayName"`
			InputCostPerMillion         string `json:"inputCostPerMillion"`
			OutputCostPerMillion        string `json:"outputCostPerMillion"`
			CacheReadCostPerMillion     string `json:"cacheReadCostPerMillion"`
			CacheCreationCostPerMillion string `json:"cacheCreationCostPerMillion"`
		} `json:"entries"`
		Version string `json:"version"`
	} `json:"pricingVersions"`
	Username string `json:"-"`
}

func loadFixtures(t *testing.T) fixturesFile {
	t.Helper()
	raw, err := os.ReadFile("../contracts/testdata/fixtures.json")
	if err != nil {
		t.Fatalf("读取契约 fixtures: %v", err)
	}
	var f fixturesFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("解析 fixtures: %v", err)
	}
	return f
}

// 版本哈希必须与 TS 逐字节一致——它同时承担控制台定价库乐观锁,漂移会误报 409。
func TestPricingVersionFixtures(t *testing.T) {
	for i, tc := range loadFixtures(t).PricingVersions {
		entries := make([]PricingEntry, 0, len(tc.Entries))
		for _, e := range tc.Entries {
			entries = append(entries, PricingEntry{
				ModelID:                     e.ModelID,
				DisplayName:                 e.DisplayName,
				InputCostPerMillion:         e.InputCostPerMillion,
				OutputCostPerMillion:        e.OutputCostPerMillion,
				CacheReadCostPerMillion:     e.CacheReadCostPerMillion,
				CacheCreationCostPerMillion: e.CacheCreationCostPerMillion,
			})
		}
		if got := ComputeDocVersion(entries); got != tc.Version {
			t.Fatalf("样本 %d 版本哈希不符:\n got %s\nwant %s", i, got, tc.Version)
		}
	}
	if EmptyVersion != loadFixtures(t).PricingVersions[0].Version {
		t.Fatalf("EmptyVersion 与空文档版本不一致: %s", EmptyVersion)
	}
}

// KV 形状的文档解析:未知字段保留、无效条目剔除(与 parsePricingDoc 一致)。
func TestParseDoc(t *testing.T) {
	value := map[string]any{
		"entries": []any{
			map[string]any{
				"modelId": "m-1", "displayName": "M1",
				"inputCostPerMillion": "1", "outputCostPerMillion": "2",
				"cacheReadCostPerMillion": "0.1", "cacheCreationCostPerMillion": "1",
				"futureField": "保留 ✓",
			},
			"not-an-entry",
			13,
		},
	}
	snapshot := ParseDoc(value)
	if snapshot == nil {
		t.Fatal("应解析成功")
	}
	if len(snapshot.Entries) != 1 {
		t.Fatalf("应只留 1 条合法条目: %d", len(snapshot.Entries))
	}
	if snapshot.Entries[0].Extra["futureField"] != "保留 ✓" {
		t.Fatal("未知字段应保留")
	}
	version, fixture := ComputeDocVersion(snapshot.Entries), loadFixtures(t)
	_ = fixture
	if version == "" || len(version) != len("prc_")+40 {
		t.Fatalf("版本形态不符: %s", version)
	}
	for _, bad := range []any{nil, "text", 42, map[string]any{}, map[string]any{"entries": "no"}} {
		if ParseDoc(bad) != nil {
			t.Fatalf("应视为不存在: %v", bad)
		}
	}
}

// 新增条目按模型 ID 首字母归位，大小写无关且不影响内容版本。
func TestSortEntriesByModelID(t *testing.T) {
	entries := []PricingEntry{
		{ModelID: "qwen3.8-omni-flash", DisplayName: "Qwen3.8 Omni"},
		{ModelID: "glm-5-3-flash-260828", DisplayName: "GLM 5.3 Flash"},
		{ModelID: "DeepSeek-v4.1-flash-1017", DisplayName: "DeepSeek V4.1"},
		{ModelID: "auto-price-model", DisplayName: "Auto Model"},
	}
	versionBefore := ComputeDocVersion(entries)
	SortEntries(entries)
	want := []string{"auto-price-model", "DeepSeek-v4.1-flash-1017", "glm-5-3-flash-260828", "qwen3.8-omni-flash"}
	for i, entry := range entries {
		if entry.ModelID != want[i] {
			t.Fatalf("第 %d 条应为 %s，实际 %s（完整顺序: %+v）", i, want[i], entry.ModelID, entries)
		}
	}
	if versionAfter := ComputeDocVersion(entries); versionAfter != versionBefore {
		t.Fatalf("排序不应改变内容版本: %s -> %s", versionBefore, versionAfter)
	}
}
