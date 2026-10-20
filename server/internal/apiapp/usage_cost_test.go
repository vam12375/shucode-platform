package apiapp

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

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
