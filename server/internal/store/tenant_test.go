package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/credits"
)

func runTenantSuite(t *testing.T, ctx context.Context, s PlatformStore) {
	t.Run("membership-isolation", func(t *testing.T) { testTenantMembership(t, ctx, s) })
	t.Run("last-admin-concurrency", func(t *testing.T) { testTenantLastAdmin(t, ctx, s) })
	t.Run("global-disable-last-admin-concurrency", func(t *testing.T) { testTenantGlobalDisableAdmin(t, ctx, s) })
	t.Run("invitation-atomic-consumption", func(t *testing.T) { testTenantInvitations(t, ctx, s) })
	for _, kind := range []string{"member", "daily", "monthly"} {
		t.Run("budget-concurrency-"+kind, func(t *testing.T) { testTenantBudget(t, ctx, s, kind) })
	}
	t.Run("budget-shanghai-boundaries", func(t *testing.T) { testTenantBudgetBoundary(t, ctx, s) })
	t.Run("credit-debt-floor", func(t *testing.T) { testTenantCreditDebtFloor(t, ctx, s) })
	t.Run("audit-team-filter", func(t *testing.T) { testTenantAudit(t, ctx, s) })
}
func tenantTeam(t *testing.T, ctx context.Context, s PlatformStore, name string) *TeamRecord {
	t.Helper()
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "tenant-" + name, QuotaCredits: "100", Unlimited: true})
	if err != nil {
		t.Fatal(err)
	}
	return team
}
func testTenantMembership(t *testing.T, ctx context.Context, s PlatformStore) {
	user := mustUser(t, ctx, s, "tenant-multi@test.local")
	teams := []*TeamRecord{tenantTeam(t, ctx, s, "single-a"), tenantTeam(t, ctx, s, "single-b")}
	var added, conflicts atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, team := range teams {
		wg.Add(1)
		go func(teamID string) {
			defer wg.Done()
			err := s.AddMember(ctx, teamID, user.ID, MemberRoleMember)
			if err == nil {
				added.Add(1)
			} else if errors.Is(err, ErrUserAlreadyInTeam) {
				conflicts.Add(1)
			} else {
				errs <- err
			}
		}(team.ID)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if added.Load() != 1 || conflicts.Load() != 1 {
		t.Fatalf("跨团队并发加入必须恰有一次成功: added=%d conflicts=%d", added.Load(), conflicts.Load())
	}
	selected, err := ResolveUserTeam(ctx, s, user.ID)
	if err != nil || selected == nil {
		t.Fatalf("解析唯一团队: %+v %v", selected, err)
	}
	at := time.Now()
	budget := "0.8"
	member, err := s.UpdateTeamMember(ctx, selected.Team.ID, user.ID, TeamMemberPatch{HasDisabled: true, DisabledAt: &at, MonthlyBudgetCredits: &budget})
	if err != nil || member == nil || member.MonthlyBudgetCredits != "0.800000" {
		t.Fatalf("成员 patch: %+v %v", member, err)
	}
	if selected, err := ResolveUserTeam(ctx, s, user.ID); err != nil || selected != nil {
		t.Fatalf("停用成员不能解析到可用团队: %+v %v", selected, err)
	}
	// 内存读取必须是快照，调用方修改返回指针不能重新启用存储中的成员。
	member.MemberDisabledAt = nil
	member.MonthlyBudgetCredits = ""
	persisted, _ := s.GetTeamMember(ctx, selected.Team.ID, user.ID)
	if persisted == nil || persisted.MemberDisabledAt == nil || persisted.MonthlyBudgetCredits != "0.800000" {
		t.Fatalf("成员返回值发生别名写入: %+v", persisted)
	}
}
func testTenantLastAdmin(t *testing.T, ctx context.Context, s PlatformStore) {
	team := tenantTeam(t, ctx, s, "admins")
	users := []*UserRecord{mustUser(t, ctx, s, "tenant-admin-a@test.local"), mustUser(t, ctx, s, "tenant-admin-b@test.local")}
	for _, user := range users {
		if err := s.AddMember(ctx, team.ID, user.ID, MemberRoleAdmin); err != nil {
			t.Fatal(err)
		}
	}
	var removed, protected atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, user := range users {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			ok, err := s.RemoveMember(ctx, team.ID, id)
			if err == nil && ok {
				removed.Add(1)
			} else if errors.Is(err, ErrLastTeamAdmin) {
				protected.Add(1)
			} else {
				errs <- fmt.Errorf("remove ok=%v err=%v", ok, err)
			}
		}(user.ID)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if removed.Load() != 1 || protected.Load() != 1 {
		t.Fatalf("并发移除后必须保留一位管理员: removed=%d protected=%d", removed.Load(), protected.Load())
	}
	members, err := s.ListMembers(ctx, team.ID)
	if err != nil || len(members) != 1 {
		t.Fatalf("管理员数量错误: %+v %v", members, err)
	}
	id := members[0].User.ID
	role := MemberRoleMember
	at := time.Now()
	for _, patch := range []TeamMemberPatch{{Role: &role}, {HasDisabled: true, DisabledAt: &at}} {
		if _, err := s.UpdateTeamMember(ctx, team.ID, id, patch); !errors.Is(err, ErrLastTeamAdmin) {
			t.Fatalf("最后管理员 patch 应受保护: %v", err)
		}
	}
	if err := s.AddMember(ctx, team.ID, id, MemberRoleMember); !errors.Is(err, ErrLastTeamAdmin) {
		t.Fatalf("upsert 不得绕过最后管理员保护: %v", err)
	}
}

func testTenantInvitations(t *testing.T, ctx context.Context, s PlatformStore) {
	team := tenantTeam(t, ctx, s, "invites")
	other := tenantTeam(t, ctx, s, "invite-other")
	owner := mustUser(t, ctx, s, "tenant-invite-owner@test.local")
	user := mustUser(t, ctx, s, "tenant-invite-member@test.local")
	wrong := mustUser(t, ctx, s, "tenant-invite-wrong@test.local")
	if err := s.AddMember(ctx, team.ID, owner.ID, MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	create := func(hash, email string, expires time.Time) string {
		t.Helper()
		id := uuid.NewString()
		if err := s.CreateTeamInvitation(ctx, TeamInvitationRecord{ID: id, TeamID: team.ID, Email: email, TokenHash: hash, CreatedBy: owner.ID, CreatedAt: now, ExpiresAt: expires}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// 已是团队管理员的账号不能再消费邀请，不能藉由 member 默认角色降权。
	create("tenant-invite-owner-existing", owner.Email, now.Add(time.Hour))
	if _, err := s.AcceptTeamInvitation(ctx, "tenant-invite-owner-existing", owner.ID, now); !errors.Is(err, ErrInvitationInvalid) {
		t.Fatalf("既有成员不能消费邀请: %v", err)
	}
	existing, err := s.GetTeamMember(ctx, team.ID, owner.ID)
	if err != nil || existing.Role != MemberRoleOwner {
		t.Fatalf("接受邀请不能改变现有管理员角色: %+v %v", existing, err)
	}
	existingInvite, err := s.FindTeamInvitationByHash(ctx, "tenant-invite-owner-existing")
	if err != nil || existingInvite.AcceptedAt != nil {
		t.Fatalf("拒绝既有成员必须保留未消费邀请: %+v %v", existingInvite, err)
	}
	hash := "tenant-invite-once"
	create(hash, user.Email, now.Add(time.Hour))
	if _, err := s.AcceptTeamInvitation(ctx, hash, wrong.ID, now); !errors.Is(err, ErrInvitationEmailMismatch) {
		t.Fatalf("错误邮箱应拒绝: %v", err)
	}
	fetched, err := s.FindTeamInvitationByHash(ctx, hash)
	if err != nil || fetched == nil || fetched.AcceptedAt != nil {
		t.Fatalf("错误邮箱不能消费邀请: %+v %v", fetched, err)
	}
	if err := s.AddMember(ctx, other.ID, user.ID, MemberRoleMember); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptTeamInvitation(ctx, hash, user.ID, now); !errors.Is(err, ErrUserAlreadyInTeam) {
		t.Fatalf("已属其他团队的账号不得接受邀请: %v", err)
	}
	if ok, err := s.RemoveMember(ctx, other.ID, user.ID); err != nil || !ok {
		t.Fatalf("移除其他团队: %v", err)
	}
	var accepted, invalid atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.AcceptTeamInvitation(ctx, hash, user.ID, now)
			if err == nil {
				accepted.Add(1)
			} else if errors.Is(err, ErrInvitationInvalid) {
				invalid.Add(1)
			} else {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if accepted.Load() != 1 || invalid.Load() != 15 {
		t.Fatalf("邀请只能消费一次: accepted=%d invalid=%d", accepted.Load(), invalid.Load())
	}
	member, err := s.GetTeamMember(ctx, team.ID, user.ID)
	if err != nil || member == nil || member.Role != MemberRoleMember {
		t.Fatalf("邀请应原子创建成员: %+v %v", member, err)
	}
	revoked := create("tenant-invite-revoked", wrong.Email, now.Add(time.Hour))
	if ok, err := s.RevokeTeamInvitation(ctx, team.ID, revoked, now); err != nil || !ok {
		t.Fatal(err)
	}
	create("tenant-invite-expired", wrong.Email, now)
	for _, hash := range []string{"tenant-invite-revoked", "tenant-invite-expired", "missing"} {
		if _, err := s.AcceptTeamInvitation(ctx, hash, wrong.ID, now); !errors.Is(err, ErrInvitationInvalid) {
			t.Fatalf("失效邀请 %s 不得接受: %v", hash, err)
		}
	}
}

func testTenantBudget(t *testing.T, ctx context.Context, s PlatformStore, kind string) {
	team := tenantTeam(t, ctx, s, "budget-"+kind)
	user := mustUser(t, ctx, s, "tenant-budget-"+kind+"@test.local")
	if err := s.AddMember(ctx, team.ID, user.ID, MemberRoleMember); err != nil {
		t.Fatal(err)
	}
	policy := TeamPolicyRecord{TeamID: team.ID, AllowedModelIDs: []string{"model-a"}}
	switch kind {
	case "member":
		limit := "1"
		if _, err := s.UpdateTeamMember(ctx, team.ID, user.ID, TeamMemberPatch{MonthlyBudgetCredits: &limit}); err != nil {
			t.Fatal(err)
		}
	case "daily":
		policy.DailyBudgetCredits = "1"
	case "monthly":
		policy.MonthlyBudgetCredits = "1"
	}
	if _, err := s.SaveTeamPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	policy.AllowedModelIDs[0] = "changed"
	saved, err := s.GetTeamPolicy(ctx, team.ID)
	if err != nil || saved.AllowedModelIDs[0] != "model-a" || saved.BudgetAlertPercent != 80 {
		t.Fatalf("策略应保存快照及默认提醒: %+v %v", saved, err)
	}
	now := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	var allowed atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	ids := make(chan string, 40)
	for i := range 40 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := uuid.NewString()
			result, err := s.ReserveQuota(ctx, ReserveQuotaInput{ID: id, TeamID: team.ID, UserID: user.ID, RequestID: fmt.Sprintf("budget-%s-%d", kind, i), Micro: 100000, CreatedAt: now})
			if err != nil {
				errs <- err
				return
			}
			if result == ReserveReserved {
				allowed.Add(1)
				ids <- id
			} else if result != ReserveDeniedBudget {
				errs <- fmt.Errorf("unexpected reserve %s", result)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	close(ids)
	for err := range errs {
		t.Fatal(err)
	}
	if allowed.Load() != 10 {
		t.Fatalf("%s 预算并发只能预留 10 次: %d", kind, allowed.Load())
	}
	var acceptedIDs []string
	for id := range ids {
		acceptedIDs = append(acceptedIDs, id)
	}
	if err := s.ReleaseReservationByID(ctx, acceptedIDs[0], now); err != nil {
		t.Fatal(err)
	}
	result, err := s.ReserveQuota(ctx, ReserveQuotaInput{ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID, RequestID: "after-release-" + kind, Micro: 100000, CreatedAt: now})
	if err != nil || result != ReserveReserved {
		t.Fatalf("释放应归还周期预算: %s %v", result, err)
	}
	usage := UsageRecord{ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID, RequestID: "settled-" + kind, Model: "model-a", UpstreamModel: "model-a", CreditsMicro: 50000, Status: UsageStatusOk, CreatedAt: now}
	if err := s.SettleReservation(ctx, acceptedIDs[1], usage); err != nil {
		t.Fatal(err)
	}
	result, err = s.ReserveQuota(ctx, ReserveQuotaInput{ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID, RequestID: "after-settle-" + kind, Micro: 50000, CreatedAt: now})
	if err != nil || result != ReserveReserved {
		t.Fatalf("结算必须以实扣替代预留: %s %v", result, err)
	}
	result, err = s.ReserveQuota(ctx, ReserveQuotaInput{ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID, RequestID: "over-budget-" + kind, Micro: 1, CreatedAt: now})
	if err != nil || result != ReserveDeniedBudget {
		t.Fatalf("达到预算后拒绝新增消费: %s %v", result, err)
	}
	// 成员停用必须由准入事务重新验证，不能只信网关早先读取的快照。
	at := now
	if _, err := s.UpdateTeamMember(ctx, team.ID, user.ID, TeamMemberPatch{HasDisabled: true, DisabledAt: &at}); err != nil {
		t.Fatal(err)
	}
	result, err = s.ReserveQuota(ctx, ReserveQuotaInput{ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID, RequestID: "disabled-" + kind, Micro: 0, CreatedAt: now})
	if err != nil || result != ReserveDeniedInactive {
		t.Fatalf("停用成员不得预留: %s %v", result, err)
	}
}

func testTenantBudgetBoundary(t *testing.T, ctx context.Context, s PlatformStore) {
	team := tenantTeam(t, ctx, s, "boundary")
	user := mustUser(t, ctx, s, "tenant-boundary@test.local")
	if err := s.AddMember(ctx, team.ID, user.ID, MemberRoleMember); err != nil {
		t.Fatal(err)
	}
	before := time.Date(2026, 10, 31, 15, 59, 59, 0, time.UTC)
	after := before.Add(time.Second)
	if err := s.InsertUsage(ctx, UsageRecord{ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID, RequestID: "previous-month", Model: "m", UpstreamModel: "m", Status: UsageStatusOk, CreditsMicro: 1000000, CreatedAt: before}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveTeamPolicy(ctx, TeamPolicyRecord{TeamID: team.ID, DailyBudgetCredits: "1", MonthlyBudgetCredits: "1"}); err != nil {
		t.Fatal(err)
	}
	reserve := func(request string, at time.Time, micro int64) ReserveResult {
		t.Helper()
		result, err := s.ReserveQuota(ctx, ReserveQuotaInput{ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID, RequestID: request, Micro: micro, CreatedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if got := reserve("before-boundary", before, 1); got != ReserveDeniedBudget {
		t.Fatalf("日/月边界前已用满应拒绝: %s", got)
	}
	if got := reserve("after-boundary", after, 1000000); got != ReserveReserved {
		t.Fatalf("上海 00:00 新月/新日应重置预算: %s", got)
	}
	if _, err := s.SaveTeamPolicy(ctx, TeamPolicyRecord{TeamID: team.ID, DailyBudgetCredits: "0"}); err != nil {
		t.Fatal(err)
	}
	if got := reserve("zero-hard-limit", after, 0); got != ReserveDeniedBudget {
		t.Fatalf("零预算应硬禁用: %s", got)
	}
}

func testTenantCreditDebtFloor(t *testing.T, ctx context.Context, s PlatformStore) {
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "tenant-credit-floor", QuotaCredits: "1", Unlimited: false})
	if err != nil {
		t.Fatal(err)
	}
	user := mustUser(t, ctx, s, "tenant-credit-floor@test.local")
	if err := s.AddMember(ctx, team.ID, user.ID, MemberRoleMember); err != nil {
		t.Fatal(err)
	}

	const reservationMicro = int64(10_000)
	const attempts = 120
	var allowed atomic.Int64
	var denied atomic.Int64
	var wg sync.WaitGroup
	ids := make(chan string, attempts)
	errs := make(chan error, attempts)
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := uuid.NewString()
			result, err := s.ReserveQuota(ctx, ReserveQuotaInput{
				ID: id, TeamID: team.ID, UserID: user.ID,
				RequestID: fmt.Sprintf("credit-floor-%d", i), Micro: reservationMicro,
			})
			if err != nil {
				errs <- err
				return
			}
			switch result {
			case ReserveReserved:
				allowed.Add(1)
				ids <- id
			case ReserveDenied:
				denied.Add(1)
			default:
				errs <- fmt.Errorf("unexpected credit reserve result: %s", result)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	close(ids)
	for err := range errs {
		t.Fatal(err)
	}
	if allowed.Load() != 100 || denied.Load() != attempts-100 {
		t.Fatalf("并发预留不得透支: allowed=%d denied=%d", allowed.Load(), denied.Load())
	}

	for _, micro := range []int64{0, -1, 1} {
		if result, err := s.ReserveQuota(ctx, ReserveQuotaInput{TeamID: team.ID, UserID: user.ID, RequestID: uuid.NewString(), Micro: micro}); err != nil || result != ReserveDenied {
			t.Fatalf("余额为0时拒绝所有新预留: micro=%d result=%s err=%v", micro, result, err)
		}
	}

	var firstReservation string
	var remainingReservations []string
	for id := range ids {
		if firstReservation == "" {
			firstReservation = id
		} else {
			remainingReservations = append(remainingReservations, id)
		}
	}
	usage := UsageRecord{
		ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID,
		RequestID: "credit-floor-settlement", Model: "model-a", UpstreamModel: "model-a",
		CreditsMicro: 1_500_000, Status: UsageStatusOk, CreatedAt: time.Now(),
	}
	invalidOwner := usage
	invalidOwner.TeamID = uuid.NewString()
	if err := s.SettleReservation(ctx, firstReservation, invalidOwner); !errors.Is(err, ErrReservationMismatch) {
		t.Fatalf("预留不能跨团队结算: %v", err)
	}

	if err := s.SettleReservation(ctx, firstReservation, usage); !errors.Is(err, ErrCreditChargeCapped) {
		t.Fatalf("超出剩余敞口的实扣应在事务中封顶并返回可识别告警: %v", err)
	}
	if err := s.SettleReservation(ctx, firstReservation, usage); err != nil {
		t.Fatalf("封顶结算重放必须幂等: %v", err)
	}

	teamAfter, err := s.GetTeam(ctx, team.ID)
	if err != nil {
		t.Fatal(err)
	}
	used, err := credits.ParseCreditsToMicro(teamAfter.UsedCredits)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := credits.ParseCreditsToMicro(teamAfter.ReservedCredits)
	if err != nil {
		t.Fatal(err)
	}
	quota, err := credits.ParseCreditsToMicro(teamAfter.QuotaCredits)
	if err != nil {
		t.Fatal(err)
	}
	if used+reserved > quota+TeamCreditDebtLimitMicro {
		t.Fatalf("实扣后超过 -0.01 硬下限: used=%d reserved=%d quota=%d", used, reserved, quota)
	}
	stored, err := s.ListUsage(ctx, UsageFilter{TeamID: team.ID, RequestID: usage.RequestID})
	if err != nil || len(stored) != 1 || stored[0].CreditsMicro != reservationMicro+TeamCreditDebtLimitMicro || stored[0].ErrorType != "credit_charge_capped" {
		t.Fatalf("超额实扣应按本请求剩余敞口封顶: records=%+v err=%v", stored, err)
	}
	result, err := s.ReserveQuota(ctx, ReserveQuotaInput{
		ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID,
		RequestID: "credit-floor-after-settlement", Micro: 1,
	})
	if err != nil || result != ReserveDenied {
		t.Fatalf("达到 -0.01 后必须拒绝新预留: result=%s err=%v", result, err)
	}
	// 多个结算同时报告超额用量，也只能分配各自预留及团队唯一的尾差空间。
	settlementErrors := make(chan error, len(remainingReservations))
	for _, id := range remainingReservations {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			record := usage
			record.ID, record.RequestID = uuid.NewString(), uuid.NewString()
			if err := s.SettleReservation(ctx, id, record); !errors.Is(err, ErrCreditChargeCapped) {
				settlementErrors <- fmt.Errorf("concurrent cap: %v", err)
			}
		}(id)
	}
	wg.Wait()
	close(settlementErrors)
	for err := range settlementErrors {
		t.Fatal(err)
	}
	final, err := s.GetTeam(ctx, team.ID)
	if err != nil || final.UsedCredits != "1.010000" || final.ReservedCredits != "0.000000" {
		t.Fatalf("并发结算不能越过 -0.01: %+v %v", final, err)
	}

}

func testTenantAudit(t *testing.T, ctx context.Context, s PlatformStore) {
	team := tenantTeam(t, ctx, s, "audit-a")
	other := tenantTeam(t, ctx, s, "audit-b")
	for _, id := range []string{team.ID, other.ID, ""} {
		if err := s.InsertAuditLog(ctx, InsertAuditLogInput{TeamID: id, Action: "tenant.test", Detail: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	logs, err := s.ListAuditLogs(ctx, AuditLogFilter{TeamID: team.ID})
	if err != nil || len(logs) != 1 || logs[0].TeamID != team.ID {
		t.Fatalf("团队审计不得泄漏其他租户: %+v %v", logs, err)
	}
}

func TestResolveUserTeamRejectsLegacyAmbiguity(t *testing.T) {
	ctx := context.Background()
	s, closeStore := newMemoryForTest(t)
	defer closeStore()
	user := mustUser(t, ctx, s, "legacy-multi@test.local")
	a := tenantTeam(t, ctx, s, "legacy-a")
	b := tenantTeam(t, ctx, s, "legacy-b")
	// 直接模拟迁移前遗留的多团队数据，正常写端口已禁止创建这一状态。
	s.mu.Lock()
	s.members[a.ID][user.ID] = struct{}{}
	s.members[b.ID][user.ID] = struct{}{}
	s.mu.Unlock()
	if got, err := ResolveUserTeam(ctx, s, user.ID); !errors.Is(err, ErrAmbiguousTeam) || got != nil {
		t.Fatalf("不能静默选择第一个团队: %+v %v", got, err)
	}
}

func testTenantGlobalDisableAdmin(t *testing.T, ctx context.Context, s PlatformStore) {
	team := tenantTeam(t, ctx, s, "global-disable-admins")
	users := []*UserRecord{mustUser(t, ctx, s, "tenant-global-admin-a@test.local"), mustUser(t, ctx, s, "tenant-global-admin-b@test.local")}
	for _, user := range users {
		if err := s.AddMember(ctx, team.ID, user.ID, MemberRoleAdmin); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now().UTC().Truncate(time.Millisecond)
	var disabled, protected atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, user := range users {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := s.UpdateUser(ctx, id, UpdateUserPatch{HasDisabled: true, DisabledAt: &at})
			if err == nil {
				disabled.Add(1)
			} else if errors.Is(err, ErrLastTeamAdmin) {
				protected.Add(1)
			} else {
				errs <- err
			}
		}(user.ID)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if disabled.Load() != 1 || protected.Load() != 1 {
		t.Fatalf("并发全局停用只能成功一次: disabled=%d protected=%d", disabled.Load(), protected.Load())
	}
	var activeID string
	for _, user := range users {
		current, err := s.FindUserByID(ctx, user.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.DisabledAt == nil {
			activeID = user.ID
		}
	}
	if activeID == "" {
		t.Fatal("团队必须保留可登录的管理员")
	}
	if _, err := s.RemoveMember(ctx, team.ID, activeID); !errors.Is(err, ErrLastTeamAdmin) {
		t.Fatalf("全局已停用的管理员不能充当保留管理员: %v", err)
	}
	role := MemberRoleMember
	for _, patch := range []TeamMemberPatch{{Role: &role}, {HasDisabled: true, DisabledAt: &at}} {
		if _, err := s.UpdateTeamMember(ctx, team.ID, activeID, patch); !errors.Is(err, ErrLastTeamAdmin) {
			t.Fatalf("全局已停用的另一管理员不能绕过成员保护: %v", err)
		}
	}
}
