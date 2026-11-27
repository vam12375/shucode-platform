package store

import (
	"context"
	"errors"
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
	// 两个管理员都读过同一版本，只能有一人提交成功；输掉的写入不能清空新预算。
	results := make(chan error, 2)
	for range 2 {
		go func(revision int64) {
			_, err := s.SaveTeamPolicy(ctx, TeamPolicyRecord{TeamID: team.ID, Revision: revision, DailyBudgetCredits: "3"})
			results <- err
		}(loaded.Revision)
	}
	conflicts := 0
	for range 2 {
		err := <-results
		if errors.Is(err, ErrTeamPolicyRevisionConflict) {
			conflicts++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if conflicts != 1 {
		t.Fatalf("同版本并发写入的冲突次数: %d", conflicts)
	}
	loaded, err = s.GetTeamPolicy(ctx, team.ID)
	if err != nil || loaded.Revision != 2 || loaded.DailyBudgetCredits != "3.000000" || loaded.RPMLimit != 0 || loaded.TPMLimit != 0 || loaded.ConcurrencyLimit != 0 {
		t.Fatalf("显式0必须恢复继承语义: %+v %v", loaded, err)
	}
}

func testCanvasToolPolicy(t *testing.T, ctx context.Context, s PlatformStore) {
	team := tenantTeam(t, ctx, s, "canvas-policy")
	initial, err := s.GetCanvasToolPolicy(ctx, team.ID)
	if err != nil || initial == nil || initial.Revision != 0 || initial.Enabled || len(initial.AllowedOperations) != 0 || !initial.RequireStructureConfirmation || !initial.RequireMediaConfirmation {
		t.Fatalf("Canvas 策略缺省值不安全: %+v %v", initial, err)
	}
	quoteLimit := int64(250_000)
	dayLimit := int64(12)
	policy := CanvasToolPolicyRecord{
		Enabled:                        true,
		AllowedOperations:              []CanvasToolOperation{CanvasToolOperationAddNode, CanvasToolOperationGenerateMedia},
		MaxMediaQuoteMicroPerOperation: &quoteLimit,
		MaxMediaOperationsPerDay:       &dayLimit,
	}
	saved, err := s.SaveCanvasToolPolicy(ctx, team.ID, 0, policy)
	if err != nil || saved == nil || saved.Revision != 1 || !saved.Enabled || !saved.RequireStructureConfirmation || !saved.RequireMediaConfirmation {
		t.Fatalf("Canvas 策略保存失败: %+v %v", saved, err)
	}
	policy.AllowedOperations[0] = CanvasToolOperationRemoveNodes
	*saved.MaxMediaQuoteMicroPerOperation = 1
	loaded, err := s.GetCanvasToolPolicy(ctx, team.ID)
	if err != nil || loaded.Revision != 1 || loaded.AllowedOperations[0] != CanvasToolOperationAddNode || *loaded.MaxMediaQuoteMicroPerOperation != quoteLimit {
		t.Fatalf("Canvas 策略读取必须返回副本: %+v %v", loaded, err)
	}
	if _, err := s.SaveCanvasToolPolicy(ctx, team.ID, 0, policy); !errors.Is(err, ErrCanvasToolPolicyRevisionConflict) {
		t.Fatalf("旧 Canvas revision 必须冲突: %v", err)
	}
	for _, invalid := range []CanvasToolPolicyRecord{
		{Enabled: true, AllowedOperations: []CanvasToolOperation{"unknown"}},
		{Enabled: true, AllowedOperations: []CanvasToolOperation{CanvasToolOperationAddNode, CanvasToolOperationAddNode}},
		{MaxConcurrentMediaOperations: ptr(int64(-1))},
		{MaxMediaQuoteMicroPerOperation: ptr(int64(1_000_000_000_001))},
	} {
		if _, err := s.SaveCanvasToolPolicy(ctx, team.ID, 1, invalid); err == nil {
			t.Fatalf("非法 Canvas 策略必须拒绝: %+v", invalid)
		}
	}
	loaded, err = s.GetCanvasToolPolicy(ctx, team.ID)
	if err != nil || loaded.Revision != 1 || !loaded.Enabled || loaded.AllowedOperations[0] != CanvasToolOperationAddNode {
		t.Fatalf("拒绝策略写入不能修改旧值: %+v %v", loaded, err)
	}
	if _, err := s.SaveTeamPolicy(ctx, TeamPolicyRecord{TeamID: team.ID}); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.GetCanvasToolPolicy(ctx, team.ID)
	if err != nil || loaded.Revision != 1 || !loaded.Enabled {
		t.Fatalf("通用团队策略保存不能覆盖 Canvas 策略: %+v %v", loaded, err)
	}
}
