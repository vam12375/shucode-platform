package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/pricing"
)

// runSuite:内存与 Postgres 实现共用的行为契约(端口语义的唯一定义处)。
func runSuite(t *testing.T, ctx context.Context, s PlatformStore) {
	t.Helper()
	t.Run("users", func(t *testing.T) { testUsers(t, ctx, s) })
	t.Run("sessions", func(t *testing.T) { testSessions(t, ctx, s) })
	t.Run("teams", func(t *testing.T) { testTeams(t, ctx, s) })
	t.Run("team-accounting", func(t *testing.T) { testTeamAccounting(t, ctx, s) })
	t.Run("quota-critical-section", func(t *testing.T) { testQuota(t, ctx, s) })
	t.Run("quota-reservations", func(t *testing.T) { testReservations(t, ctx, s) })
	t.Run("postpaid-usage", func(t *testing.T) { testPostpaidUsage(t, ctx, s) })
	t.Run("background-usage-observation", func(t *testing.T) { testBackgroundUsageObservation(t, ctx, s) })
	t.Run("full-precharge-background", func(t *testing.T) { testFullPrechargeAndBackgroundJobs(t, ctx, s) })
	t.Run("pending-settlements", func(t *testing.T) { testPendingSettlements(t, ctx, s) })
	t.Run("upstream-routes", func(t *testing.T) { testUpstreamRoutes(t, ctx, s) })
	t.Run("ops-alerts", func(t *testing.T) { testOpsAlerts(t, ctx, s) })
	t.Run("device-tokens", func(t *testing.T) { testDeviceTokens(t, ctx, s) })
	t.Run("login-flows", func(t *testing.T) { testLoginFlows(t, ctx, s) })
	t.Run("releases", func(t *testing.T) { testReleases(t, ctx, s) })
	t.Run("models", func(t *testing.T) { testModels(t, ctx, s) })
	t.Run("providers", func(t *testing.T) { testProviders(t, ctx, s) })
	t.Run("pricing", func(t *testing.T) { testPricing(t, ctx, s) })
	t.Run("audit", func(t *testing.T) { testAudit(t, ctx, s) })
	t.Run("config-kv", func(t *testing.T) { testConfigKV(t, ctx, s) })
	t.Run("usage", func(t *testing.T) { testUsage(t, ctx, s) })
	t.Run("usage-price-snapshot", func(t *testing.T) { runUsageSnapshotSuite(t, ctx, s) })
	t.Run("team-policy-limits", func(t *testing.T) { testTeamPolicyLimits(t, ctx, s) })
	t.Run("budget-carry-active-reservations", func(t *testing.T) { testTenantBudgetCarriesActiveReservations(t, ctx, s) })
	t.Run("tenant-permissions", func(t *testing.T) { runTenantSuite(t, ctx, s) })
}

func mustUser(t *testing.T, ctx context.Context, s PlatformStore, email string) *UserRecord {
	t.Helper()
	u, err := s.CreateUser(ctx, UserInput{Email: email, PasswordHash: "scrypt$x", DisplayName: "测试", Role: RoleUser})
	if err != nil {
		t.Fatalf("建用户 %s: %v", email, err)
	}
	return u
}

func testUsers(t *testing.T, ctx context.Context, s PlatformStore) {
	u := mustUser(t, ctx, s, "Alice@Test.Local ")
	if u.Email != "alice@test.local" {
		t.Fatalf("建档邮箱应 trim+降写: %q", u.Email)
	}
	if _, err := s.CreateUser(ctx, UserInput{Email: "alice@test.local", PasswordHash: "x", DisplayName: "x", Role: RoleUser}); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("重复邮箱应返回 ErrEmailTaken: %v", err)
	}
	if _, err := s.CreateUser(ctx, UserInput{Email: "ALICE@TEST.LOCAL", PasswordHash: "x", DisplayName: "x", Role: RoleUser}); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("大小写不同也算重复: %v", err)
	}
	found, err := s.FindUserByEmail(ctx, "alice@test.local")
	if err != nil || found == nil || found.ID != u.ID {
		t.Fatalf("find by email: %v %+v", err, found)
	}
	// PG 语义:查询侧 lower 比较但不 trim。
	if found, _ := s.FindUserByEmail(ctx, " alice@test.local"); found != nil {
		t.Fatal("查询侧不应 trim 输入")
	}
	if got, _ := s.FindUserByID(ctx, u.ID); got == nil || got.DisplayName != "测试" {
		t.Fatalf("find by id: %+v", got)
	}
	// patch:仅显式字段生效;HasDisabled 区分「缺省」与「显式启用」。
	rename := "改名"
	updated, err := s.UpdateUser(ctx, u.ID, UpdateUserPatch{DisplayName: &rename})
	if err != nil || updated.DisplayName != "改名" || updated.Role != RoleUser {
		t.Fatalf("改名 patch: %v %+v", err, updated)
	}
	disabledAt := time.Now().UTC().Truncate(time.Millisecond)
	updated, _ = s.UpdateUser(ctx, u.ID, UpdateUserPatch{HasDisabled: true, DisabledAt: &disabledAt})
	if updated.DisabledAt == nil || !updated.DisabledAt.Equal(disabledAt) {
		t.Fatalf("禁用时间不符: %+v", updated.DisabledAt)
	}
	updated, _ = s.UpdateUser(ctx, u.ID, UpdateUserPatch{HasDisabled: true})
	if updated.DisabledAt != nil {
		t.Fatal("显式启用应清空 disabledAt")
	}
	updated, _ = s.UpdateUser(ctx, u.ID, UpdateUserPatch{})
	if updated.DisabledAt != nil || updated.DisplayName != "改名" {
		t.Fatal("空 patch 不得改动任何字段")
	}
	users, _ := s.ListUsers(ctx)
	if len(users) == 0 || users[0].CreatedAt.After(users[len(users)-1].CreatedAt) {
		t.Fatal("用户列表应按创建时间升序")
	}
	// root 全库唯一、不可禁用/降级；root 自己改显示名不受影响。
	root, err := s.CreateUser(ctx, UserInput{
		Email: "root@test.local", PasswordHash: "scrypt$x", DisplayName: "Root", Role: RoleRoot,
	})
	if err != nil {
		t.Fatalf("建 root: %v", err)
	}
	if foundRoot, err := s.FindRootUser(ctx); err != nil || foundRoot == nil || foundRoot.ID != root.ID {
		t.Fatalf("find root: %v %+v", err, foundRoot)
	}
	if _, err := s.CreateUser(ctx, UserInput{
		Email: "root2@test.local", PasswordHash: "x", DisplayName: "Root2", Role: RoleRoot,
	}); !errors.Is(err, ErrRootUserExists) {
		t.Fatalf("第二个 root 应返回 ErrRootUserExists: %v", err)
	}
	promote := RoleRoot
	if _, err := s.UpdateUser(ctx, u.ID, UpdateUserPatch{Role: &promote}); !errors.Is(err, ErrRootUserExists) {
		t.Fatalf("把他人提升为 root 应返回 ErrRootUserExists: %v", err)
	}
	demote := RoleAdmin
	if _, err := s.UpdateUser(ctx, root.ID, UpdateUserPatch{Role: &demote}); !errors.Is(err, ErrRootUserProtected) {
		t.Fatalf("降级 root 应返回 ErrRootUserProtected: %v", err)
	}
	if _, err := s.UpdateUser(ctx, root.ID, UpdateUserPatch{
		HasDisabled: true, DisabledAt: &disabledAt,
	}); !errors.Is(err, ErrRootUserProtected) {
		t.Fatalf("禁用 root 应返回 ErrRootUserProtected: %v", err)
	}
	rootName := "Root Renamed"
	if updated, err := s.UpdateUser(ctx, root.ID, UpdateUserPatch{DisplayName: &rootName}); err != nil || updated.DisplayName != rootName {
		t.Fatalf("root 改显示名: %v %+v", err, updated)
	}
	if got, _ := s.FindRootUser(ctx); got == nil || got.Role != RoleRoot || got.DisabledAt != nil {
		t.Fatalf("root 保护失败: %+v", got)
	}
	if u2, _ := s.UpdateUser(ctx, "00000000-0000-4000-8000-000000000000", UpdateUserPatch{}); u2 != nil {
		t.Fatal("不存在的用户应返回 nil")
	}
}

