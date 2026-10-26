package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func testAuditCursorSameTimestamp(t *testing.T, ctx context.Context, s PlatformStore) {
	actor := mustUser(t, ctx, s, "audit-same-timestamp@test.local")
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "审计同时间分页", QuotaCredits: "100"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 26, 1, 2, 3, 123456000, time.UTC)
	for _, index := range []int{3, 1, 4, 2} {
		action := fmt.Sprintf("pagination.audit.%d", index)
		if err := s.InsertAuditLog(ctx, InsertAuditLogInput{ActorUserID: actor.ID, TeamID: team.ID, Action: action}); err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", 8000+index)
		// 审计写入时间由存储生成，测试夹具主动固定时间/UUID，避免依赖时钟分辨率或随机碰撞。
		switch actual := s.(type) {
		case *memoryStore:
			actual.mu.Lock()
			for i := range actual.auditLogs {
				if actual.auditLogs[i].ActorUserID == actor.ID && actual.auditLogs[i].Action == action {
					actual.auditLogs[i].ID, actual.auditLogs[i].CreatedAt = id, at
				}
			}
			actual.mu.Unlock()
		case *PostgresStore:
			if _, err := actual.pool.Exec(ctx, `UPDATE admin_audit_logs SET id=$1::uuid,created_at=$2 WHERE actor_user_id=$3::uuid AND action=$4`, id, at, actor.ID, action); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("未知存储夹具类型: %T", s)
		}
	}
	for _, scoped := range []bool{false, true} {
		filter := AuditLogFilter{Limit: ptr(int64(2))}
		if scoped {
			filter.TeamID = team.ID
		} else {
			filter.ActorUserID = actor.ID
		}
		var got []string
		for range 3 {
			page, err := s.ListAuditLogs(ctx, filter)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range page {
				got = append(got, row.Action)
			}
			if len(page) == 0 {
				break
			}
			last := page[len(page)-1]
			filter.CursorCreatedAt, filter.CursorID = &last.CreatedAt, last.ID
		}
		want := []string{"pagination.audit.4", "pagination.audit.3", "pagination.audit.2", "pagination.audit.1"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("审计同时间分页(团队范围=%v)丢失/重复/排序不一致: got %v want %v", scoped, got, want)
		}
	}
}
