package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testTenantBudgetCarriesActiveReservations(t *testing.T, ctx context.Context, s PlatformStore) {
	for _, kind := range []string{"daily", "monthly", "member"} {
		t.Run(kind, func(t *testing.T) {
			team := tenantTeam(t, ctx, s, "budget-carry-"+kind)
			user := mustUser(t, ctx, s, "budget-carry-"+kind+"@test.local")
			if err := s.AddMember(ctx, team.ID, user.ID, MemberRoleMember); err != nil {
				t.Fatal(err)
			}
			before := time.Date(2026, 10, 31, 15, 59, 59, 0, time.UTC)
			if kind == "daily" {
				before = time.Date(2026, 10, 17, 15, 59, 59, 0, time.UTC)
			}
			after := before.Add(time.Second)
			policy := TeamPolicyRecord{TeamID: team.ID}
			switch kind {
			case "daily":
				policy.DailyBudgetCredits = "1"
			case "monthly":
				policy.MonthlyBudgetCredits = "1"
			case "member":
				budget := "1"
				if _, err := s.UpdateTeamMember(ctx, team.ID, user.ID, TeamMemberPatch{MonthlyBudgetCredits: &budget}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.SaveTeamPolicy(ctx, policy); err != nil {
				t.Fatal(err)
			}
			oldID := uuid.NewString()
			old, err := s.ReserveQuota(ctx, ReserveQuotaInput{ID: oldID, TeamID: team.ID, UserID: user.ID, RequestID: "carry-old-" + kind, Micro: 750000, CreatedAt: before})
			if err != nil || old != ReserveReserved {
				t.Fatalf("旧周期请求应成功预留: %s %v", old, err)
			}
			next, err := s.ReserveQuota(ctx, ReserveQuotaInput{ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID, RequestID: "carry-new-denied-" + kind, Micro: 500000, CreatedAt: after})
			if err != nil || next != ReserveDenied {
				t.Fatalf("旧周期在途预留仍需占用新周期预算: %s %v", next, err)
			}
			if err := s.ReleaseReservationByID(ctx, oldID, after); err != nil {
				t.Fatal(err)
			}
			next, err = s.ReserveQuota(ctx, ReserveQuotaInput{ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID, RequestID: "carry-new-released-" + kind, Micro: 1000000, CreatedAt: after})
			if err != nil || next != ReserveReserved {
				t.Fatalf("旧预留释放后新周期应恢复完整预算: %s %v", next, err)
			}
		})
	}
}
