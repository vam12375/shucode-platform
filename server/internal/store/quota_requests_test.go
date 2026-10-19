package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMemoryQuotaRequestsConformance(t *testing.T) {
	s, closeStore := newMemoryForTest(t)
	defer closeStore()
	runQuotaRequestSuite(t, s)
}
func TestPostgresQuotaRequestsConformance(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未设置 TEST_DATABASE_URL,跳过 Postgres 人工额度申请测试")
	}
	s, closeStore := newPostgresForTest(t, databaseURL, os.Getenv("TEST_MIGRATIONS_DIR"))
	defer closeStore()
	runQuotaRequestSuite(t, s)
}
func runQuotaRequestSuite(t *testing.T, s PlatformStore) {
	ctx := context.Background()
	root, err := s.CreateUser(ctx, UserInput{Email: "quota-root@test.local", PasswordHash: "x", DisplayName: "Root", Role: RoleRoot})
	if err != nil {
		t.Fatal(err)
	}
	fixture := func(t *testing.T, name string) (*TeamRecord, *UserRecord, QuotaRequestRecord) {
		t.Helper()
		team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "request-" + name, QuotaCredits: "5", Unlimited: true})
		if err != nil {
			t.Fatal(err)
		}
		manager := mustUser(t, ctx, s, "request-"+name+"@test.local")
		if err := s.AddMember(ctx, team.ID, manager.ID, MemberRoleAdmin); err != nil {
			t.Fatal(err)
		}
		record := QuotaRequestRecord{ID: uuid.NewString(), TeamID: team.ID, RequestedBy: manager.ID, AmountCredits: "2.5", Reason: "项目扩容", CreatedAt: time.Now().UTC().Truncate(time.Millisecond)}
		return team, manager, record
	}
	t.Run("concurrent-approval-is-once", func(t *testing.T) {
		team, _, request := fixture(t, "concurrency")
		if err := s.CreateQuotaRequest(ctx, request); err != nil {
			t.Fatal(err)
		}
		var approved, reviewed atomic.Int64
		var wg sync.WaitGroup
		errs := make(chan error, 24)
		now := time.Now().UTC().Truncate(time.Millisecond)
		for range 24 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				record, err := s.ReviewQuotaRequest(ctx, request.ID, root.ID, "approve", "人工授信", now)
				if err == nil && record.Status == "approved" {
					approved.Add(1)
				} else if errors.Is(err, ErrQuotaRequestReviewed) {
					reviewed.Add(1)
				} else {
					errs <- fmt.Errorf("record=%+v err=%v", record, err)
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		if approved.Load() != 1 || reviewed.Load() != 23 {
			t.Fatalf("审批必须只能成功一次 approved=%d reviewed=%d", approved.Load(), reviewed.Load())
		}
		updated, err := s.GetTeam(ctx, team.ID)
		if err != nil || updated.QuotaCredits != "7.500000" || !updated.Unlimited {
			t.Fatalf("额度只能增加一次且保留unlimited: %+v %v", updated, err)
		}
		flows, err := s.ListQuotaAdjustments(ctx, team.ID, 10)
		if err != nil || len(flows) != 1 || flows[0].OldQuotaCredits != "5.000000" || flows[0].NewQuotaCredits != "7.500000" || flows[0].Action != "approve_quota_request" {
			t.Fatalf("审批与额度流水必须同事务: %+v %v", flows, err)
		}
		records, err := s.ListQuotaRequests(ctx, team.ID)
		if err != nil || len(records) != 1 || records[0].ReviewedAt == nil || records[0].ReviewedBy != root.ID || records[0].AmountCredits != "2.500000" {
			t.Fatalf("审批记录: %+v %v", records, err)
		}
		*records[0].ReviewedAt = now.Add(time.Hour)
		again, _ := s.ListQuotaRequests(ctx, team.ID)
		if !again[0].ReviewedAt.Equal(now) {
			t.Fatal("返回的审批时间不能别名修改存储")
		}
	})
	t.Run("reject-does-not-grant", func(t *testing.T) {
		team, manager, request := fixture(t, "reject")
		if err := s.CreateQuotaRequest(ctx, request); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReviewQuotaRequest(ctx, request.ID, manager.ID, "approve", "", time.Now()); !errors.Is(err, ErrQuotaRequestInvalid) {
			t.Fatalf("团队管理员不能审批: %v", err)
		}
		if _, err := s.ReviewQuotaRequest(ctx, request.ID, root.ID, "refund", "", time.Now()); !errors.Is(err, ErrQuotaRequestInvalid) {
			t.Fatalf("非法审批参数应拒绝: %v", err)
		}
		reviewed, err := s.ReviewQuotaRequest(ctx, request.ID, root.ID, "reject", "预算依据不足", time.Now())
		if err != nil || reviewed.Status != "rejected" {
			t.Fatalf("拒绝申请: %+v %v", reviewed, err)
		}
		if _, err := s.ReviewQuotaRequest(ctx, request.ID, root.ID, "approve", "", time.Now()); !errors.Is(err, ErrQuotaRequestReviewed) {
			t.Fatalf("拒绝后也不得重放批准: %v", err)
		}
		updated, _ := s.GetTeam(ctx, team.ID)
		flows, _ := s.ListQuotaAdjustments(ctx, team.ID, 10)
		if updated.QuotaCredits != "5.000000" || len(flows) != 0 {
			t.Fatalf("拒绝不能增额或写资金流水: %+v %+v", updated, flows)
		}
	})
	t.Run("invalid-amounts-and-overflow", func(t *testing.T) {
		team, _, request := fixture(t, "limits")
		for _, amount := range []string{"0", "-1", "1000000000000", "999999999999.9999999", "1e3", "NaN"} {
			request.AmountCredits = amount
			if err := s.CreateQuotaRequest(ctx, request); !errors.Is(err, ErrQuotaRequestInvalid) {
				t.Fatalf("非法金额 %q 应拒绝: %v", amount, err)
			}
		}
		request.AmountCredits = "999999999999.999999"
		if err := s.CreateQuotaRequest(ctx, request); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReviewQuotaRequest(ctx, request.ID, root.ID, "approve", "", time.Now()); !errors.Is(err, ErrQuotaRequestInvalid) {
			t.Fatalf("新增额度溢出 numeric(18,6) 应拒绝: %v", err)
		}
		updated, _ := s.GetTeam(ctx, team.ID)
		records, _ := s.ListQuotaRequests(ctx, team.ID)
		flows, _ := s.ListQuotaAdjustments(ctx, team.ID, 10)
		if updated.QuotaCredits != "5.000000" || len(records) != 1 || records[0].Status != "pending" || len(flows) != 0 {
			t.Fatalf("溢出审批必须完整回滚: team=%+v requests=%+v flows=%+v", updated, records, flows)
		}
	})
	t.Run("disabled-team-and-scope", func(t *testing.T) {
		team, manager, request := fixture(t, "disabled")
		if err := s.CreateQuotaRequest(ctx, request); err != nil {
			t.Fatal(err)
		}
		other, _, otherRequest := fixture(t, "other")
		if err := s.CreateQuotaRequest(ctx, otherRequest); err != nil {
			t.Fatal(err)
		}
		records, _ := s.ListQuotaRequests(ctx, team.ID)
		if len(records) != 1 || records[0].TeamID == other.ID {
			t.Fatalf("团队申请查询越界: %+v", records)
		}
		now := time.Now()
		if _, err := s.UpdateTeam(ctx, team.ID, UpdateTeamPatch{HasDisabled: true, DisabledAt: &now}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReviewQuotaRequest(ctx, request.ID, root.ID, "approve", "", now); !errors.Is(err, ErrQuotaRequestInvalid) {
			t.Fatalf("停用团队不能批准: %v", err)
		}
		request.ID = uuid.NewString()
		request.RequestedBy = manager.ID
		if err := s.CreateQuotaRequest(ctx, request); !errors.Is(err, ErrQuotaRequestInvalid) {
			t.Fatalf("停用团队不能申请: %v", err)
		}
		if _, err := s.ReviewQuotaRequest(ctx, records[0].ID, root.ID, "reject", "团队已停用", now); err != nil {
			t.Fatalf("停用团队允许拒绝待审申请: %v", err)
		}
	})
}
