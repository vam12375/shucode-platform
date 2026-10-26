package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestMemoryUsageSeriesPreciseBoundaries(t *testing.T) {
	s, closeStore := newMemoryForTest(t)
	defer closeStore()
	testUsageSeriesPreciseBoundaries(t, s)
}

func TestPostgresUsageSeriesPreciseBoundaries(t *testing.T) {
	baseURL := os.Getenv("TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("未设置 TEST_DATABASE_URL,跳过 Postgres 时间边界回归")
	}
	s, closeStore := newPostgresForTest(t, baseURL, os.Getenv("TEST_MIGRATIONS_DIR"))
	defer closeStore()
	testUsageSeriesPreciseBoundaries(t, s)
}

func testUsageSeriesPreciseBoundaries(t *testing.T, s PlatformStore) {
	t.Helper()
	ctx := context.Background()
	user := mustUser(t, ctx, s, "usage-series-bounds@test.local")
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "趋势部分小时边界", QuotaCredits: "100"})
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 10, 24, 15, 37, 12, 123000000, time.UTC)
	to := from.Add(24 * time.Hour)
	dayStart := time.Date(2026, 10, 24, 16, 0, 0, 0, time.UTC)
	wholeHourFrom := from.Truncate(time.Hour)
	wholeHourTo := to.Truncate(time.Hour).Add(time.Hour - time.Nanosecond)
	timestamps := []time.Time{
		wholeHourFrom.Add(5 * time.Minute),
		from.Add(-time.Microsecond),
		from,
		from.Add(10 * time.Minute),
		dayStart,
		to.Truncate(time.Hour).Add(5 * time.Minute),
		to,
		to.Add(time.Microsecond),
		to.Truncate(time.Hour).Add(55 * time.Minute),
		to.Truncate(time.Hour).Add(time.Hour - time.Microsecond),
		to.Truncate(time.Hour).Add(time.Hour),
	}
	for index, at := range timestamps {
		value := int64(index + 1)
		if err := s.InsertUsage(ctx, UsageRecord{
			ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", 15000+index), RequestID: fmt.Sprintf("series-bounds-%d", index),
			UserID: user.ID, TeamID: team.ID, Model: "series-boundary-model", UpstreamModel: "fixture",
			CreditsMicro: value, InputTokens: value * 2, OutputTokens: value * 3,
			CacheReadTokens: value * 4, CacheCreationTokens: value * 5, LatencyMs: ptr(value * 6),
			Status: UsageStatusOk, CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	point := func(bucket string, requests, credits int64) UsageSeriesRow {
		return UsageSeriesRow{BucketStart: bucket, Requests: requests, CreditsMicro: credits,
			InputTokens: credits * 2, OutputTokens: credits * 3, CacheReadTokens: credits * 4,
			CacheCreationTokens: credits * 5, LatencyMsSum: credits * 6}
	}
	for _, scope := range []string{"global", "team"} {
		for _, test := range []struct {
			name        string
			from, to    time.Time
			granularity UsageSeriesGranularity
			want        []UsageSeriesRow
		}{
			{"partial-hour", from, to, SeriesGranularityHour, []UsageSeriesRow{
				point("2026-10-24T23:00:00", 2, 7), point("2026-10-25T00:00:00", 1, 5), point("2026-10-25T23:00:00", 2, 13),
			}},
			{"partial-day", from, to, SeriesGranularityDay, []UsageSeriesRow{
				point("2026-10-24T00:00:00", 2, 7), point("2026-10-25T00:00:00", 3, 18),
			}},
			{"whole-hour", wholeHourFrom, wholeHourTo, SeriesGranularityHour, []UsageSeriesRow{
				point("2026-10-24T23:00:00", 4, 10), point("2026-10-25T00:00:00", 1, 5), point("2026-10-25T23:00:00", 5, 40),
			}},
			{"shanghai-day", dayStart, wholeHourTo, SeriesGranularityDay, []UsageSeriesRow{
				point("2026-10-25T00:00:00", 6, 45),
			}},
			{"closed-single-instant", from, from, SeriesGranularityHour, []UsageSeriesRow{
				point("2026-10-24T23:00:00", 1, 3),
			}},
		} {
			t.Run(scope+"/"+test.name, func(t *testing.T) {
				filter := UsageFilter{From: &test.from, To: &test.to}
				if scope == "team" {
					filter.TeamID = team.ID
				}
				got, err := s.AggregateUsageSeries(ctx, filter, test.granularity)
				if err != nil {
					t.Fatal(err)
				}
				assertJSONEq(t, got, test.want, "趋势必须精确应用首尾闭区间，再按上海墙钟分桶")
			})
		}
	}
}

func TestUsageSeriesWholeHourEligibility(t *testing.T) {
	start := time.Date(2026, 10, 24, 16, 0, 0, 0, time.UTC)
	end := start.Add(24*time.Hour - time.Nanosecond)
	partial := start.Add(37 * time.Minute)
	millisecondEnd := start.Add(24*time.Hour - time.Millisecond)
	for _, test := range []struct {
		name   string
		filter UsageFilter
		want   bool
	}{
		{"unbounded", UsageFilter{}, true},
		{"whole-day", UsageFilter{From: &start, To: &end}, true},
		{"whole-from", UsageFilter{From: &start}, true},
		{"whole-to", UsageFilter{To: &end}, true},
		{"partial-from", UsageFilter{From: &partial, To: &end}, false},
		{"partial-to", UsageFilter{From: &start, To: &partial}, false},
		{"closed-hour-start", UsageFilter{From: &start, To: &start}, false},
		{"millisecond-end", UsageFilter{From: &start, To: &millisecondEnd}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := usageSeriesHasWholeHourBounds(test.filter); got != test.want {
				t.Fatalf("rollup eligibility=%v want %v", got, test.want)
			}
		})
	}
}
