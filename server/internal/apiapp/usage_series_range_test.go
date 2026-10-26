package apiapp

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestUsageSeriesRejectsInvalidReversedAndExcessiveRanges(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	for _, query := range []url.Values{
		{"from": {"2026-02-31T00:00:00"}},
		{"to": {"not-a-time"}},
		{"from": {"2026-10-26T00:00:00"}, "to": {"2026-10-25T23:59:59.999999999"}},
		{"granularity": {"minute"}},
		{"granularity": {"hour"}, "from": {"2026-09-01T00:00:00Z"}, "to": {"2026-10-20T00:00:00Z"}},
		{"granularity": {"hour"}, "from": {"2026-09-01T08:00:00+08:00"}, "to": {"2026-10-20T08:00:00+08:00"}},
		{"granularity": {"day"}, "from": {"2010-01-01T00:00:00"}, "to": {"2026-10-26T00:00:00"}},
	} {
		if rec := f.do("GET", "/console/api/usage/series?"+query.Encode(), cookie, ""); rec.Code != 400 {
			t.Fatalf("非法趋势范围必须400: %s -> %d %s", query.Encode(), rec.Code, rec.Body.String())
		}
	}
}

func TestUsageSeriesPreservesNaiveUTCAndClosedShanghaiDay(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	ctx := context.Background()
	user, err := f.store.FindUserByEmail(ctx, "admin@mendao.chat")
	if err != nil || user == nil {
		t.Fatalf("缺少管理员夹具: %v", err)
	}
	teams, err := f.store.ListTeams(ctx)
	if err != nil || len(teams) == 0 {
		t.Fatalf("缺少团队夹具: %v", err)
	}
	start := time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)
	for index, at := range []time.Time{start.Add(-time.Microsecond), start, start.Add(24*time.Hour - time.Microsecond), start.Add(24 * time.Hour)} {
		if err := f.store.InsertUsage(ctx, store.UsageRecord{
			ID: uuid.NewString(), RequestID: "series-shanghai-" + time.Duration(index).String(),
			UserID: user.ID, TeamID: teams[0].Team.ID, Model: "series-range-fixture", UpstreamModel: "fixture",
			CreditsMicro: 100, Status: store.UsageStatusOk, CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, bounds := range [][2]string{
		{"2026-10-25T16:00:00", "2026-10-26T15:59:59.999999999"},
		{"2026-10-26T00:00:00+08:00", "2026-10-26T23:59:59.999999999+08:00"},
	} {
		query := url.Values{"from": {bounds[0]}, "to": {bounds[1]}, "granularity": {"day"}, "teamId": {teams[0].Team.ID}}
		rec := f.do("GET", "/console/api/usage/series?"+query.Encode(), cookie, "")
		if rec.Code != 200 {
			t.Fatalf("合法上海自然日: %d %s", rec.Code, rec.Body.String())
		}
		rows := dataOf(t, rec)["series"].([]any)
		if len(rows) != 1 {
			t.Fatalf("完整上海自然日应只有一个桶: %v", rows)
		}
		row := rows[0].(map[string]any)
		if row["bucketStart"] != "2026-10-26T00:00:00" || row["requests"] != float64(2) || row["creditsMicro"] != float64(200) {
			t.Fatalf("上海边界应包含首点与日尾微秒，排除相邻日: %+v", row)
		}
	}
}
