package store

import (
	"context"
	"testing"
)

func testTeamPolicyLimits(t *testing.T, ctx context.Context, s PlatformStore) {
	team := tenantTeam(t, ctx, s, "policy-limits")
	initial, err := s.GetTeamPolicy(ctx, team.ID)
	if err != nil || initial == nil || initial.RPMLimit != 0 || initial.TPMLimit != 0 || initial.ConcurrencyLimit != 0 {
		t.Fatalf("缺省策略必须继承平台限额: %+v %v", initial, err)
	}
	policy := TeamPolicyRecord{TeamID: team.ID, RPMLimit: 10, TPMLimit: 1000000, ConcurrencyLimit: 20}
	if _, err := s.SaveTeamPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetTeamPolicy(ctx, team.ID)
	if err != nil || loaded.RPMLimit != 10 || loaded.TPMLimit != 1000000 || loaded.ConcurrencyLimit != 20 {
		t.Fatalf("团队流控未完整往返: %+v %v", loaded, err)
	}
	for _, invalid := range []TeamPolicyRecord{
		{TeamID: team.ID, RPMLimit: -1},
		{TeamID: team.ID, TPMLimit: 1000001},
		{TeamID: team.ID, ConcurrencyLimit: -1},
	} {
		if _, err := s.SaveTeamPolicy(ctx, invalid); err == nil {
			t.Fatalf("非法团队流控必须拒绝: %+v", invalid)
		}
	}
	loaded, err = s.GetTeamPolicy(ctx, team.ID)
	if err != nil || loaded.RPMLimit != 10 || loaded.TPMLimit != 1000000 || loaded.ConcurrencyLimit != 20 {
		t.Fatalf("拒绝非法写入时不能篡改旧策略: %+v %v", loaded, err)
	}
	if _, err := s.SaveTeamPolicy(ctx, TeamPolicyRecord{TeamID: team.ID}); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.GetTeamPolicy(ctx, team.ID)
	if err != nil || loaded.RPMLimit != 0 || loaded.TPMLimit != 0 || loaded.ConcurrencyLimit != 0 {
		t.Fatalf("显式0必须恢复继承语义: %+v %v", loaded, err)
	}
}
