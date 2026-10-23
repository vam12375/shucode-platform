package apiapp

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/pricing"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestUsageAndOpsCostSummaryAreRootOnly(t *testing.T) {
	f := newAPIFixture(t)
	adminCookie := f.loginAdmin()
	memberLogin := f.do("POST", "/console/api/auth/login", "",
		`{"email":"member@mendao.chat","password":"mendao-member"}`)
	if memberLogin.Code != 200 {
		t.Fatalf("成员登录失败: %d %s", memberLogin.Code, memberLogin.Body.String())
	}
	memberCookie := sessionCookie(memberLogin)
	ctx := context.Background()
	member, err := f.store.FindUserByEmail(ctx, "member@mendao.chat")
	if err != nil || member == nil {
		t.Fatal("演示成员不存在")
	}
	memberships, err := f.store.ListTeamsForUser(ctx, member.ID)
	if err != nil || len(memberships) == 0 {
		t.Fatalf("成员没有有效团队: %v", err)
	}
	estimated := int64(12345)
	if err := f.store.InsertUsage(ctx, store.UsageRecord{
		ID: uuid.NewString(), RequestID: "cost-" + uuid.NewString(),
		UserID: member.ID, TeamID: memberships[0].Team.ID,
		Model: "cost-model", UpstreamModel: "cost-model",
		InputTokens: 10, OutputTokens: 5, CreditsMicro: 50_000,
		Status: store.UsageStatusOk,
		PriceSnapshot: &store.UsagePricingSnapshot{
			SchemaVersion: 1, CapturedAt: time.Now().UTC(),
			Cost: store.UsageCostSnapshot{
				Status: "estimated", EstimatedMicro: &estimated,
				Rates: &store.UsagePriceSnapshot{Currency: "CNY"},
			},
		},
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	adminUsage := dataOf(t, f.do("GET", "/console/api/usage", adminCookie, ""))
	costSummary, ok := adminUsage["costSummary"].(map[string]any)
	if !ok {
		t.Fatalf("root 用量应返回成本汇总: %v", adminUsage)
	}
	if costSummary["estimatedRequests"].(float64) < 1 ||
		costSummary["totals"].([]any)[0].(map[string]any)["currency"] != "CNY" {
		t.Fatalf("成本汇总不符: %v", costSummary)
	}

	memberUsage := dataOf(t, f.do("GET", "/console/api/usage", memberCookie, ""))
	if memberUsage["costSummary"] != nil {
		t.Fatalf("非 root 不得看到采购成本: %v", memberUsage)
	}

	overview := dataOf(t, f.do("GET", "/console/api/ops/overview?hours=24", adminCookie, ""))
	if overview["costSummary"] == nil {
		t.Fatalf("运维大盘应返回窗口成本: %v", overview)
	}
}

func TestReferenceAnalysisUsageSeparatesFixedChargeAndFrozenModelCost(t *testing.T) {
	f := newTenantFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 23, 1, 2, 3, 0, time.UTC)
	estimated := int64(10_028)
	snapshot := &store.UsagePricingSnapshot{
		SchemaVersion: 1, CapturedAt: at,
		Sale: store.UsagePriceSnapshot{InputPerMtok: "0", OutputPerMtok: "0", CacheReadPerMtok: "0", CacheWritePerMtok: "0", Multiplier: "1", Currency: "CNY"},
		Cost: store.UsageCostSnapshot{
			Status: "estimated", PricingVersion: "frozen-version", ModelID: "deepseek-frozen-upstream", EstimatedMicro: &estimated,
			Rates: &store.UsagePriceSnapshot{InputPerMtok: "2", OutputPerMtok: "8", CacheReadPerMtok: "0.04", CacheWritePerMtok: "0", Multiplier: "0.5", Currency: "CNY"},
		},
	}
	modelUsage := store.UsageRecord{
		ID: uuid.NewString(), RequestID: "model-" + uuid.NewString(), TeamID: f.team.ID, UserID: f.member.ID,
		Model: "deepseek-fixture", UpstreamModel: "deepseek-frozen-upstream", ProviderID: uuid.NewString(), ClientIP: "192.0.2.44",
		InputTokens: 6280, OutputTokens: 937, CacheReadTokens: 10, ReasoningEffort: "high", CreditsMicro: 0,
		PriceSnapshot: snapshot, Status: store.UsageStatusOk, CreatedAt: at,
	}
	fixedUsage := store.UsageRecord{
		ID: uuid.NewString(), RequestID: "fixed-" + uuid.NewString(), TeamID: f.team.ID, UserID: f.member.ID,
		Model: "media:reference-analysis", UpstreamModel: "media:reference-analysis", CreditsMicro: 1_000_000,
		Status: store.UsageStatusOk, CreatedAt: at.Add(time.Second),
	}
	for _, record := range []store.UsageRecord{modelUsage, fixedUsage} {
		if err := f.store.InsertUsage(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	// 用户实扣为零不能抹掉采购成本；改动当前价格库也不能重算已冻结的历史快照。
	if _, err := f.store.SaveModelPricing(ctx, []store.PricingEntry{{
		ModelID: modelUsage.UpstreamModel, DisplayName: "current fixture price", InputCostPerMillion: "99", OutputCostPerMillion: "199",
		CacheReadCostPerMillion: "9", CacheCreationCostPerMillion: "19", Currency: "USD",
	}}, pricing.EmptyVersion); err != nil {
		t.Fatal(err)
	}

	rootUsage := dataOf(t, f.do("GET", "/console/api/usage", f.rootCookie, ""))
	rows := rootUsage["records"].([]any)
	if len(rows) != 2 {
		t.Fatalf("应分别返回模型请求与固定分析费: %+v", rows)
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
		switch row.ID {
		case modelUsage.ID:
			if row.CreditsMicro != 0 || row.InputTokens != 6280 || row.OutputTokens != 937 || row.CacheReadTokens != 10 ||
				row.UpstreamModel != modelUsage.UpstreamModel || row.ProviderID == nil || *row.ProviderID != modelUsage.ProviderID ||
				row.ClientIP == nil || *row.ClientIP != modelUsage.ClientIP || !reflect.DeepEqual(row.PriceSnapshot, snapshot) {
				t.Fatalf("root 模型用量必须保留独立实扣与完整冻结采购快照: %+v", row)
			}
		case fixedUsage.ID:
			if row.Model != "media:reference-analysis" || row.CreditsMicro != 1_000_000 || row.PriceSnapshot != nil || row.InputTokens != 0 || row.OutputTokens != 0 {
				t.Fatalf("固定分析费不能复制模型采购成本或修改实扣: %+v", row)
			}
		default:
			t.Fatalf("出现未知用量: %+v", row)
		}
	}
	costSummary := rootUsage["costSummary"].(map[string]any)
	totals := costSummary["totals"].([]any)
	if costSummary["estimatedRequests"] != float64(1) || len(totals) != 1 ||
		totals[0].(map[string]any)["currency"] != "CNY" || totals[0].(map[string]any)["costMicro"] != float64(estimated) {
		t.Fatalf("root 成本汇总应采用模型请求的冻结采购成本: %+v", costSummary)
	}

	memberUsage := dataOf(t, f.do("GET", "/console/api/usage", f.memberCookie, ""))
	memberRows := memberUsage["records"].([]any)
	if len(memberRows) != 2 || memberUsage["costSummary"] != nil {
		t.Fatalf("成员应保留自己的两笔用户账单且不得看到成本汇总: %+v", memberUsage)
	}
	var charged float64
	for _, raw := range memberRows {
		row := raw.(map[string]any)
		charged += row["creditsMicro"].(float64)
		for _, key := range []string{"priceSnapshot", "upstreamModel", "providerId", "clientIp"} {
			if _, exists := row[key]; exists {
				t.Fatalf("成员用量不得泄露采购或路由字段 %s: %+v", key, row)
			}
		}
	}
	if charged != 1_000_000 {
		t.Fatalf("用户实扣应仅包含一笔固定分析费: %v", charged)
	}
}
