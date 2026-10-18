package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	t.Run("quota-critical-section", func(t *testing.T) { testQuota(t, ctx, s) })
	t.Run("device-tokens", func(t *testing.T) { testDeviceTokens(t, ctx, s) })
	t.Run("login-flows", func(t *testing.T) { testLoginFlows(t, ctx, s) })
	t.Run("releases", func(t *testing.T) { testReleases(t, ctx, s) })
	t.Run("models", func(t *testing.T) { testModels(t, ctx, s) })
	t.Run("providers", func(t *testing.T) { testProviders(t, ctx, s) })
	t.Run("pricing", func(t *testing.T) { testPricing(t, ctx, s) })
	t.Run("audit", func(t *testing.T) { testAudit(t, ctx, s) })
	t.Run("config-kv", func(t *testing.T) { testConfigKV(t, ctx, s) })
	t.Run("usage", func(t *testing.T) { testUsage(t, ctx, s) })
}

func mustUser(t *testing.T, ctx context.Context, s PlatformStore, email string) *UserRecord {
	t.Helper()
	u, err := s.CreateUser(ctx, UserInput{Email: email, PasswordHash: "scrypt$x", DisplayName: "测试", Role: RoleMember})
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
	if _, err := s.CreateUser(ctx, UserInput{Email: "alice@test.local", PasswordHash: "x", DisplayName: "x", Role: RoleMember}); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("重复邮箱应返回 ErrEmailTaken: %v", err)
	}
	if _, err := s.CreateUser(ctx, UserInput{Email: "ALICE@TEST.LOCAL", PasswordHash: "x", DisplayName: "x", Role: RoleMember}); !errors.Is(err, ErrEmailTaken) {
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
	if err != nil || updated.DisplayName != "改名" || updated.Role != RoleMember {
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
	if u2, _ := s.UpdateUser(ctx, "00000000-0000-4000-8000-000000000000", UpdateUserPatch{}); u2 != nil {
		t.Fatal("不存在的用户应返回 nil")
	}
}

func testSessions(t *testing.T, ctx context.Context, s PlatformStore) {
	u := mustUser(t, ctx, s, "sess@test.local")
	now := time.Now().UTC().Truncate(time.Millisecond)
	rec := SessionRecord{TokenHash: "hash-1", UserID: u.ID, CreatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour), LastSeenAt: now}
	if err := s.CreateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.FindSession(ctx, "hash-1")
	if err != nil || got == nil || got.UserID != u.ID || !got.ExpiresAt.Equal(rec.ExpiresAt) {
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
	quota := "2000.000000"
	unlimited := true
	updated, err := s.UpdateTeam(ctx, team.ID, UpdateTeamPatch{QuotaCredits: &quota, Unlimited: &unlimited})
	if err != nil || updated.QuotaCredits != "2000.000000" || !updated.Unlimited || updated.Name != "研发部" {
		t.Fatalf("更新团队: %v %+v", err, updated)
	}
	if removed, _ := s.RemoveMember(ctx, team.ID, member.ID); !removed {
		t.Fatal("移除成员失败")
	}
	if removed, _ := s.RemoveMember(ctx, team.ID, member.ID); removed {
		t.Fatal("二次移除应为 false")
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
		t.Fatal("超配额应拒绝(1 micro 也不行)")
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
	userID := u.ID
	authorizedAt := now.Add(time.Second)
	updated, err := s.UpdateLoginFlow(ctx, flow.ID, UpdateLoginFlowPatch{Status: ptr(FlowStatusAuthorized), UserID: &userID, AuthorizedAt: &authorizedAt})
	if err != nil || updated.Status != FlowStatusAuthorized || updated.UserID != u.ID || updated.AuthorizedAt == nil {
		t.Fatalf("授权登录流: %v %+v", err, updated)
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

func testModels(t *testing.T, ctx context.Context, s PlatformStore) {
	model, err := s.UpsertModel(ctx, UpsertModelInput{
		ModelID: "m-test", DisplayName: "M", InputPricePerMtok: "3", OutputPricePerMtok: "15",
	})
	if err != nil || model.CacheReadPricePerMtok != "0.000000" || !model.Enabled || model.SortOrder != 0 {
		t.Fatalf("新模型默认值不符: %v %+v", err, model)
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
	if err := s.InsertAuditLog(ctx, InsertAuditLogInput{Action: "provider.delete", Detail: []byte(`null`)}); err != nil {
		t.Fatal(err)
	}
	logs, _ := s.ListAuditLogs(ctx, AuditLogFilter{ActorUserID: actor.ID})
	if len(logs) != 1 || logs[0].ActorEmail != "actor@test.local" || logs[0].TargetID != "u-1" {
		t.Fatalf("审计过滤/邮箱 join: %+v", logs)
	}
	all, _ := s.ListAuditLogs(ctx, AuditLogFilter{})
	if len(all) != 2 || all[0].Action != "provider.delete" {
		t.Fatalf("审计列表应按时间倒序: %+v", all)
	}
	cursor := all[0]
	page2, _ := s.ListAuditLogs(ctx, AuditLogFilter{CursorCreatedAt: &cursor.CreatedAt, CursorID: cursor.ID})
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
		if err := s.InsertUsage(ctx, UsageRecord{
			ID:        fmt.Sprintf("00000000-0000-4000-8000-%012d", 400+i),
			RequestID: fmt.Sprintf("req-%d", i),
			UserID:    u.ID, TeamID: team.ID, Model: "m-a", UpstreamModel: "m-a-up",
			InputTokens: 10, OutputTokens: 20, CreditsMicro: int64(100 * (i + 1)),
			Status: UsageStatusOk, ClientIP: "198.51.100.9", LatencyMs: &latency,
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InsertUsage(ctx, UsageRecord{
		ID: "00000000-0000-4000-8000-000000000500", RequestID: "req-err",
		UserID: u.ID, TeamID: team.ID, Model: "m-b", UpstreamModel: "m-b-up",
		CreditsMicro: 50, Status: UsageStatusError, ErrorType: "upstream_500",
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
	day, _ := s.AggregateUsageSeries(ctx, UsageFilter{TeamID: team.ID}, SeriesGranularityDay)
	// base=01:00Z(+8 → 09:00) 当天,10:00Z→18:00 当天:全部同一天。
	if len(day) != 1 || day[0].BucketStart != "2026-10-18T00:00:00" || day[0].Requests != 6 {
		t.Fatalf("日桶不符: %+v", day)
	}
	hour, _ := s.AggregateUsageSeries(ctx, UsageFilter{TeamID: team.ID}, SeriesGranularityHour)
	if len(hour) != 6 || hour[0].BucketStart != "2026-10-18T09:00:00" {
		t.Fatalf("小时桶不符: %+v", hour)
	}
}
