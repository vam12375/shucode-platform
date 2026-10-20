package apiapp

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestRootAddsTeamCreditIdempotently(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	team, err := f.store.CreateTeam(context.Background(), store.CreateTeamInput{Name: "topup", QuotaCredits: "10"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/console/api/teams/" + team.ID + "/credits"
	body := fmt.Sprintf(`{"amountCredits":"2.5","requestId":%q}`, uuid.NewString())
	for range 2 {
		if rec := f.do("POST", path, cookie, body); rec.Code != 200 {
			t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
		}
	}
	updated, _ := f.store.GetTeam(context.Background(), team.ID)
	flows, _ := f.store.ListQuotaAdjustments(context.Background(), team.ID, 10)
	if updated.QuotaCredits != "12.500000" || updated.UsedCredits != "0.000000" || len(flows) != 1 || flows[0].Action != "add_credit" {
		t.Fatalf("重复提交不能重复增加: %+v %+v", updated, flows)
	}
	for _, amount := range []string{"0", "-1", "0.0000001", "NaN", "1000000000000"} {
		body := fmt.Sprintf(`{"amountCredits":%q,"requestId":%q}`, amount, uuid.NewString())
		if rec := f.do("POST", path, cookie, body); rec.Code != 400 {
			t.Errorf("invalid %s: %d %s", amount, rec.Code, rec.Body.String())
		}
	}
	if rec := f.do("POST", path, cookie, `{"amountCredits":"1"}`); rec.Code != 400 {
		t.Errorf("缺幂等 key: %d", rec.Code)
	}
	if rec := f.do("POST", path, "", body); rec.Code != 401 {
		t.Errorf("匿名加余额: %d", rec.Code)
	}
}
