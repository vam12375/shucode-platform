package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func testUsageModelFilterPaginationExport(t *testing.T, ctx context.Context, s PlatformStore) {
	user := mustUser(t, ctx, s, "model-filter-pagination@test.local")
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "多模型筛选分页", QuotaCredits: "100"})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	// 故意乱序插入同一时间的 UUID，确保分页不能依赖插入顺序或数据库物理顺序。
	for _, index := range []int{3, 1, 4, 2, 6, 5} {
		model := "media:route-pool-1k"
		if index%2 == 0 {
			model = "media:route-pool-2k"
		}
		if index == 5 {
			model = "media:excluded-model"
		}
		at := base
		if index == 6 {
			at = base.Add(-time.Second)
		}
		if err := s.InsertUsage(ctx, UsageRecord{
			ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", 7000+index), RequestID: fmt.Sprintf("model-filter-%d", index),
			UserID: user.ID, TeamID: team.ID, Model: model, UpstreamModel: "private-route", Status: UsageStatusOk,
			CreditsMicro: int64(index), CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	filter := UsageFilter{TeamID: team.ID, Models: []string{"media:route-pool-1k", "media:route-pool-2k"}, Limit: ptr(int64(2))}
	ids := func(rows []UsageRecord, err error) []string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(rows))
		for i, row := range rows {
			out[i] = row.RequestID
		}
		return out
	}
	want := []string{"model-filter-4", "model-filter-3", "model-filter-2", "model-filter-1", "model-filter-6"}
	var got []string
	for range 4 {
		page, err := s.ListUsage(ctx, filter)
		got = append(got, ids(page, err)...)
		if len(page) == 0 {
			break
		}
		last := page[len(page)-1]
		filter.CursorCreatedAt, filter.CursorID = &last.CreatedAt, last.ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("多模型分页必须不丢失、不重复、按(时间,UUID)倒序: got %v want %v", got, want)
	}
	filter.CursorCreatedAt, filter.CursorID = nil, ""
	if got := ids(s.ListUsageForExport(ctx, filter, 3)); !reflect.DeepEqual(got, want[:3]) {
		t.Fatalf("导出上限独立于页面上限且沿用多模型过滤: %v", got)
	}
	if got := ids(s.ListUsageForExport(ctx, filter, 100)); !reflect.DeepEqual(got, want) {
		t.Fatalf("全量导出必须保留筛选与稳定顺序: %v", got)
	}
	filter.Model = "media:route-pool-1k"
	if got := ids(s.ListUsageForExport(ctx, filter, 100)); !reflect.DeepEqual(got, []string{"model-filter-3", "model-filter-1"}) {
		t.Fatalf("单模型与多模型条件取交集: %v", got)
	}
	filter.Model, filter.Models = "", []string{}
	if got := ids(s.ListUsage(ctx, filter)); len(got) != 0 {
		t.Fatalf("空多模型集合不得退化为未筛选: %v", got)
	}
	if got := ids(s.ListUsageForExport(ctx, filter, 100)); len(got) != 0 {
		t.Fatalf("空集合导出不得泄露未授权模型: %v", got)
	}
	filter.Models = nil
	if got := ids(s.ListUsageForExport(ctx, filter, 100)); len(got) != 6 {
		t.Fatalf("nil集合表示不按多模型筛选: %v", got)
	}
}
