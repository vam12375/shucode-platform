package store

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func testAddTeamCredit(t *testing.T, ctx context.Context, s PlatformStore) {
	actor := mustUser(t, ctx, s, "topup@store.test")
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "topup", QuotaCredits: "10", Unlimited: true})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.TryReserveQuota(ctx, team.ID, 250_000); err != nil || !ok {
		t.Fatalf("reserve: %v", err)
	}
	amount := int64(2_500_000)
	var wg sync.WaitGroup
	for range 4 {
		event := uuid.NewString()
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := s.UpdateTeamWithAccounting(ctx, team.ID, UpdateTeamPatch{QuotaIncreaseMicro: &amount}, "add_credit", &InsertAuditLogInput{EventID: event, TeamID: team.ID, ActorUserID: actor.ID, Action: "team.credit.add", TargetType: "team", TargetID: team.ID})
				if err != nil {
					t.Errorf("add: %v", err)
				}
			}()
		}
	}
	wg.Wait()
	updated, _ := s.GetTeam(ctx, team.ID)
	flows, _ := s.ListQuotaAdjustments(ctx, team.ID, 100)
	if updated.QuotaCredits != "20.000000" || updated.ReservedCredits != "0.250000" || updated.UsedCredits != "0.000000" || !updated.Unlimited || len(flows) != 4 {
		t.Fatalf("并发累加/重放幂等/保留用量: %+v %+v", updated, flows)
	}
	amount = maxCreditFieldMicro
	if _, err := s.UpdateTeamWithAccounting(ctx, team.ID, UpdateTeamPatch{QuotaIncreaseMicro: &amount}, "add_credit", &InsertAuditLogInput{EventID: uuid.NewString()}); err == nil {
		t.Fatal("溢出不能入账")
	}
	unchanged, _ := s.GetTeam(ctx, team.ID)
	if unchanged.QuotaCredits != "20.000000" {
		t.Fatal("非法增加不应改变额度")
	}
}