func testSessions(t *testing.T, ctx context.Context, s PlatformStore) {
	u := mustUser(t, ctx, s, "sess@test.local")
	now := time.Now().UTC().Truncate(time.Millisecond)
	rec := SessionRecord{SecurityVersion: "security-epoch-1", TokenHash: "hash-1", UserID: u.ID, CreatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour), LastSeenAt: now}
	if err := s.CreateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.FindSession(ctx, "hash-1")
	if err != nil || got == nil || got.UserID != u.ID || got.SecurityVersion != rec.SecurityVersion || !got.ExpiresAt.Equal(rec.ExpiresAt) {
		t.Fatalf("find session: %v %+v", err, got)
	}
	later := now.Add(time.Minute)
	if err := s.TouchSession(ctx, "hash-1", later); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FindSession(ctx, "hash-1"); !got.LastSeenAt.Equal(later) {
		t.Fatalf("touch 未生效: %v", got.LastSeenAt)
	}
	if err := s.CreateSession(ctx, SessionRecord{TokenHash: "hash-2", UserID: u.ID, CreatedAt: now, ExpiresAt: later, LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSessionsForUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FindSession(ctx, "hash-1"); got != nil {
		t.Fatal("按用户删会话失败")
	}
	if err := s.DeleteSession(ctx, "hash-2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FindSession(ctx, "hash-2"); got != nil {
		t.Fatal("单会话删除失败")
	}
}

func testTeams(t *testing.T, ctx context.Context, s PlatformStore) {
	owner := mustUser(t, ctx, s, "owner@test.local")
	member := mustUser(t, ctx, s, "member@test.local")
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: " 研发部 ", QuotaCredits: "1000.000000"})
	if err != nil || team.Name != "研发部" || team.UsedCredits != "0.000000" || team.ReservedCredits != "0.000000" {
		t.Fatalf("建团队: %v %+v", err, team)
	}
	if _, err := s.CreateTeam(ctx, CreateTeamInput{Name: "研发部", QuotaCredits: "1"}); !errors.Is(err, ErrTeamNameTaken) {
		t.Fatalf("重名团队应 ErrTeamNameTaken: %v", err)
	}
	if err := s.AddMember(ctx, team.ID, owner.ID, MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := s.AddMember(ctx, team.ID, member.ID, MemberRoleMember); err != nil {
		t.Fatal(err)
	}
	// 重复添加只改角色,不重置加入时间。
	if err := s.AddMember(ctx, team.ID, member.ID, MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	members, _ := s.ListMembers(ctx, team.ID)
	if len(members) != 2 || members[0].User.ID != owner.ID || members[1].Role != MemberRoleOwner {
		t.Fatalf("成员列表/加入顺序/角色覆盖不符: %+v", members)
	}
	teams, _ := s.ListTeams(ctx)
	if len(teams) != 1 || teams[0].MemberCount != 2 {
		t.Fatalf("团队列表成员数不符: %+v", teams)
	}
	// listTeamsForUser 按成员加入时间排序(PG 语义)。
	ownerTeams, _ := s.ListTeamsForUser(ctx, owner.ID)
	memberTeams, _ := s.ListTeamsForUser(ctx, member.ID)
	if len(ownerTeams) != 1 || ownerTeams[0].Role != MemberRoleOwner || len(memberTeams) != 1 || memberTeams[0].Role != MemberRoleOwner {
		t.Fatalf("成员归属/角色不符: %+v %+v", ownerTeams, memberTeams)
	}
	// 全量成员关系展平(控制台账号列表注入归属用):加入顺序、团队名、角色、预算空值。
	all, err := s.ListAllMemberships(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].UserID != owner.ID || all[1].UserID != member.ID {
		t.Fatalf("全量成员关系应按加入时间升序: %+v", all)
	}
	for _, row := range all {
		if row.TeamID != team.ID || row.TeamName != "研发部" || row.MonthlyBudgetCredits != "" || row.MemberDisabledAt != nil {
			t.Fatalf("成员关系归属字段不符: %+v", row)
		}
	}
	if all[0].Role != MemberRoleOwner || all[1].Role != MemberRoleOwner {
		t.Fatalf("重复添加只覆盖角色: %+v", all)
	}
	quota := "2000.000000"
	unlimited := true
	updated, err := s.UpdateTeam(ctx, team.ID, UpdateTeamPatch{QuotaCredits: &quota, Unlimited: &unlimited})
	if err != nil || updated.QuotaCredits != "2000.000000" || !updated.Unlimited || updated.Name != "研发部" {
		t.Fatalf("更新团队: %v %+v", err, updated)
	}
	rpm, tpm, concurrency := int64(60), int64(120_000), int64(8)
	updated, err = s.UpdateTeam(ctx, team.ID, UpdateTeamPatch{RPMLimit: &rpm, TPMLimit: &tpm, ConcurrencyLimit: &concurrency})
	if err != nil || updated.RPMLimit != 60 || updated.TPMLimit != 120_000 || updated.ConcurrencyLimit != 8 {
		t.Fatalf("团队流控更新失败: %v %+v", err, updated)
	}
	if removed, _ := s.RemoveMember(ctx, team.ID, member.ID); !removed {
		t.Fatal("移除成员失败")
	}
	if removed, _ := s.RemoveMember(ctx, team.ID, member.ID); removed {
		t.Fatal("二次移除应为 false")
	}
}

func testTeamAccounting(t *testing.T, ctx context.Context, s PlatformStore) {
	t.Run("add_credit", func(t *testing.T) { testAddTeamCredit(t, ctx, s) })
	suffix := fmt.Sprint(time.Now().UnixNano())
	admin := mustUser(t, ctx, s, "accounting-"+suffix+"@test.local")
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "accounting-team-" + suffix, QuotaCredits: "5.000000"})
	if err != nil {
		t.Fatal(err)
	}
	quota := "20.000000"
	eventID := fmt.Sprintf("00000000-0000-4000-8000-%012d", time.Now().UnixNano()%1_000_000_000_000)
	entry := &InsertAuditLogInput{
		EventID: eventID, TeamID: team.ID, ActorUserID: admin.ID,
		Action: "team.update", TargetType: "team", TargetID: team.ID,
		Detail: []byte(`{"fields":["quota"]}`),
	}
	updated, err := s.UpdateTeamWithAccounting(ctx, team.ID, UpdateTeamPatch{QuotaCredits: &quota}, "set_quota", entry)
	if err != nil || updated == nil || updated.QuotaCredits != "20.000000" {
		t.Fatalf("团队原子更新失败: %+v %v", updated, err)
	}
	adjustments, err := s.ListQuotaAdjustments(ctx, team.ID, 10)
	if err != nil || len(adjustments) != 1 {
		t.Fatalf("额度流水应与团队更新同事务写入: %d %v", len(adjustments), err)
	}
	if adjustments[0].OldQuotaCredits != "5.000000" || adjustments[0].NewQuotaCredits != "20.000000" {
		t.Fatalf("额度流水快照不符: %+v", adjustments[0])
	}
	limit := int64(10)
	logs, err := s.ListAuditLogs(ctx, AuditLogFilter{TeamID: team.ID, ActorUserID: admin.ID, Limit: &limit})
	if err != nil || len(logs) != 1 || logs[0].Action != "team.update" {
		t.Fatalf("团队审计应与更新同事务写入: %+v %v", logs, err)
	}
	// 同一事务方法对不存在的团队返回 nil，不写流水/审计。
	missing, err := s.UpdateTeamWithAccounting(ctx, "00000000-0000-4000-8000-999999999999", UpdateTeamPatch{QuotaCredits: &quota}, "set_quota", entry)
	if err != nil || missing != nil {
		t.Fatalf("不存在的团队应返回 nil: %+v %v", missing, err)
	}
}

func testQuota(t *testing.T, ctx context.Context, s PlatformStore) {
	u := mustUser(t, ctx, s, "quota@test.local")
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "配额团队-" + fmt.Sprint(time.Now().UnixNano()), QuotaCredits: "5.000000"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.TryReserveQuota(ctx, team.ID, 5_000_000); !ok {
		t.Fatal("恰好用满配额应成功")
	}
	if ok, _ := s.TryReserveQuota(ctx, team.ID, 1); ok {
		t.Fatal("余额到 0 后应拒绝(1 micro 也不行)")
	}
	if err := s.ReleaseReservation(ctx, team.ID, 5_000_000); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.TryReserveQuota(ctx, team.ID, 5_000_000); !ok {
		t.Fatal("释放后应可再次预留")
	}
	// 结算:预留转实扣、usage 落库同事务。
	rec := UsageRecord{
		ID: "00000000-0000-4000-8000-000000000101", RequestID: "req-settle",
		UserID: u.ID, TeamID: team.ID, Model: "m", UpstreamModel: "m",
		InputTokens: 1, OutputTokens: 1, CreditsMicro: 1_234_567,
		Status: UsageStatusOk, CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := s.SettleUsage(ctx, rec, 5_000_000); err != nil {
		t.Fatal(err)
	}
	after, _ := s.GetTeam(ctx, team.ID)
	if after.UsedCredits != "1.234567" || after.ReservedCredits != "0.000000" {
		t.Fatalf("结算后额度不符: used=%s reserved=%s", after.UsedCredits, after.ReservedCredits)
	}
	if usage, _ := s.ListUsage(ctx, UsageFilter{TeamID: team.ID}); len(usage) != 1 || usage[0].RequestID != "req-settle" {
		t.Fatal("结算必须同时写入 usage 账本")
	}
	// 并发临界区:100 并发各预留 0.1,配额 5.0 → 恰好 50 个成功。
	conc, _ := s.CreateTeam(ctx, CreateTeamInput{Name: "并发团队-" + fmt.Sprint(time.Now().UnixNano()), QuotaCredits: "5.000000"})
	var success int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.TryReserveQuota(ctx, conc.ID, 100_000)
			if err != nil {
				t.Errorf("并发预留错误: %v", err)
				return
			}
			if ok {
				atomic.AddInt64(&success, 1)
			}
		}()
	}
	wg.Wait()
	if success != 50 {
		t.Fatalf("并发预留应恰好成功 50 次,实际 %d(存在超发或误拒)", success)
	}
	final, _ := s.GetTeam(ctx, conc.ID)
	if final.ReservedCredits != "5.000000" {
		t.Fatalf("并发预留总额不符: %s", final.ReservedCredits)
	}
	// 停用团队拒绝预留。
	if blocked, err := tryUpdateTeamDisabled(s, ctx, conc.ID); err != nil || !blocked {
		t.Fatalf("停用团队应拒绝预留: %v", err)
	}
}

