package apiapp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestUsageReasoningUnknownRemainsNullable(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	ctx := context.Background()
	admin, _ := f.store.FindUserByEmail(ctx, "admin@mendao.chat")
	teams, _ := f.store.ListTeams(ctx)
	for i, model := range []string{"historical-model", "media:source-analysis"} {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", 910+i)
		if err := f.store.InsertUsage(ctx, store.UsageRecord{ID: id, RequestID: id, TeamID: teams[0].Team.ID, UserID: admin.ID, Model: model, Status: store.UsageStatusOk, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		response := f.do("GET", "/console/api/usage?requestId="+id, cookie, "")
		records := dataOf(t, response)["records"].([]any)
		if len(records) != 1 {
			t.Fatal("request missing", response.Body.String())
		}
		row := records[0].(map[string]any)
		for _, field := range []string{"reasoningEffort", "reasoningBudgetTokens"} {
			value, exists := row[field]
			if !exists || value != nil {
				t.Fatalf("unknown %s must remain explicit null: %+v", field, row)
			}
		}
	}
}

func TestUsageExportRequestIDAndOps(t *testing.T) {
	f := newAPIFixture(t)
	adminCookie := f.loginAdmin()
	ctx := context.Background()
	admin, err := f.store.FindUserByEmail(ctx, "admin@mendao.chat")
	if err != nil || admin == nil {
		t.Fatalf("管理员不存在: %v", err)
	}
	teams, err := f.store.ListTeams(ctx)
	if err != nil || len(teams) == 0 {
		t.Fatalf("演示团队不存在: %v", err)
	}
	team := teams[0].Team
	requestID := "api-ops-" + time.Now().UTC().Format("150405.000000")
	latency := int64(123)
	budget := int64(8192)
	if err := f.store.InsertUsage(ctx, store.UsageRecord{
		ID: "00000000-0000-4000-8000-000000000901", RequestID: requestID,
		UserID: admin.ID, TeamID: team.ID, Model: "claude-sonnet-4-5", UpstreamModel: "claude-sonnet-4-5",
		InputTokens: 10, OutputTokens: 5, CreditsMicro: 123_456, Status: store.UsageStatusOk,
		LatencyMs: &latency, CreatedAt: time.Now().UTC(),
		ReasoningEffort: "max", ReasoningBudgetTokens: &budget,
	}); err != nil {
		t.Fatal(err)
	}

	// requestId 过滤。
	usageRec := f.do("GET", "/console/api/usage?requestId="+requestID, adminCookie, "")
	if usageRec.Code != 200 {
		t.Fatalf("按 requestId 查询失败: %d %s", usageRec.Code, usageRec.Body.String())
	}
	records := dataOf(t, usageRec)["records"].([]any)
	if len(records) != 1 {
		t.Fatalf("requestId 过滤应命中 1 条: %d", len(records))
	}
	row := records[0].(map[string]any)
	if row["reasoningEffort"] != "max" || row["reasoningBudgetTokens"] != float64(8192) {
		t.Fatalf("actual request reasoning missing in root list: %+v", row)
	}

	// CSV 导出（含 UTF-8 BOM 与 request_id）。
	exportRec := f.do("GET", "/console/api/usage/export?requestId="+requestID, adminCookie, "")
	if exportRec.Code != 200 || !strings.Contains(exportRec.Header().Get("content-type"), "text/csv") {
		t.Fatalf("导出响应不符: %d %s", exportRec.Code, exportRec.Header().Get("content-type"))
	}
	body := exportRec.Body.String()
	if !strings.HasPrefix(body, "\ufeff") || !strings.Contains(body, requestID) || !strings.Contains(body, "request_id") || !strings.Contains(body, "思考强度") || !strings.Contains(body, ",max,8192,") {
		t.Fatalf("CSV 内容不符: %s", body)
	}

	// ops 概览。
	overviewRec := f.do("GET", "/console/api/ops/overview?hours=1", adminCookie, "")
	if overviewRec.Code != 200 {
		t.Fatalf("ops overview 失败: %d %s", overviewRec.Code, overviewRec.Body.String())
	}
	overview := dataOf(t, overviewRec)
	if overview["requests"].(float64) < 1 || overview["creditsMicro"].(float64) < 123_456 {
		t.Fatalf("ops overview 指标不符: %v", overview)
	}

	// 告警规则创建与列表。
	ruleRec := f.do("POST", "/console/api/ops/alert-rules", adminCookie,
		`{"name":"错误率-API测试","metric":"error_rate","threshold":0.1,"windowMinutes":15,"enabled":true}`)
	if ruleRec.Code != 200 {
		t.Fatalf("创建告警规则失败: %d %s", ruleRec.Code, ruleRec.Body.String())
	}
	rulesRec := f.do("GET", "/console/api/ops/alert-rules", adminCookie, "")
	if rulesRec.Code != 200 || len(dataOf(t, rulesRec)["rules"].([]any)) != 1 {
		t.Fatalf("告警规则列表不符: %d %s", rulesRec.Code, rulesRec.Body.String())
	}

	// 团队流控字段写入并回读。
	patchRec := f.do("PATCH", "/console/api/teams/"+team.ID, adminCookie,
		`{"rpmLimit":60,"tpmLimit":120000,"concurrencyLimit":4}`)
	if patchRec.Code != 200 {
		t.Fatalf("更新团队流控失败: %d %s", patchRec.Code, patchRec.Body.String())
	}
	teamData := dataOf(t, patchRec)["team"].(map[string]any)
	if teamData["rpmLimit"].(float64) != 60 || teamData["tpmLimit"].(float64) != 120000 ||
		teamData["concurrencyLimit"].(float64) != 4 {
		t.Fatalf("团队流控回读不符: %v", teamData)
	}
}

func TestOpsAlertRuleValidation(t *testing.T) {
	f := newAPIFixture(t)
	adminCookie := f.loginAdmin()
	badMetric := f.do("POST", "/console/api/ops/alert-rules", adminCookie,
		`{"name":"bad","metric":"unknown","threshold":1}`)
	if badMetric.Code != 400 {
		t.Fatalf("未知指标应 400: %d %s", badMetric.Code, badMetric.Body.String())
	}
	badThreshold := f.do("POST", "/console/api/ops/alert-rules", adminCookie,
		`{"name":"bad","metric":"error_rate","threshold":-1}`)
	if badThreshold.Code != 400 {
		t.Fatalf("负阈值应 400: %d %s", badThreshold.Code, badThreshold.Body.String())
	}
}
