package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func postpaidFixture(t *testing.T, ctx context.Context, s PlatformStore, name string) (*TeamRecord, *UserRecord) {
	t.Helper()
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "postpaid-" + name, QuotaCredits: "1.000000"})
	if err != nil {
		t.Fatal(err)
	}
	u := mustUser(t, ctx, s, "postpaid-"+name+"@store.test")
	if err := s.AddMember(ctx, team.ID, u.ID, MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	return team, u
}

func testPostpaidUsage(t *testing.T, ctx context.Context, s PlatformStore) {
	t.Run("concurrent-full-debit", func(t *testing.T) {
		team, user := postpaidFixture(t, ctx, s, "concurrent")
		const requests = 8
		ids := make([]string, requests)
		var wg sync.WaitGroup
		for i := range ids {
			ids[i] = uuid.NewString()
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				if result, err := s.AdmitUsage(ctx, ReserveQuotaInput{ID: id, TeamID: team.ID, UserID: user.ID, RequestID: id, Micro: 800_000}); err != nil || result != ReserveReserved {
					t.Errorf("admit: result=%s error=%v", result, err)
				}
			}(ids[i])
		}
		wg.Wait()
		before, _ := s.GetTeam(ctx, team.ID)
		if before.UsedCredits != "0.000000" || before.ReservedCredits != "0.000000" {
			t.Fatalf("准入不得占用余额: %+v", before)
		}
		for _, id := range ids {
			identity, err := s.GetQuotaReservation(ctx, id)
			if err != nil || identity == nil || !identity.Postpaid || identity.ReservedMicro != 0 {
				t.Fatalf("请求身份不能持有金额: %+v %v", identity, err)
			}
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				record := UsageRecord{ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID, RequestID: id, Model: "m", UpstreamModel: "m", CreditsMicro: 800_000, Status: UsageStatusOk, CreatedAt: time.Now()}
				if err := s.SettleReservation(ctx, id, record); err != nil {
					t.Errorf("settle: %v", err)
				}
				if err := s.SettleReservation(ctx, id, record); err != nil {
					t.Errorf("replay: %v", err)
				}
			}(id)
		}
		wg.Wait()
		after, _ := s.GetTeam(ctx, team.ID)
		usage, err := s.ListUsage(ctx, UsageFilter{TeamID: team.ID})
		if err != nil || after.UsedCredits != "6.400000" || after.ReservedCredits != "0.000000" || len(usage) != requests {
			t.Fatalf("并发结算必须完整实扣且重放不重复扣款: team=%+v usage=%d error=%v", after, len(usage), err)
		}
		if result, err := s.AdmitUsage(ctx, ReserveQuotaInput{ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID, RequestID: "negative-balance"}); err != nil || result != ReserveDenied {
			t.Fatalf("负余额拒绝新请求: %s %v", result, err)
		}
	})
	t.Run("identity-owner-and-affordability", func(t *testing.T) {
		team, user := postpaidFixture(t, ctx, s, "identity")
		id := uuid.NewString()
		input := ReserveQuotaInput{ID: id, TeamID: team.ID, UserID: user.ID, RequestID: "postpaid-owner", Micro: 1_000_001}
		if result, err := s.AdmitUsage(ctx, input); err != nil || result != ReserveDenied {
			t.Fatalf("单次已知价格不足不得放行: %s %v", result, err)
		}
		input.Micro = 0
		if result, err := s.AdmitUsage(ctx, input); err != nil || result != ReserveReserved {
			t.Fatalf("admit: %s %v", result, err)
		}
		if result, err := s.AdmitUsage(ctx, input); err != nil || result != ReserveDuplicate {
			t.Fatalf("请求身份必须唯一: %s %v", result, err)
		}
		collision := input
		collision.RequestID = "different-request-same-identity"
		if result, err := s.AdmitUsage(ctx, collision); err != nil || result != ReserveDuplicate {
			t.Fatalf("身份 ID 冲突不能覆盖已受理请求: %s %v", result, err)
		}
		record := UsageRecord{ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID, RequestID: input.RequestID, Model: "m", UpstreamModel: "m", Status: UsageStatusAborted, CreatedAt: time.Now()}
		for _, field := range []string{"team", "user", "request"} {
			invalid := record
			switch field {
			case "team":
				invalid.TeamID = uuid.NewString()
			case "user":
				invalid.UserID = uuid.NewString()
			case "request":
				invalid.RequestID = "wrong"
			}
			if err := s.SettleReservation(ctx, id, invalid); !errors.Is(err, ErrReservationMismatch) {
				t.Fatalf("%s 不能修改请求归属: %v", field, err)
			}
		}
		if err := s.SettleReservation(ctx, id, record); err != nil {
			t.Fatal(err)
		}
		record.CreditsMicro = 999_999
		if err := s.SettleReservation(ctx, id, record); err != nil {
			t.Fatal(err)
		}
		invalid := record
		invalid.RequestID = "wrong-after-terminal"
		if err := s.SettleReservation(ctx, id, invalid); !errors.Is(err, ErrReservationMismatch) {
			t.Fatalf("终态重放也必须校验请求归属: %v", err)
		}
		after, _ := s.GetTeam(ctx, team.ID)
		if after.UsedCredits != "0.000000" || after.ReservedCredits != "0.000000" {
			t.Fatalf("零费用终态重放不能后补扣款: %+v", after)
		}
	})
	t.Run("settled-budgets-only", func(t *testing.T) {
		for _, kind := range []string{"daily", "monthly", "member"} {
			t.Run(kind, func(t *testing.T) {
				team, user := postpaidFixture(t, ctx, s, "budget-"+kind)
				policy := TeamPolicyRecord{TeamID: team.ID}
				switch kind {
				case "daily":
					policy.DailyBudgetCredits = "0.500000"
				case "monthly":
					policy.MonthlyBudgetCredits = "0.500000"
				case "member":
					budget := "0.500000"
					if _, err := s.UpdateTeamMember(ctx, team.ID, user.ID, TeamMemberPatch{MonthlyBudgetCredits: &budget}); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := s.SaveTeamPolicy(ctx, policy); err != nil {
					t.Fatal(err)
				}
				legacy := uuid.NewString()
				if result, err := s.ReserveQuota(ctx, ReserveQuotaInput{ID: legacy, TeamID: team.ID, UserID: user.ID, RequestID: legacy, Micro: 500_000}); err != nil || result != ReserveReserved {
					t.Fatalf("legacy fixture: %s %v", result, err)
				}
				for i := range 2 {
					id := uuid.NewString()
					if result, err := s.AdmitUsage(ctx, ReserveQuotaInput{ID: id, TeamID: team.ID, UserID: user.ID, RequestID: id, Micro: 500_000}); err != nil || result != ReserveReserved {
						t.Fatalf("未结算请求不得占预算: %s %v", result, err)
					}
					if i == 1 {
						if err := s.SettleReservation(ctx, id, UsageRecord{ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID, RequestID: id, Model: "m", UpstreamModel: "m", CreditsMicro: 500_000, Status: UsageStatusOk, CreatedAt: time.Now()}); err != nil {
							t.Fatal(err)
						}
					}
				}
				if result, err := s.AdmitUsage(ctx, ReserveQuotaInput{ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID, RequestID: fmt.Sprintf("budget-exhausted-%s", kind)}); err != nil || result != ReserveDeniedBudget {
					t.Fatalf("已结算预算耗尽必须拒绝: %s %v", result, err)
				}
			})
		}
	})
	t.Run("range-check-is-atomic", func(t *testing.T) {
		team, user := postpaidFixture(t, ctx, s, "range")
		id := uuid.NewString()
		if result, err := s.AdmitUsage(ctx, ReserveQuotaInput{ID: id, TeamID: team.ID, UserID: user.ID, RequestID: id}); err != nil || result != ReserveReserved {
			t.Fatalf("admit: %s %v", result, err)
		}
		first := uuid.NewString()
		if result, err := s.AdmitUsage(ctx, ReserveQuotaInput{ID: first, TeamID: team.ID, UserID: user.ID, RequestID: first}); err != nil || result != ReserveReserved {
			t.Fatalf("admit before debit: %s %v", result, err)
		}
		if err := s.SettleReservation(ctx, first, UsageRecord{ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID, RequestID: first, Model: "m", UpstreamModel: "m", CreditsMicro: maxCreditFieldMicro, Status: UsageStatusOk, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := s.SettleReservation(ctx, id, UsageRecord{ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID, RequestID: id, Model: "m", UpstreamModel: "m", CreditsMicro: 1, Status: UsageStatusOk, CreatedAt: time.Now()}); !errors.Is(err, ErrInvalidCreditCharge) {
			t.Fatalf("溢出不能提交: %v", err)
		}
		identity, _ := s.GetQuotaReservation(ctx, id)
		usage, _ := s.ListUsage(ctx, UsageFilter{TeamID: team.ID})
		if identity.Status != ReservationReserved || len(usage) != 1 {
			t.Fatalf("溢出必须保持待结算且不写 usage: %+v %+v", identity, usage)
		}
	})
	t.Run("identity-survives-deadline-without-holding-funds", func(t *testing.T) {
		team, user := postpaidFixture(t, ctx, s, "deadline")
		id := uuid.NewString()
		at := time.Now().Add(-time.Hour)
		if result, err := s.AdmitUsage(ctx, ReserveQuotaInput{ID: id, TeamID: team.ID, UserID: user.ID, RequestID: id, Micro: 100_000, CreatedAt: at, TTL: time.Second}); err != nil || result != ReserveReserved {
			t.Fatalf("admit: %s %v", result, err)
		}
		if _, err := s.ExpireStaleReservations(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		identity, _ := s.GetQuotaReservation(ctx, id)
		if identity.Status != ReservationReserved || identity.ReservedMicro != 0 || !identity.Postpaid {
			t.Fatalf("异步恢复身份不能被金额回收器提前结束: %+v", identity)
		}
		if err := s.ReleaseReservationByID(ctx, id, time.Now()); err != nil {
			t.Fatal(err)
		}
		after, _ := s.GetTeam(ctx, team.ID)
		if after.UsedCredits != "0.000000" || after.ReservedCredits != "0.000000" {
			t.Fatalf("关闭身份不能改动余额: %+v", after)
		}
	})
}