// tryUpdateTeamDisabled:把团队置为停用,并验证预留被拒绝。
func tryUpdateTeamDisabled(s PlatformStore, ctx context.Context, teamID string) (bool, error) {
	at := time.Now().UTC().Truncate(time.Millisecond)
	updated, err := s.UpdateTeam(ctx, teamID, UpdateTeamPatch{HasDisabled: true, DisabledAt: &at})
	if err != nil || updated == nil {
		return false, err
	}
	ok, err := s.TryReserveQuota(ctx, teamID, 1)
	return !ok && err == nil, err
}

func testReservations(t *testing.T, ctx context.Context, s PlatformStore) {
	u := mustUser(t, ctx, s, "reserve@test.local")
	suffix := fmt.Sprint(time.Now().UnixNano())
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "预留团队-" + suffix, QuotaCredits: "1.000000"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	input := ReserveQuotaInput{
		ID: "00000000-0000-4000-8000-000000000601", TeamID: team.ID,
		RequestID: "reserve-req-1-" + suffix, Micro: 400_000, TTL: time.Hour, CreatedAt: now,
	}
	if result, err := s.ReserveQuota(ctx, input); err != nil || result != ReserveReserved {
		t.Fatalf("首次预留应成功: %v %s", err, result)
	}
	duplicate := input
	duplicate.ID = "00000000-0000-4000-8000-000000000602"
	if result, err := s.ReserveQuota(ctx, duplicate); err != nil || result != ReserveDuplicate {
		t.Fatalf("重复 request_id 应返回 duplicate: %v %s", err, result)
	}
	// request_id 幂等必须按团队隔离：其他租户复用同一 ID 不应被抢占。
	team2, err := s.CreateTeam(ctx, CreateTeamInput{Name: "预留团队二-" + suffix, QuotaCredits: "1.000000"})
	if err != nil {
		t.Fatal(err)
	}
	otherTeamInput := input
	otherTeamInput.ID = "00000000-0000-4000-8000-000000000607"
	otherTeamInput.TeamID = team2.ID
	if result, err := s.ReserveQuota(ctx, otherTeamInput); err != nil || result != ReserveReserved {
		t.Fatalf("跨团队相同 request_id 应允许: %v %s", err, result)
	}
	if err := s.ReleaseReservationByID(ctx, otherTeamInput.ID, now); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTeam(ctx, team.ID); got.ReservedCredits != "0.400000" {
		t.Fatalf("预留额度不符: %s", got.ReservedCredits)
	}
	record := UsageRecord{
		ID: "00000000-0000-4000-8000-000000000603", RequestID: input.RequestID,
		UserID: u.ID, TeamID: team.ID, Model: "m", UpstreamModel: "m",
		CreditsMicro: 300_000, Status: UsageStatusOk, CreatedAt: now.Add(time.Second),
	}
	if err := s.SettleReservation(ctx, input.ID, record); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTeam(ctx, team.ID)
	if got.UsedCredits != "0.300000" || got.ReservedCredits != "0.000000" {
		t.Fatalf("预留结算后额度不符: used=%s reserved=%s", got.UsedCredits, got.ReservedCredits)
	}
	// 重复结算必须幂等（不重复写 usage）。
	if err := s.SettleReservation(ctx, input.ID, record); err != nil {
		t.Fatal(err)
	}
	if usage, _ := s.ListUsage(ctx, UsageFilter{TeamID: team.ID}); len(usage) != 1 {
		t.Fatalf("重复结算不应重复写 usage: %d", len(usage))
	}
	// 释放。
	releaseInput := ReserveQuotaInput{
		ID: "00000000-0000-4000-8000-000000000604", TeamID: team.ID,
		RequestID: "reserve-req-2-" + suffix, Micro: 500_000, TTL: time.Hour, CreatedAt: now,
	}
	if result, _ := s.ReserveQuota(ctx, releaseInput); result != ReserveReserved {
		t.Fatalf("第二次预留失败: %s", result)
	}
	if err := s.ReleaseReservationByID(ctx, releaseInput.ID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTeam(ctx, team.ID); got.ReservedCredits != "0.000000" {
		t.Fatalf("释放后预留应归零: %s", got.ReservedCredits)
	}
	// 过期回收。
	expireInput := ReserveQuotaInput{
		ID: "00000000-0000-4000-8000-000000000605", TeamID: team.ID,
		RequestID: "reserve-req-3-" + suffix, Micro: 600_000, TTL: time.Minute,
		CreatedAt: now.Add(-2 * time.Hour),
	}
	if result, _ := s.ReserveQuota(ctx, expireInput); result != ReserveReserved {
		t.Fatalf("第三次预留失败: %s", result)
	}
	if count, err := s.ExpireStaleReservations(ctx, now); err != nil || count != 1 {
		t.Fatalf("过期回收数量不符: %d %v", count, err)
	}
	if got, _ := s.GetTeam(ctx, team.ID); got.ReservedCredits != "0.000000" {
		t.Fatalf("过期回收后预留应归零: %s", got.ReservedCredits)
	}
	if open, _ := s.ListOpenReservations(ctx, 10); len(open) != 0 {
		t.Fatalf("不应有未释放预留: %+v", open)
	}

	// 媒体任务持有预留，通用 TTL 清扫不能抢先释放仍在运行或核对中的额度。
	held := ReserveQuotaInput{ID: "00000000-0000-4000-8000-000000009607", TeamID: team.ID, RequestID: "media-held-" + suffix, Micro: 100_000, TTL: time.Minute, CreatedAt: now.Add(-2 * time.Hour), HeldByTask: true}
	if result, err := s.ReserveQuota(ctx, held); err != nil || result != ReserveReserved {
		t.Fatalf("held reserve: %s %v", result, err)
	}
	if count, err := s.ExpireStaleReservations(ctx, now); err != nil || count != 0 {
		t.Fatalf("held expired: %d %v", count, err)
	}
	if open, err := s.ListOpenReservations(ctx, 10); err != nil || len(open) != 1 || !open[0].HeldByTask {
		t.Fatalf("held ownership: %+v %v", open, err)
	}
	if err := s.ReleaseReservationByID(ctx, held.ID, now); err != nil {
		t.Fatal(err)
	}

	// 新网关的未知成本即使过期仍占用额度，直至明确结算。
	held.ID = "00000000-0000-4000-8000-000000009608"
	held.RequestID = "gateway-retain-" + suffix
	held.HeldByTask, held.RetainUntilSettled = false, true
	if result, err := s.ReserveQuota(ctx, held); err != nil || result != ReserveReserved {
		t.Fatalf("retain reserve: %s %v", result, err)
	}
	if count, err := s.ExpireStaleReservations(ctx, now); err != nil || count != 0 {
		t.Fatalf("retain expired: %d %v", count, err)
	}
	if open, err := s.ListOpenReservations(ctx, 10); err != nil || len(open) != 1 || !open[0].RetainUntilSettled {
		t.Fatalf("retain ownership: %+v %v", open, err)
	}
	record.ID, record.RequestID, record.CreditsMicro = "00000000-0000-4000-8000-000000009609", held.RequestID, 100_000
	if err := s.SettleReservation(ctx, held.ID, record); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTeam(ctx, team.ID); got.ReservedCredits != "0.000000" {
		t.Fatalf("retain settlement: %+v", got)
	}

	// 超配额拒绝。
	over := ReserveQuotaInput{
		ID: "00000000-0000-4000-8000-000000000606", TeamID: team.ID,
		RequestID: "reserve-req-4-" + suffix, Micro: 900_000, TTL: time.Hour, CreatedAt: now,
	}
	if result, _ := s.ReserveQuota(ctx, over); result != ReserveDenied {
		t.Fatalf("超配额应拒绝: %s", result)
	}
}

func testPendingSettlements(t *testing.T, ctx context.Context, s PlatformStore) {
	suffix := fmt.Sprint(time.Now().UnixNano())
	u := mustUser(t, ctx, s, "pending-"+suffix+"@test.local")
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "pending-team-" + suffix, QuotaCredits: "10.000000"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(ctx, team.ID, u.ID, MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	reservationID := fmt.Sprintf("00000000-0000-4000-8000-%012d", time.Now().UnixNano()%1_000_000_000_000)
	input := ReserveQuotaInput{
		ID: reservationID, TeamID: team.ID, UserID: u.ID,
		RequestID: "pending-req-" + suffix, Micro: 1000, TTL: time.Hour, CreatedAt: now,
	}
	if result, err := s.ReserveQuota(ctx, input); err != nil || result != ReserveReserved {
		t.Fatalf("预留失败: %s %v", result, err)
	}
	record := UsageRecord{
		ID:            fmt.Sprintf("00000000-0000-4000-8000-%012d", (time.Now().UnixNano()+1)%1_000_000_000_000),
		RequestID:     input.RequestID,
		UserID:        u.ID,
		TeamID:        team.ID,
		Model:         "pending-model",
		UpstreamModel: "pending-upstream",
		CreditsMicro:  500,
		PriceSnapshot: &UsagePricingSnapshot{SchemaVersion: 1, CapturedAt: now},
		Status:        UsageStatusError,
		ErrorType:     "upstream_idle_timeout",
		CreatedAt:     now,
	}
	if err := s.EnqueuePendingSettlement(ctx, reservationID, record); err != nil {
		t.Fatal(err)
	}
	// 重复入队幂等。
	if err := s.EnqueuePendingSettlement(ctx, reservationID, record); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListPendingSettlements(ctx, PendingSettlementFilter{
		Status: PendingSettlementPending, Limit: 10,
	})
	if err != nil || len(pending) != 1 {
		t.Fatalf("待结算列表不符: %d %v", len(pending), err)
	}
	item := pending[0]
	if item.ReservationID != reservationID || item.Record.RequestID != input.RequestID ||
		item.Record.CreditsMicro != 500 || item.Record.Status != UsageStatusError ||
		item.Record.PriceSnapshot == nil || item.Attempts != 0 {
		t.Fatalf("待结算记录不符: %+v", item)
	}
	if count, err := s.CountPendingSettlements(ctx, PendingSettlementPending); err != nil || count != 1 {
		t.Fatalf("待结算计数不符: %d %v", count, err)
	}
	next := now.Add(time.Minute)
	if err := s.MarkPendingSettlementRetry(ctx, reservationID, 1, "db unavailable", next); err != nil {
		t.Fatal(err)
	}
	dueNow, _ := s.ListPendingSettlements(ctx, PendingSettlementFilter{
		Status: PendingSettlementPending, DueBefore: &now,
	})
	if len(dueNow) != 0 {
		t.Fatalf("未到重试时间不应被取出: %+v", dueNow)
	}
	dueLater, _ := s.ListPendingSettlements(ctx, PendingSettlementFilter{
		Status: PendingSettlementPending, DueBefore: &next,
	})
	if len(dueLater) != 1 || dueLater[0].Attempts != 1 || dueLater[0].LastError != "db unavailable" {
		t.Fatalf("重试状态不符: %+v", dueLater)
	}
	if err := s.MarkPendingSettlementApplied(ctx, reservationID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if count, err := s.CountPendingSettlements(ctx, PendingSettlementPending); err != nil || count != 0 {
		t.Fatalf("补偿完成后不应再有 pending: %d %v", count, err)
	}
	applied, err := s.ListPendingSettlements(ctx, PendingSettlementFilter{
		Status: PendingSettlementApplied, Limit: 10,
	})
	if err != nil || len(applied) != 1 {
		t.Fatalf("applied 状态不符: %d %v", len(applied), err)
	}
	// 已完成的补偿不能被后续幂等入队重置回 pending。
	if err := s.EnqueuePendingSettlement(ctx, reservationID, record); err != nil {
		t.Fatal(err)
	}
	if count, _ := s.CountPendingSettlements(ctx, PendingSettlementPending); count != 0 {
		t.Fatalf("重复入队不应重置已完成的补偿: %d", count)
	}
	// 预留行不存在时结算必须显式报错，不能静默丢弃 usage。
	if err := s.SettleReservation(ctx, "00000000-0000-4000-8000-999999999998", record); !errors.Is(err, ErrReservationNotFound) {
		t.Fatalf("不存在的预留应返回 ErrReservationNotFound: %v", err)
	}
}

func testUpstreamRoutes(t *testing.T, ctx context.Context, s PlatformStore) {
	suffix := fmt.Sprint(time.Now().UnixNano())
	providerA, err := s.CreateUpstreamProvider(ctx, CreateUpstreamProviderInput{
		Name: "route-a-" + suffix, BaseURL: "https://a.example.com", Protocol: ProtocolOpenAIChatCompletions,
	})
	if err != nil {
		t.Fatal(err)
	}
	providerB, err := s.CreateUpstreamProvider(ctx, CreateUpstreamProviderInput{
		Name: "route-b-" + suffix, BaseURL: "https://b.example.com", Protocol: ProtocolOpenAIChatCompletions,
	})
	if err != nil {
		t.Fatal(err)
	}
	modelID := "m-routes-" + suffix
	if _, err := s.UpsertModel(ctx, UpsertModelInput{
		ModelID: modelID, DisplayName: "Routes", InputPricePerMtok: "1", OutputPricePerMtok: "2",
	}); err != nil {
		t.Fatal(err)
	}
	routeA, err := s.CreateUpstreamRoute(ctx, CreateUpstreamRouteInput{
		ModelID: modelID, ProviderID: providerA.ID, UpstreamModel: "up-a", Priority: 100, Weight: 1, Enabled: true,
	})
	if err != nil || routeA == nil || routeA.ProviderName != providerA.Name {
		t.Fatalf("建路由 A: %v %+v", err, routeA)
	}
	routeB, err := s.CreateUpstreamRoute(ctx, CreateUpstreamRouteInput{
		ModelID: modelID, ProviderID: providerB.ID, UpstreamModel: "up-b", Priority: 10, Weight: 2, Enabled: true,
	})
	if err != nil || routeB == nil {
		t.Fatalf("建路由 B: %v", err)
	}
	if _, err := s.CreateUpstreamRoute(ctx, CreateUpstreamRouteInput{
		ModelID: modelID, ProviderID: providerB.ID, Priority: 1, Weight: 1, Enabled: true,
	}); err == nil {
		t.Fatal("同模型重复绑定同供应商应失败")
	}
	list, _ := s.ListUpstreamRoutes(ctx, modelID, true)
	if len(list) != 2 || list[0].ProviderID != providerB.ID || list[1].ProviderID != providerA.ID {
		t.Fatalf("路由应按 priority 升序: %+v", list)
	}
	disabled := false
	if updated, err := s.UpdateUpstreamRoute(ctx, routeB.ID, UpdateUpstreamRoutePatch{Enabled: &disabled, Weight: ptr(int64(5))}); err != nil ||
		updated == nil || updated.Enabled || updated.Weight != 5 {
		t.Fatalf("更新路由: %v %+v", err, updated)
	}
	if enabledList, _ := s.ListUpstreamRoutes(ctx, modelID, false); len(enabledList) != 1 {
		t.Fatalf("停用路由不应出现在启用列表: %+v", enabledList)
	}
	if deleted, _ := s.DeleteUpstreamRoute(ctx, routeA.ID); !deleted {
		t.Fatal("删除路由失败")
	}
	// 自动熔断 → 自动恢复。
	if tripped, _ := s.ReportUpstreamFailure(ctx, providerA.ID, "upstream_500", 1); !tripped {
		t.Fatal("阈值 1 应触发熔断")
	}
	if recovered, err := s.RecoverUpstreamProvider(ctx, providerA.ID, time.Now()); err != nil || !recovered {
		t.Fatalf("自动熔断应可恢复: %v %v", err, recovered)
	}
	if recovered, _ := s.RecoverUpstreamProvider(ctx, providerA.ID, time.Now()); recovered {
		t.Fatal("已启用供应商不应再次恢复")
	}
	if provider, _ := s.GetUpstreamProvider(ctx, providerA.ID); !provider.Enabled || provider.DisabledMode != "" {
		t.Fatalf("恢复后状态不符: %+v", provider)
	}
	_, _ = s.DeleteModel(ctx, modelID)
}

func testOpsAlerts(t *testing.T, ctx context.Context, s PlatformStore) {
	u := mustUser(t, ctx, s, "ops@test.local")
	suffix := fmt.Sprint(time.Now().UnixNano())
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "运维团队-" + suffix, QuotaCredits: "100.000000"})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Millisecond)
	for i, latency := range []int64{100, 300} {
		latencyCopy := latency
		if err := s.InsertUsage(ctx, UsageRecord{
			ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", 700+i), RequestID: fmt.Sprintf("ops-%s-%d", suffix, i),
			UserID: u.ID, TeamID: team.ID, Model: "m", UpstreamModel: "m",
			CreditsMicro: 1_000_000, Status: UsageStatusOk, LatencyMs: &latencyCopy,
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	overview, err := s.OpsOverview(ctx, base.Add(-time.Minute))
	if err != nil || overview.Requests != 2 || overview.SuccessRequests != 2 || overview.CreditsMicro != 2_000_000 {
		t.Fatalf("ops overview 不符: %v %+v", err, overview)
	}
	if overview.P50LatencyMs == nil || *overview.P50LatencyMs != 200 {
		t.Fatalf("P50 不符: %+v", overview.P50LatencyMs)
	}
	if overview.P95LatencyMs == nil || *overview.P95LatencyMs < 289 || *overview.P95LatencyMs > 291 {
		t.Fatalf("P95 不符: %+v", overview.P95LatencyMs)
	}
	points, err := s.OpsSeries(ctx, base.Add(-time.Minute), 60)
	if err != nil || len(points) != 1 || points[0].Requests != 2 {
		t.Fatalf("ops series 不符: %v %+v", err, points)
	}
	rule, err := s.UpsertAlertRule(ctx, UpsertAlertRuleInput{
		Name: "错误率-" + suffix, Metric: "error_rate", Threshold: "0.100000", WindowMinutes: 15, Enabled: true,
	})
	if err != nil || rule == nil {
		t.Fatalf("建告警规则: %v", err)
	}
	event, err := s.InsertAlertEvent(ctx, *rule, "0.500000", "错误率超过阈值", time.Now().UTC())
	if err != nil || event == nil || event.Status != "open" || event.RuleName != rule.Name {
		t.Fatalf("写告警事件: %v %+v", err, event)
	}
	if open, _ := s.OpenAlertEventForRule(ctx, rule.ID); open == nil || open.ID != event.ID {
		t.Fatal("应查到未处理告警")
	}
	if resolved, err := s.ResolveAlertEvent(ctx, event.ID, time.Now().UTC()); err != nil || !resolved {
		t.Fatalf("处理告警失败: %v", err)
	}
	if open, _ := s.OpenAlertEventForRule(ctx, rule.ID); open != nil {
		t.Fatal("处理后不应再查到未处理告警")
	}
	if events, _ := s.ListAlertEvents(ctx, "resolved", 10); len(events) == 0 {
		t.Fatal("应能按状态查询告警事件")
	}
	if deleted, err := s.DeleteAlertRule(ctx, rule.ID); err != nil || !deleted {
		t.Fatalf("删除告警规则失败: %v", err)
	}
}

func testDeviceTokens(t *testing.T, ctx context.Context, s PlatformStore) {
	u := mustUser(t, ctx, s, "device@test.local")
	now := time.Now().UTC().Truncate(time.Millisecond)
	token := DeviceTokenRecord{
		ID: "00000000-0000-4000-8000-000000000201", UserID: u.ID, Name: "笔记本",
		Platform: "darwin", TokenHash: "th-1", TokenCiphertext: "v1.aa.bb.cc", TokenHint: "n123",
		CreatedAt: now,
	}
	if err := s.CreateDeviceToken(ctx, token); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FindDeviceTokenByHash(ctx, "th-1"); got == nil || got.Name != "笔记本" || got.Platform != "darwin" {
		t.Fatalf("按哈希查设备: %+v", got)
	}
	if got, _ := s.FindDeviceTokenByID(ctx, token.ID); got == nil || got.TokenHash != "th-1" {
		t.Fatal("按 id 查设备失败")
	}
	later := now.Add(time.Hour)
	if err := s.TouchDeviceToken(ctx, token.ID, later); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FindDeviceTokenByID(ctx, token.ID); got.LastUsedAt == nil || !got.LastUsedAt.Equal(later) {
		t.Fatal("touch 设备失败")
	}
	if err := s.RevokeDeviceToken(ctx, token.ID, later); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FindDeviceTokenByID(ctx, token.ID); got.RevokedAt == nil {
		t.Fatal("吊销失败")
	}
	// 批量吊销只统计新吊销条数。
	second := DeviceTokenRecord{
		ID: "00000000-0000-4000-8000-000000000202", UserID: u.ID, Name: "台式机",
		TokenHash: "th-2", TokenCiphertext: "v1.aa.bb.cc", TokenHint: "n456", CreatedAt: now,
	}
	if err := s.CreateDeviceToken(ctx, second); err != nil {
		t.Fatal(err)
	}
	n, err := s.RevokeDeviceTokensForUser(ctx, u.ID, later)
	if err != nil || n != 1 {
		t.Fatalf("批量吊销应只算新吊销 1 条: %d %v", n, err)
	}
	if list, _ := s.ListDeviceTokens(ctx, u.ID); len(list) != 2 || list[0].CreatedAt.Before(list[1].CreatedAt) {
		t.Fatal("设备列表应按创建时间倒序")
	}
}

func testLoginFlows(t *testing.T, ctx context.Context, s PlatformStore) {
	u := mustUser(t, ctx, s, "flow@test.local")
	now := time.Now().UTC().Truncate(time.Millisecond)
	flow := LoginFlowRecord{
		ID: "00000000-0000-4000-8000-000000000301", PollTokenHash: "poll-hash",
		Status: FlowStatusPending, DeviceName: "测试机", DevicePlatform: "linux",
		CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	}
	if err := s.CreateLoginFlow(ctx, flow); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetLoginFlow(ctx, flow.ID); got == nil || got.Status != FlowStatusPending {
		t.Fatalf("查登录流: %+v", got)
	}
	// 条件转换：状态不匹配时不得覆盖（authorize 与 poll 的竞态边界）。
	if got, err := s.UpdateLoginFlowIfStatus(ctx, flow.ID, FlowStatusAuthorized,
		UpdateLoginFlowPatch{Status: ptr(FlowStatusDenied)}); err != nil || got != nil {
		t.Fatalf("状态不匹配不应更新: %v %+v", err, got)
	}
	userID := u.ID
	authorizedAt := now.Add(time.Second)
	updated, err := s.UpdateLoginFlowIfStatus(ctx, flow.ID, FlowStatusPending,
		UpdateLoginFlowPatch{Status: ptr(FlowStatusAuthorized), UserID: &userID, AuthorizedAt: &authorizedAt})
	if err != nil || updated == nil || updated.Status != FlowStatusAuthorized || updated.UserID != u.ID || updated.AuthorizedAt == nil {
		t.Fatalf("授权登录流: %v %+v", err, updated)
	}
	// 旧授权请求（仍以为状态是 pending）不得把已授权流程改回 denied。
	if got, err := s.UpdateLoginFlowIfStatus(ctx, flow.ID, FlowStatusPending,
		UpdateLoginFlowPatch{Status: ptr(FlowStatusDenied)}); err != nil || got != nil {
		t.Fatalf("过期状态转换不应覆盖: %v %+v", err, got)
	}
	if current, _ := s.GetLoginFlow(ctx, flow.ID); current == nil || current.Status != FlowStatusAuthorized {
		t.Fatalf("授权状态被旧请求覆盖: %+v", current)
	}
	// 条件消费:并发只允许一个成功。
	var consumed int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.ConsumeLoginFlow(ctx, flow.ID, now.Add(2*time.Second))
			if err != nil {
				t.Errorf("消费登录流错误: %v", err)
				return
			}
			if got != nil {
				atomic.AddInt64(&consumed, 1)
			}
		}()
	}
	wg.Wait()
	if consumed != 1 {
		t.Fatalf("登录流只能被消费一次,实际成功 %d", consumed)
	}
	if got, _ := s.GetLoginFlow(ctx, flow.ID); got.Status != FlowStatusConsumed || got.ConsumedAt == nil {
		t.Fatalf("消费后状态不符: %+v", got)
	}
	polledAt := now.Add(3 * time.Second)
	if got, _ := s.UpdateLoginFlow(ctx, flow.ID, UpdateLoginFlowPatch{LastPolledAt: &polledAt}); got.LastPolledAt == nil || !got.LastPolledAt.Equal(polledAt) {
		t.Fatal("轮询时间更新失败")
	}
	// 密码重置/禁用时作废未消费登录流：pending + authorized 都失效，consumed 保持原状。
	for _, id := range []string{
		"00000000-0000-4000-8000-000000000303",
		"00000000-0000-4000-8000-000000000304",
	} {
		record := LoginFlowRecord{
			ID: id, PollTokenHash: "revoke-hash-" + id, Status: FlowStatusPending,
			DeviceName: "待撤销机", CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
		}
		if err := s.CreateLoginFlow(ctx, record); err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdateLoginFlowIfStatus(ctx, id, FlowStatusPending,
			UpdateLoginFlowPatch{Status: ptr(FlowStatusAuthorized), UserID: &userID}); err != nil {
			t.Fatal(err)
		}
	}
	consumedFlow := LoginFlowRecord{
		ID: "00000000-0000-4000-8000-000000000305", PollTokenHash: "revoke-hash-consumed",
		Status: FlowStatusPending, DeviceName: "已消费机", CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	}
	if err := s.CreateLoginFlow(ctx, consumedFlow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateLoginFlowIfStatus(ctx, consumedFlow.ID, FlowStatusPending,
		UpdateLoginFlowPatch{Status: ptr(FlowStatusAuthorized), UserID: &userID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeLoginFlow(ctx, consumedFlow.ID, now); err != nil {
		t.Fatal(err)
	}
	if count, err := s.RevokeLoginFlowsForUser(ctx, userID, now); err != nil || count != 2 {
		t.Fatalf("应作废 2 条未消费登录流: %d %v", count, err)
	}
	for _, id := range []string{
		"00000000-0000-4000-8000-000000000303",
		"00000000-0000-4000-8000-000000000304",
	} {
		got, err := s.GetLoginFlow(ctx, id)
		if err != nil || got == nil || got.Status != FlowStatusExpired {
			t.Fatalf("未消费登录流应已作废: %v %+v", err, got)
		}
	}
	if got, _ := s.GetLoginFlow(ctx, consumedFlow.ID); got == nil || got.Status != FlowStatusConsumed {
		t.Fatalf("已消费登录流不应被改状态: %+v", got)
	}

	// 过期登录流清理。
	expiredFlow := LoginFlowRecord{
		ID: "00000000-0000-4000-8000-000000000302", PollTokenHash: "poll-hash-2",
		Status: FlowStatusExpired, DeviceName: "过期机", CreatedAt: now.Add(-2 * time.Hour),
		ExpiresAt: now.Add(-time.Hour),
	}
	if err := s.CreateLoginFlow(ctx, expiredFlow); err != nil {
		t.Fatal(err)
	}
	if count, err := s.DeleteExpiredLoginFlows(ctx, now); err != nil || count < 1 {
		t.Fatalf("过期登录流清理: %d %v", count, err)
	}
}

func testReleases(t *testing.T, ctx context.Context, s PlatformStore) {
	content := []byte(`{"schemaVersion":1,"revision":1,"config":{"providerConfigRules":{"templateRules":[],"providerRules":[]}}}`)
	first, err := s.InsertRelease(ctx, InsertReleaseInput{Revision: 1, Content: content})
	if err != nil || !first.IsActive {
		t.Fatalf("首个 Release 应自动激活: %v %+v", err, first)
	}
	second, err := s.InsertRelease(ctx, InsertReleaseInput{Revision: 2, Content: content})
	if err != nil || second.IsActive {
		t.Fatalf("第二个 Release 不应激活: %+v", second)
	}
	if _, err := s.InsertRelease(ctx, InsertReleaseInput{Revision: 2, Content: content}); err == nil {
		t.Fatal("重复 revision 应报错")
	}
	activated, err := s.ActivateRelease(ctx, 2)
	if err != nil || activated == nil || !activated.IsActive {
		t.Fatalf("激活失败: %v %+v", err, activated)
	}
	if got, _ := s.GetRelease(ctx, 1); got.IsActive {
		t.Fatal("激活新版本后旧版本应失活")
	}
	if got, _ := s.GetActiveRelease(ctx); got == nil || got.Revision != 2 {
		t.Fatal("活动 Release 应为 2")
	}
	if max, _ := s.GetMaxReleaseRevision(ctx); max == nil || *max != 2 {
		t.Fatalf("最大 revision: %v", max)
	}
	if list, _ := s.ListReleases(ctx); len(list) != 2 || list[0].Revision != 2 {
		t.Fatal("Release 列表应 revision 倒序")
	}
	if missing, _ := s.ActivateRelease(ctx, 99); missing != nil {
		t.Fatal("激活不存在的 revision 应返回 nil")
	}
}

func sameJSONBytes(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func testModels(t *testing.T, ctx context.Context, s PlatformStore) {
	model, err := s.UpsertModel(ctx, UpsertModelInput{
		ModelID: "m-test", DisplayName: "M", InputPricePerMtok: "3", OutputPricePerMtok: "15",
	})
	if err != nil || model.CacheReadPricePerMtok != "0.000000" || !model.Enabled || model.SortOrder != 0 {
		t.Fatalf("新模型默认值不符: %v %+v", err, model)
	}
	if model.OffPeakMultiplier != "1.000000" {
		t.Fatalf("空闲倍率默认应为 1: %+v", model)
	}
	if model.ConfigMode != "recommended" || len(model.ModelConfig) != 0 {
		t.Fatalf("模型配置默认应跟随官方推荐: %+v", model)
	}
	manualConfig := []byte(`{"properties":{"contextWindow":200000}}`)
	// 存储层按原始 JSONB 返回；调用方（API）写入的是紧凑 JSON，测试保持同一口径。
	if updated, err := s.SetModelConfig(ctx, "m-test", "manual", manualConfig); err != nil ||
		updated == nil || updated.ConfigMode != "manual" || !sameJSONBytes(updated.ModelConfig, manualConfig) {
		t.Fatalf("手动模型配置写入失败: %v %+v", err, updated)
	}
	// Upsert 只改目录字段，不能丢失能力配置。
	if _, err := s.UpsertModel(ctx, UpsertModelInput{
		ModelID: "m-test", DisplayName: "M-config", InputPricePerMtok: "3", OutputPricePerMtok: "15",
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetModel(ctx, "m-test"); got == nil || got.ConfigMode != "manual" || !sameJSONBytes(got.ModelConfig, manualConfig) {
		t.Fatalf("Upsert 应保留模型配置: %+v", got)
	}
	if updated, err := s.SetModelConfig(ctx, "m-test", "recommended", nil); err != nil ||
		updated == nil || updated.ConfigMode != "recommended" || len(updated.ModelConfig) != 0 {
		t.Fatalf("恢复官方配置失败: %v %+v", err, updated)
	}
	if missing, err := s.SetModelConfig(ctx, "missing", "manual", manualConfig); err != nil || missing != nil {
		t.Fatalf("不存在模型应返回 nil: %v %+v", err, missing)
	}
	offPeak := "0.500000"
	if updated, err := s.UpsertModel(ctx, UpsertModelInput{
		ModelID: "m-test", DisplayName: "M", InputPricePerMtok: "3", OutputPricePerMtok: "15",
		OffPeakMultiplier: &offPeak,
	}); err != nil || updated.OffPeakMultiplier != "0.500000" {
		t.Fatalf("空闲倍率写入失败: %v %+v", err, updated)
	}
	// 绑定字段:缺省保持。
	provider, _ := s.CreateUpstreamProvider(ctx, CreateUpstreamProviderInput{
		Name: "prov-" + fmt.Sprint(time.Now().UnixNano()), BaseURL: "https://up.example.com", Protocol: ProtocolAnthropicMessages,
	})
	if _, err := s.UpsertModel(ctx, UpsertModelInput{
		ModelID: "m-test", DisplayName: "M", InputPricePerMtok: "3", OutputPricePerMtok: "15",
		ProvideUpstreamModel: true, UpstreamModel: "real-upstream",
		ProvideUpstreamProviderID: true, UpstreamProviderID: provider.ID,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetModel(ctx, "m-test")
	if got.UpstreamModel != "real-upstream" || got.UpstreamProviderID != provider.ID {
		t.Fatalf("绑定写入失败: %+v", got)
	}
	// 未提供绑定字段 → 保持;提供空 → 清除。
	if _, err := s.UpsertModel(ctx, UpsertModelInput{
		ModelID: "m-test", DisplayName: "M2", InputPricePerMtok: "4", OutputPricePerMtok: "16",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetModel(ctx, "m-test")
	if got.DisplayName != "M2" || got.UpstreamModel != "real-upstream" {
		t.Fatalf("缺省绑定应保持: %+v", got)
	}
	if _, err := s.UpsertModel(ctx, UpsertModelInput{
		ModelID: "m-test", DisplayName: "M2", InputPricePerMtok: "4", OutputPricePerMtok: "16",
		ProvideUpstreamModel: true, ProvideUpstreamProviderID: true,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetModel(ctx, "m-test")
	if got.UpstreamModel != "" || got.UpstreamProviderID != "" {
		t.Fatalf("显式空绑定应清除: %+v", got)
	}
	// Postgres 口径:可选字段缺省按 EXCLUDED 默认覆盖(非保留旧值)。
	disabled := false
	sortOrder := int64(9)
	crPrice, cwPrice := "0.3", "3.75"
	if _, err := s.UpsertModel(ctx, UpsertModelInput{
		ModelID: "m-test", DisplayName: "M3", InputPricePerMtok: "5", OutputPricePerMtok: "17",
		CacheReadPricePerMtok: &crPrice, CacheWritePricePerMtok: &cwPrice,
		Enabled: &disabled, SortOrder: &sortOrder,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetModel(ctx, "m-test")
	if got.CacheReadPricePerMtok != "0.300000" || got.CacheWritePricePerMtok != "3.750000" || got.Enabled || got.SortOrder != 9 {
		t.Fatalf("显式字段写入不符: %+v", got)
	}
	if _, err := s.UpsertModel(ctx, UpsertModelInput{
		ModelID: "m-test", DisplayName: "M4", InputPricePerMtok: "6", OutputPricePerMtok: "18",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetModel(ctx, "m-test")
	if got.CacheReadPricePerMtok != "0.000000" || !got.Enabled || got.SortOrder != 0 {
		t.Fatalf("缺省可选字段应按 PG 口径回落默认值: %+v", got)
	}
	if list, _ := s.ListModels(ctx, false); len(list) != 1 {
		t.Fatal("默认只列启用模型")
	}
	if deleted, _ := s.DeleteModel(ctx, "m-test"); !deleted {
		t.Fatal("删除模型失败")
	}
	if deleted, _ := s.DeleteModel(ctx, "m-test"); deleted {
		t.Fatal("重复删除应为 false")
	}
}

func testProviders(t *testing.T, ctx context.Context, s PlatformStore) {
	name := "供应商-" + fmt.Sprint(time.Now().UnixNano())
	p, err := s.CreateUpstreamProvider(ctx, CreateUpstreamProviderInput{
		Name: name, BaseURL: "https://a.example.com", Protocol: ProtocolOpenAIChatCompletions,
		APIKeyCiphertext: "v1.iv.ct.tag", APIKeyHint: "abcd",
	})
	if err != nil || p.Enabled != true || p.ConsecutiveFailures != 0 || p.DisabledMode != "" {
		t.Fatalf("建供应商: %v %+v", err, p)
	}
	if _, err := s.CreateUpstreamProvider(ctx, CreateUpstreamProviderInput{
		Name: name, BaseURL: "https://b.example.com", Protocol: ProtocolAnthropicMessages,
	}); !errors.Is(err, ErrProviderNameTaken) {
		t.Fatalf("重名供应商应 ErrProviderNameTaken: %v", err)
	}
	// patch 语义:缺省保持;Provide 才覆盖。
	newBase := "https://b.example.com"
	version := "2023-06-01"
	timeout := int64(30000)
	updated, err := s.UpdateUpstreamProvider(ctx, p.ID, UpdateUpstreamProviderPatch{
		BaseURL: &newBase, ProvideAnthropicVersion: true, AnthropicVersion: &version, ProvideTimeoutMs: true, TimeoutMs: &timeout,
	})
	if err != nil || updated.BaseURL != newBase || updated.AnthropicVersion != version || updated.TimeoutMs != 30000 {
		t.Fatalf("更新供应商: %v %+v", err, updated)
	}
	if updated.APIKeyCiphertext != "v1.iv.ct.tag" || updated.APIKeyHint != "abcd" {
		t.Fatal("未提供 apiKey 应保持原值")
	}
	// 清除 anthropicVersion/timeout。
	if updated, _ = s.UpdateUpstreamProvider(ctx, p.ID, UpdateUpstreamProviderPatch{ProvideAnthropicVersion: true, ProvideTimeoutMs: true}); updated.AnthropicVersion != "" || updated.TimeoutMs != 0 {
		t.Fatalf("显式清除失败: %+v", updated)
	}
	status := int64(200)
	model := "claude-x"
	okTrue := true
	if err := s.RecordProviderTestResult(ctx, p.ID, ProviderTestOutcome{
		OK: okTrue, Kind: "ok", Status: &status, LatencyMs: 321, Model: model, Detail: "fine",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetUpstreamProvider(ctx, p.ID)
	if got.LastTestOk == nil || !*got.LastTestOk || got.LastTestStatus == nil || *got.LastTestStatus != 200 ||
		got.LastTestModel != model || got.LastTestLatencyMs == nil || *got.LastTestLatencyMs != 321 || got.LastTestAt == nil {
		t.Fatalf("测试结果落库不符: %+v", got)
	}
	// 熔断:阈值 3,前两次不触发,第三次触发 auto 停用;0=关闭。
	if tripped, _ := s.ReportUpstreamFailure(ctx, p.ID, "upstream_502", 3); tripped {
		t.Fatal("第一次失败不应熔断")
	}
	if tripped, _ := s.ReportUpstreamFailure(ctx, p.ID, "upstream_502", 3); tripped {
		t.Fatal("第二次失败不应熔断")
	}
	if tripped, _ := s.ReportUpstreamFailure(ctx, p.ID, "upstream_502", 3); !tripped {
		t.Fatal("第三次失败应熔断")
	}
	got, _ = s.GetUpstreamProvider(ctx, p.ID)
	if got.Enabled || got.DisabledMode != "auto" || got.AutoDisabledReason != "upstream_502" || got.AutoDisabledAt == nil {
		t.Fatalf("熔断现场不符: %+v", got)
	}
	// 已停用的行不再计数、不覆盖人工决定。
	if tripped, _ := s.ReportUpstreamFailure(ctx, p.ID, "again", 3); tripped {
		t.Fatal("已停用的供应商不应再触发")
	}
	if list, _ := s.ListUpstreamProviders(ctx, false); containsProvider(list, p.ID) {
		t.Fatal("停用供应商不应出现在启用列表")
	}
	// 成功重置计数。
	enabledTrue := true
	if _, err := s.UpdateUpstreamProvider(ctx, p.ID, UpdateUpstreamProviderPatch{Enabled: &enabledTrue}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReportUpstreamSuccess(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetUpstreamProvider(ctx, p.ID)
	if got.ConsecutiveFailures != 0 || !got.Enabled {
		t.Fatalf("成功后计数应清零: %+v", got)
	}
	// threshold=0 关闭熔断:连续失败也不停用。
	for i := 0; i < 5; i++ {
		if tripped, _ := s.ReportUpstreamFailure(ctx, p.ID, "x", 0); tripped {
			t.Fatal("threshold=0 不应熔断")
		}
	}
	if got, _ := s.GetUpstreamProvider(ctx, p.ID); !got.Enabled {
		t.Fatal("threshold=0 应保持启用")
	}
	if deleted, _ := s.DeleteUpstreamProvider(ctx, p.ID); !deleted {
		t.Fatal("删除供应商失败")
	}
}

func containsProvider(list []UpstreamProviderRecord, id string) bool {
	for _, provider := range list {
		if provider.ID == id {
			return true
		}
	}
	return false
}

func testPricing(t *testing.T, ctx context.Context, s PlatformStore) {
	if snapshot, _ := s.GetModelPricing(ctx); snapshot != nil {
		t.Fatal("初始定价库应为空")
	}
	entries := []PricingEntry{
		{
			ModelID: "claude-haiku-4-5", DisplayName: "Haiku",
			InputCostPerMillion: "1", OutputCostPerMillion: "5",
			CacheReadCostPerMillion: "0.1", CacheCreationCostPerMillion: "1.25",
		},
		{
			ModelID: "claude-sonnet-4-5", DisplayName: "Sonnet",
			InputCostPerMillion: "3", OutputCostPerMillion: "15",
			CacheReadCostPerMillion: "0.3", CacheCreationCostPerMillion: "3.75",
		},
	}
	saved, err := s.SaveModelPricing(ctx, entries, pricing.EmptyVersion)
	if err != nil || saved == nil {
		t.Fatalf("首次保存: %v %+v", err, saved)
	}
	if saved.Version != pricing.ComputeDocVersion(entries) {
		t.Fatalf("保存返回版本不符: %s", saved.Version)
	}
	got, _ := s.GetModelPricing(ctx)
	if got == nil || len(got.Entries) != 2 || got.Version != saved.Version {
		t.Fatalf("读取定价库: %+v", got)
	}
	// 陈旧版本 → 冲突(null),不覆盖。
	if stale, _ := s.SaveModelPricing(ctx, entries[:1], pricing.EmptyVersion); stale != nil {
		t.Fatal("陈旧版本应返回 nil 冲突")
	}
	// 正确版本 → 成功。
	if updated, _ := s.SaveModelPricing(ctx, entries[:1], saved.Version); updated == nil {
		t.Fatal("匹配版本应保存成功")
	}
}

func testAudit(t *testing.T, ctx context.Context, s PlatformStore) {
	actor := mustUser(t, ctx, s, "actor@test.local")
	if err := s.InsertAuditLog(ctx, InsertAuditLogInput{
		ActorUserID: actor.ID, Action: "user.update", TargetType: "user", TargetID: "u-1",
		Detail: []byte(`{"changed":"display_name"}`),
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := s.InsertAuditLog(ctx, InsertAuditLogInput{ActorUserID: actor.ID, Action: "provider.delete", Detail: []byte(`null`)}); err != nil {
		t.Fatal(err)
	}
	logs, _ := s.ListAuditLogs(ctx, AuditLogFilter{ActorUserID: actor.ID})
	if len(logs) != 2 || logs[0].ActorEmail != "actor@test.local" || logs[1].TargetID != "u-1" {
		t.Fatalf("审计过滤/邮箱 join: %+v", logs)
	}
	all, _ := s.ListAuditLogs(ctx, AuditLogFilter{ActorUserID: actor.ID})
	if len(all) != 2 || all[0].Action != "provider.delete" {
		t.Fatalf("审计列表应按时间倒序: %+v", all)
	}
	cursor := all[0]
	page2, _ := s.ListAuditLogs(ctx, AuditLogFilter{
		ActorUserID: actor.ID, CursorCreatedAt: &cursor.CreatedAt, CursorID: cursor.ID,
	})
	if len(page2) != 1 || page2[0].Action != "user.update" {
		t.Fatalf("审计游标翻页: %+v", page2)
	}
	team, _ := s.CreateTeam(ctx, CreateTeamInput{Name: "审计团队-" + fmt.Sprint(time.Now().UnixNano()), QuotaCredits: "10"})
	if err := s.InsertQuotaAdjustment(ctx, InsertQuotaAdjustmentInput{
		TeamID: team.ID, ActorUserID: actor.ID, Action: "set_quota",
		OldQuotaCredits: "10.000000", NewQuotaCredits: "20.000000", OldUnlimited: false, NewUnlimited: true,
	}); err != nil {
		t.Fatal(err)
	}
	adjusts, _ := s.ListQuotaAdjustments(ctx, team.ID, 10)
	if len(adjusts) != 1 || adjusts[0].NewQuotaCredits != "20.000000" || !adjusts[0].NewUnlimited || adjusts[0].ActorEmail != "actor@test.local" {
		t.Fatalf("额度流水: %+v", adjusts)
	}
}

func testConfigKV(t *testing.T, ctx context.Context, s PlatformStore) {
	if value, _ := s.GetConfigValue(ctx, "missing"); value != nil {
		t.Fatal("缺省键应为 nil")
	}
	if err := s.SetConfigValue(ctx, "help", []byte(`{"feedback_url":"https://x"}`)); err != nil {
		t.Fatal(err)
	}
	// jsonb 文本格式由 Postgres 规范化(空格/键序),消费方按 JSON 语义解读;
	// 这里做语义比较,不逐字节比较。
	assertJSONStringSemantics(t, mustGetKV(t, ctx, s, "help"), `{"feedback_url":"https://x"}`)
	if err := s.SetConfigValue(ctx, "help", []byte(`{"feedback_url":"https://y"}`)); err != nil {
		t.Fatal(err)
	}
	assertJSONStringSemantics(t, mustGetKV(t, ctx, s, "help"), `{"feedback_url":"https://y"}`)
}

func mustGetKV(t *testing.T, ctx context.Context, s PlatformStore, key string) []byte {
	t.Helper()
	value, err := s.GetConfigValue(ctx, key)
	if err != nil {
		t.Fatalf("KV 读取: %v", err)
	}
	return value
}

func assertJSONStringSemantics(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("KV 结果不是合法 JSON: %s (%v)", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatal(err)
	}
	gotNorm, _ := json.Marshal(gotValue)
	wantNorm, _ := json.Marshal(wantValue)
	if string(gotNorm) != string(wantNorm) {
		t.Fatalf("KV 语义不符: got %s want %s", got, want)
	}
}

func testUsage(t *testing.T, ctx context.Context, s PlatformStore) {
	u := mustUser(t, ctx, s, "usage@test.local")
	team, _ := s.CreateTeam(ctx, CreateTeamInput{Name: "用量团队-" + fmt.Sprint(time.Now().UnixNano()), QuotaCredits: "100"})
	base := time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		latency := int64(100 + i)
		var snapshot *UsagePricingSnapshot
		if i < 2 {
			snapshot = &UsagePricingSnapshot{
				SchemaVersion: 1, CapturedAt: base,
				Cost: UsageCostSnapshot{
					Status: "estimated", EstimatedMicro: ptr(int64(40 + i*20)),
					UsageEstimated: i == 0,
					Rates:          &UsagePriceSnapshot{Currency: "CNY"},
				},
			}
		}
		if err := s.InsertUsage(ctx, UsageRecord{
			ID:        fmt.Sprintf("00000000-0000-4000-8000-%012d", 400+i),
			RequestID: fmt.Sprintf("req-%d", i),
			UserID:    u.ID, TeamID: team.ID, Model: "m-a", UpstreamModel: "m-a-up",
			InputTokens: 10, OutputTokens: 20, CreditsMicro: int64(100 * (i + 1)),
			Status: UsageStatusOk, ClientIP: "198.51.100.9", LatencyMs: &latency,
			PriceSnapshot: snapshot,
			CreatedAt:     base.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InsertUsage(ctx, UsageRecord{
		ID: "00000000-0000-4000-8000-000000000500", RequestID: "req-err",
		UserID: u.ID, TeamID: team.ID, Model: "m-b", UpstreamModel: "m-b-up",
		CreditsMicro: 50, Status: UsageStatusError, ErrorType: "upstream_500",
		PriceSnapshot: &UsagePricingSnapshot{
			SchemaVersion: 1, CapturedAt: base,
			Cost: UsageCostSnapshot{
				Status: "estimated", EstimatedMicro: ptr(int64(25)),
				Rates: &UsagePriceSnapshot{Currency: "USD"},
			},
		},
		CreatedAt: base.Add(10 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	all, _ := s.ListUsage(ctx, UsageFilter{TeamID: team.ID})
	if len(all) != 6 || all[0].RequestID != "req-err" {
		t.Fatalf("用量明细(时间倒序): %d", len(all))
	}
	if got, _ := s.ListUsage(ctx, UsageFilter{TeamID: team.ID, Status: UsageStatusError}); len(got) != 1 || got[0].ErrorType != "upstream_500" {
		t.Fatal("状态过滤失败")
	}
	if got, _ := s.ListUsage(ctx, UsageFilter{TeamID: team.ID, Model: "m-a"}); len(got) != 5 {
		t.Fatal("模型过滤失败")
	}
	from := base.Add(90 * time.Minute)
	if got, _ := s.ListUsage(ctx, UsageFilter{TeamID: team.ID, From: &from}); len(got) != 4 {
		t.Fatalf("起始时间过滤失败: %d", len(got))
	}
	if got, _ := s.ListUsage(ctx, UsageFilter{TeamID: team.ID, Limit: ptr(int64(2))}); len(got) != 2 {
		t.Fatal("limit 失败")
	}
	agg, _ := s.AggregateUsage(ctx, UsageFilter{TeamID: team.ID})
	if len(agg) != 2 || agg[0].Model != "m-a" || agg[0].Requests != 5 ||
		agg[0].InputTokens != 50 || agg[0].OutputTokens != 100 ||
		agg[0].CreditsMicro != 1500 || agg[0].LatencyMsSum != 510 {
		t.Fatalf("聚合不符: %+v", agg)
	}
	costSummary, err := s.AggregateUsageCosts(ctx, UsageFilter{TeamID: team.ID})
	if err != nil {
		t.Fatalf("成本汇总失败: %v", err)
	}
	if costSummary.EstimatedRequests != 3 || costSummary.MissingCostRequests != 3 ||
		costSummary.EstimatedUsageRequests != 1 || len(costSummary.Groups) != 2 ||
		len(costSummary.Totals) != 2 {
		t.Fatalf("成本汇总计数不符: %+v", costSummary)
	}
	if costSummary.Groups[0].Model != "m-a" || costSummary.Groups[0].Currency != "CNY" ||
		costSummary.Groups[0].Requests != 2 || costSummary.Groups[0].CostMicro != 100 ||
		costSummary.Groups[0].ChargedMicro != 300 || costSummary.Groups[0].EstimatedUsageRequests != 1 {
		t.Fatalf("CNY 成本分组不符: %+v", costSummary.Groups[0])
	}
	if costSummary.Groups[1].Model != "m-b" || costSummary.Groups[1].Currency != "USD" ||
		costSummary.Groups[1].Requests != 1 || costSummary.Groups[1].CostMicro != 25 ||
		costSummary.Groups[1].ChargedMicro != 50 {
		t.Fatalf("USD 成本分组不符: %+v", costSummary.Groups[1])
	}
	if costSummary.Totals[0].Currency != "CNY" || costSummary.Totals[0].Requests != 2 ||
		costSummary.Totals[0].CostMicro != 100 || costSummary.Totals[0].ChargedMicro != 300 ||
		costSummary.Totals[1].Currency != "USD" || costSummary.Totals[1].CostMicro != 25 {
		t.Fatalf("成本合计不符: %+v", costSummary.Totals)
	}
	filteredCosts, err := s.AggregateUsageCosts(ctx, UsageFilter{TeamID: team.ID, From: &from})
	if err != nil || filteredCosts.EstimatedRequests != 1 || filteredCosts.MissingCostRequests != 3 ||
		len(filteredCosts.Groups) != 1 || filteredCosts.Groups[0].Currency != "USD" {
		t.Fatalf("成本汇总范围过滤不符: %v %+v", err, filteredCosts)
	}
	day, _ := s.AggregateUsageSeries(ctx, UsageFilter{TeamID: team.ID}, SeriesGranularityDay)
	// base=01:00Z(+8 → 09:00) 当天,10:00Z→18:00 当天:全部同一天。
	if len(day) != 1 || day[0].BucketStart != "2026-10-18T00:00:00" || day[0].Requests != 6 {
		t.Fatalf("日桶不符: %+v", day)
	}
	hour, _ := s.AggregateUsageSeries(ctx, UsageFilter{TeamID: team.ID}, SeriesGranularityHour)
	if len(hour) != 6 || hour[0].BucketStart != "2026-10-18T09:00:00" {
		t.Fatalf("小时桶不符: %+v", hour)
	}
	// 全局趋势走 rollup（PG）或明细分桶（内存），累计值必须与全局聚合一致。
	globalAgg, _ := s.AggregateUsage(ctx, UsageFilter{})
	var globalRequests, globalCredits int64
	for _, row := range globalAgg {
		globalRequests += row.Requests
		globalCredits += row.CreditsMicro
	}
	for _, granularity := range []UsageSeriesGranularity{SeriesGranularityDay, SeriesGranularityHour} {
		series, err := s.AggregateUsageSeries(ctx, UsageFilter{}, granularity)
		if err != nil {
			t.Fatalf("全局 %s 趋势失败: %v", granularity, err)
		}
		var requests, credits int64
		for _, row := range series {
			requests += row.Requests
			credits += row.CreditsMicro
		}
		if requests != globalRequests || credits != globalCredits {
			t.Fatalf("全局 %s 趋势累计不符: series=%d/%d agg=%d/%d",
				granularity, requests, credits, globalRequests, globalCredits)
		}
	}
}
