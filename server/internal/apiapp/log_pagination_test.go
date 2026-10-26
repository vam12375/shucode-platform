package apiapp

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestLogCursorPreservesSubMillisecondPrecisionAndDisplayFormat(t *testing.T) {
	at := time.Date(2026, 10, 26, 1, 2, 3, 123456789, time.FixedZone("fixture", 8*60*60))
	id := "00000000-0000-4000-8000-000000009000"
	encoded := encodeUsageCursor(at, id)
	raw, gotID, ok := decodeUsageCursor(encoded)
	parsed, valid := parseQueryTime(raw)
	if !ok || !valid || gotID != id || !parsed.Equal(at) {
		t.Fatalf("游标必须无损往返纳秒时间: %q -> %s", encoded, parsed)
	}
	if isoString(at) != "2026-10-25T17:02:03.123Z" {
		t.Fatal("显示格式不能跟随游标精度变化")
	}
	// 已发出的毫秒游标继续可解析，不能要求用户刷新才能翻页。
	if _, _, ok := decodeUsageCursor(isoString(at) + "~" + id); !ok {
		t.Fatal("历史毫秒游标必须兼容")
	}
}

func TestUsageAuditAndTeamPaginationKeepPrecisionAndMaximumPageLookahead(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	ctx := context.Background()
	actor, err := f.store.FindUserByEmail(ctx, "admin@mendao.chat")
	if err != nil || actor == nil {
		t.Fatalf("管理员夹具不存在: %v", err)
	}
	team, err := f.store.CreateTeam(ctx, store.CreateTeamInput{Name: "分页上限夹具", QuotaCredits: "100"})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 26, 1, 2, 3, 123400000, time.UTC)
	for index := range 501 {
		if err := f.store.InsertUsage(ctx, store.UsageRecord{
			ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", 9000+index), RequestID: fmt.Sprintf("precise-page-%d", index),
			UserID: actor.ID, TeamID: team.ID, Model: "public-pagination-model", UpstreamModel: "fixture",
			CreditsMicro: 1, Status: store.UsageStatusOk, CreatedAt: base.Add(time.Duration(index) * time.Microsecond),
		}); err != nil {
			t.Fatal(err)
		}
		if err := f.store.InsertAuditLog(ctx, store.InsertAuditLogInput{ActorUserID: actor.ID, TeamID: team.ID, Action: fmt.Sprintf("pagination.fixture.%d", index)}); err != nil {
			t.Fatal(err)
		}
	}
	get := func(path string) map[string]any {
		t.Helper()
		rec := f.do("GET", path, cookie, "")
		if rec.Code != 200 {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
		}
		return dataOf(t, rec)
	}
	usagePath := "/console/api/usage?teamId=" + team.ID + "&limit=500"
	usage := get(usagePath)
	rows := usage["records"].([]any)
	cursor, _ := usage["nextCursor"].(string)
	if len(rows) != 500 || cursor == "" {
		t.Fatalf("最大页仍须探测第501条: records=%d cursor=%q", len(rows), cursor)
	}
	if got := rows[len(rows)-1].(map[string]any)["createdAt"]; got != isoString(base) {
		t.Fatalf("明细显示仍为毫秒: %v", got)
	}
	last := get(usagePath + "&cursor=" + url.QueryEscape(cursor))
	remaining := last["records"].([]any)
	if len(remaining) != 1 || remaining[0].(map[string]any)["requestId"] != "precise-page-0" || last["nextCursor"] != nil {
		t.Fatalf("同毫秒内最后一条必须可达且末页结束: %+v", last)
	}
	// 外部 limit 即使超过500也不能把内部探测记录暴露给用户。
	if oversized := get("/console/api/usage?teamId=" + team.ID + "&limit=999"); len(oversized["records"].([]any)) != 500 {
		t.Fatal("公开页上限必须保持500")
	}
	auditPath := "/console/api/audit?teamId=" + team.ID + "&limit=500"
	audit := get(auditPath)
	logs := audit["logs"].([]any)
	auditCursor, _ := audit["nextCursor"].(string)
	if len(logs) != 500 || auditCursor == "" {
		t.Fatalf("审计最大页也须保留下一页: records=%d cursor=%q", len(logs), auditCursor)
	}
	if auditLast := get(auditPath + "&cursor=" + url.QueryEscape(auditCursor)); len(auditLast["logs"].([]any)) != 1 || auditLast["nextCursor"] != nil {
		t.Fatalf("审计最后一条必须可达: %+v", auditLast)
	}
	teamPath := "/console/api/teams/" + team.ID
	firstTeam := get(teamPath)
	teamCursor, _ := firstTeam["recentUsageNextCursor"].(string)
	if len(firstTeam["recentUsage"].([]any)) != 50 || teamCursor == "" {
		t.Fatalf("团队最近用量首页不完整: %+v", firstTeam)
	}
	secondTeam := get(teamPath + "?usageCursor=" + url.QueryEscape(teamCursor))
	teamRows := secondTeam["recentUsage"].([]any)
	if len(teamRows) != 50 || teamRows[0].(map[string]any)["requestId"] != "precise-page-450" {
		t.Fatalf("团队最近用量必须保留微秒游标: %+v", teamRows)
	}
	if !strings.Contains(cursor, "123401") || !strings.Contains(teamCursor, "123851") {
		t.Fatalf("游标精度未保留: usage=%q team=%q", cursor, teamCursor)
	}
}
