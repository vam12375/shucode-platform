package apiapp

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestMediaUsageHistoricalCostSnapshotsAcrossConsoleSurfaces(t *testing.T) {
	f := newTenantFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 25, 1, 2, 3, 0, time.UTC)
	providerID := uuid.NewString()
	models := []media.Model{
		{ID: "frozen-ark-image", Name: "图片甲", DisplayName: "图片甲", Kind: "image", Adapter: "ark", UpstreamModel: "private-image-ark", ProviderID: providerID, PriceMicro: 300_000, CostMicro: 100_000, PriceVersion: "frozen-media-v1"},
		{ID: "frozen-toapis-image", Name: "图片乙", DisplayName: "图片乙", Kind: "image", Adapter: "toapis", UpstreamModel: "gpt-image-2", ProviderID: providerID, Resolutions: []string{"2K"}, PriceMicro: 600_000, CostMicro: 250_000, PriceVersion: "frozen-media-v1"},
		{ID: "frozen-second-video", Name: "视频甲", DisplayName: "视频甲", Kind: "video", Adapter: "toapis", UpstreamModel: "seedance-2-mini", ProviderID: providerID, Resolutions: []string{"720p"}, MinDuration: 4, MaxDuration: 15, PriceMicro: 200_000, CostMicro: 50_000, PriceVersion: "frozen-media-v1"},
		{ID: "frozen-token-video", Name: "视频乙", DisplayName: "视频乙", Kind: "video", Adapter: "ark", UpstreamModel: "private-video-ark", ProviderID: providerID, Resolutions: []string{"720p"}, MinDuration: 4, MaxDuration: 15, PriceMicro: 200_000, CostMicro: 50_000, PriceVersion: "frozen-media-v1"},
	}
	saveCatalog := func() {
		t.Helper()
		raw, err := json.Marshal(models)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.SetConfigValue(ctx, media.ConfigKey, raw); err != nil {
			t.Fatal(err)
		}
	}
	saveCatalog()
	rates := []store.UsagePriceSnapshot{
		{Unit: "image", PerUnit: "0.300000", Quantity: 1, Multiplier: "1.000000", Currency: "CNY"},
		{Unit: "image", PerUnit: "0.600000", Quantity: 1, Multiplier: "1.000000", Currency: "CNY"},
		{Unit: "second", PerUnit: "0.200000", Quantity: 5, Multiplier: "1.000000", Currency: "CNY"},
		{InputPerMtok: "0.000000", OutputPerMtok: "40.000000", CacheReadPerMtok: "0.000000", CacheWritePerMtok: "0.000000", Multiplier: "1.000000", Currency: "CNY"},
	}
	costRates := []string{"0.100000", "0.250000", "0.050000", "28.000000"}
	costs := []int64{100_000, 250_000, 250_000, 2_800_000}
	charges := []int64{300_000, 600_000, 1_000_000, 4_000_000}
	records := make([]store.UsageRecord, 0, len(models)+1)
	for i, model := range models {
		costRate := rates[i]
		if costRate.Unit == "" {
			costRate.OutputPerMtok = costRates[i]
		} else {
			costRate.PerUnit = costRates[i]
		}
		cost := costs[i]
		record := store.UsageRecord{
			ID: uuid.NewString(), RequestID: "media-frozen-" + uuid.NewString(),
			UserID: f.member.ID, TeamID: f.team.ID,
			Model: "media:" + model.ID, UpstreamModel: model.UpstreamModel, ProviderID: providerID,
			CreditsMicro: charges[i], Status: store.UsageStatusOk, CreatedAt: at.Add(time.Duration(i) * time.Second),
			PriceSnapshot: &store.UsagePricingSnapshot{
				SchemaVersion: 1, CapturedAt: at, Sale: rates[i],
				Cost: store.UsageCostSnapshot{
					Status: "estimated", PricingVersion: model.PriceVersion, ModelID: model.ID,
					Rates: &costRate, EstimatedMicro: &cost,
				},
			},
		}
		if costRate.Unit == "" {
			record.OutputTokens = 100_000
		}
		records = append(records, record)
	}
	legacy := records[0]
	legacy.ID, legacy.RequestID = uuid.NewString(), "media-legacy-"+uuid.NewString()
	legacy.PriceSnapshot = nil
	legacy.CreatedAt = at.Add(4 * time.Second)
	records = append(records, legacy)
	for _, record := range records {
		if err := f.store.InsertUsage(ctx, record); err != nil {
			t.Fatal(err)
		}
	}

	query := "/console/api/usage?teamId=" + f.team.ID + "&from=2026-10-25T00:00:00&to=2026-10-26T00:00:00"
	assertRootRows := func(path string, expected []store.UsageRecord) map[string]any {
		t.Helper()
		response := f.do("GET", path, f.rootCookie, "")
		if response.Code != 200 {
			t.Fatalf("root 用量读取失败: %d %s", response.Code, response.Body.String())
		}
		data := dataOf(t, response)
		rows := data["records"].([]any)
		if len(rows) != len(expected) {
			t.Fatalf("用量数量不符: got %d, want %d", len(rows), len(expected))
		}
		byID := make(map[string]store.UsageRecord, len(expected))
		for _, record := range expected {
			byID[record.ID] = record
		}
		for _, raw := range rows {
			encoded, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			var row usagePublic
			if err := json.Unmarshal(encoded, &row); err != nil {
				t.Fatal(err)
			}
			want, ok := byID[row.ID]
			if !ok || row.Model != want.Model || row.CreditsMicro != want.CreditsMicro || row.OutputTokens != want.OutputTokens ||
				row.ProviderID == nil || *row.ProviderID != providerID || !reflect.DeepEqual(row.PriceSnapshot, want.PriceSnapshot) {
				t.Fatalf("root 列表及详情载荷必须保留冻结媒体价格与独立实扣: %+v", row)
			}
		}
		return data
	}
	assertSummary := func(data map[string]any) {
		t.Helper()
		summary := data["costSummary"].(map[string]any)
		totals := summary["totals"].([]any)
		if summary["estimatedRequests"] != float64(4) || summary["missingCostRequests"] != float64(1) || len(totals) != 1 {
			t.Fatalf("媒体成本快照与历史缺失计数不符: %+v", summary)
		}
		total := totals[0].(map[string]any)
		if total["currency"] != "CNY" || total["costMicro"] != float64(3_400_000) || total["chargedMicro"] != float64(5_900_000) || total["requests"] != float64(4) {
			t.Fatalf("区间成本应汇总冻结金额并排除缺失快照: %+v", total)
		}
	}
	assertSummary(assertRootRows(query, records))

	// 根因：媒体用量曾漏写快照；读取历史时不可用当前目录补价，也不能把旧空值视为零成本。
	for i := range models {
		models[i].CostMicro = 9_000_000
		models[i].PriceMicro = 10_000_000
		models[i].PriceVersion = "current-media-v2"
	}
	saveCatalog()
	assertSummary(assertRootRows(query, records))
	// 详情弹窗直接消费列表行；按 requestId 单独读取也必须返回同一冻结快照。
	for _, record := range records {
		assertRootRows(query+"&requestId="+record.RequestID, []store.UsageRecord{record})
	}
	page := dataOf(t, f.do("GET", query+"&limit=1", f.rootCookie, ""))
	if len(page["records"].([]any)) != 1 {
		t.Fatal("分页夹具应只返回一条记录")
	}
	assertSummary(page)

	for _, cookie := range []string{f.adminCookie, f.memberCookie} {
		data := dataOf(t, f.do("GET", query, cookie, ""))
		rows := data["records"].([]any)
		if len(rows) != len(records) || data["costSummary"] != nil {
			t.Fatalf("非 root 应保留用户账单但不得看到采购汇总: %+v", data)
		}
		for _, raw := range rows {
			row := raw.(map[string]any)
			for _, field := range []string{"priceSnapshot", "upstreamModel", "providerId"} {
				if _, exists := row[field]; exists {
					t.Fatalf("非 root 用量及详情不得泄露媒体采购字段 %s: %+v", field, row)
				}
			}
		}
		for _, path := range []string{"/console/api/usage/export", "/console/api/teams/" + f.team.ID} {
			response := f.do("GET", path, cookie, "")
			if response.Code != 200 {
				t.Fatalf("非 root 用量读取失败: %s %d %s", path, response.Code, response.Body.String())
			}
			for _, secret := range []string{"priceSnapshot", "frozen-media-v1", "current-media-v2", providerID} {
				if strings.Contains(response.Body.String(), secret) {
					t.Fatalf("非 root %s 泄露采购证据 %s", path, secret)
				}
			}
		}
	}
}
