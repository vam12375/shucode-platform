package apiapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

type tenantFixture struct {
	*apiFixture
	root, admin, member, other, unassigned                               *store.UserRecord
	team, otherTeam                                                      *store.TeamRecord
	rootCookie, adminCookie, memberCookie, otherCookie, unassignedCookie string
}

func newTenantFixture(t *testing.T) *tenantFixture {
	t.Helper()
	f := &tenantFixture{apiFixture: newAPIEmptyFixture(t)}
	ctx := context.Background()
	hash, err := crypto.HashPassword("tenant-password-123")
	if err != nil {
		t.Fatal(err)
	}
	create := func(email string, role store.UserRole) *store.UserRecord {
		u, err := f.store.CreateUser(ctx, store.UserInput{Email: email, DisplayName: email, PasswordHash: hash, Role: role})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	f.root = create("root@tenant.test", store.RoleRoot)
	// 管理权来自团队关系，即使全局 role=user 也可担任团队管理员。
	f.admin = create("admin@tenant.test", store.RoleUser)
	f.member = create("member@tenant.test", store.RoleUser)
	f.other = create("other@tenant.test", store.RoleAdmin)
	f.unassigned = create("unassigned@tenant.test", store.RoleAdmin)
	f.team, err = f.store.CreateTeam(ctx, store.CreateTeamInput{Name: "团队A", QuotaCredits: "100"})
	if err != nil {
		t.Fatal(err)
	}
	f.otherTeam, err = f.store.CreateTeam(ctx, store.CreateTeamInput{Name: "团队B", QuotaCredits: "200"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		team, user string
		role       store.TeamMemberRole
	}{{f.team.ID, f.admin.ID, store.MemberRoleAdmin}, {f.team.ID, f.member.ID, store.MemberRoleMember}, {f.otherTeam.ID, f.other.ID, store.MemberRoleOwner}} {
		if err := f.store.AddMember(ctx, item.team, item.user, item.role); err != nil {
			t.Fatal(err)
		}
	}
	login := func(user *store.UserRecord) string {
		rec := f.do("POST", "/console/api/auth/login", "", fmt.Sprintf(`{"email":%q,"password":"tenant-password-123"}`, user.Email))
		if rec.Code != 200 {
			t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
		}
		return sessionCookie(rec)
	}
	f.rootCookie = login(f.root)
	f.adminCookie = login(f.admin)
	f.memberCookie = login(f.member)
	f.otherCookie = login(f.other)
	f.unassignedCookie = login(f.unassigned)
	return f
}

func TestTenantPlatformManagementIsRootOnly(t *testing.T) {
	f := newTenantFixture(t)
	for _, endpoint := range []struct{ method, path, body string }{
		{"GET", "/model-pricing", ""}, {"PUT", "/model-pricing", `{}`}, {"GET", "/providers", ""}, {"POST", "/providers", `{}`},
		{"GET", "/config", ""}, {"PUT", "/config", `{}`}, {"GET", "/releases", ""}, {"POST", "/releases", `{}`},
		{"GET", "/ops/overview", ""}, {"GET", "/ops/alert-rules", ""}, {"GET", "/models/x/routes", ""},
		{"PUT", "/models/x", `{}`}, {"POST", "/models/import", `{}`}, {"DELETE", "/models/x", ""},
		{"POST", "/teams", `{"name":"unauthorized"}`}, {"POST", "/users", `{}`}, {"PATCH", "/users/" + f.member.ID, `{"disabled":true}`},
	} {
		for _, cookie := range []string{f.adminCookie, f.memberCookie, f.unassignedCookie} {
			rec := f.do(endpoint.method, "/console/api"+endpoint.path, cookie, endpoint.body)
			if rec.Code != 403 {
				t.Errorf("%s %s must deny: %d %s", endpoint.method, endpoint.path, rec.Code, rec.Body.String())
			}
		}
	}
	for _, body := range []string{`{"quotaCredits":"999999"}`, `{"unlimited":true}`, `{"rpmLimit":0}`, `{"disabled":true}`} {
		if rec := f.do("PATCH", "/console/api/teams/"+f.team.ID, f.adminCookie, body); rec.Code != 403 {
			t.Errorf("admin cannot grant credit/ceilings: %d %s", rec.Code, rec.Body.String())
		}
	}
	for _, cookie := range []string{f.adminCookie, f.memberCookie, f.unassignedCookie} {
		if rec := f.do("POST", "/console/api/teams/"+f.team.ID+"/credits", cookie, `{"amountCredits":"10","requestId":"00000000-0000-4000-8000-000000000001"}`); rec.Code != 403 {
			t.Errorf("非超级管理员不能添加余额: %d %s", rec.Code, rec.Body.String())
		}
	}
	if rec := f.do("PATCH", "/console/api/teams/"+f.team.ID, f.adminCookie, `{"name":"团队A改名"}`); rec.Code != 200 {
		t.Fatalf("team manager should rename own team: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("GET", "/console/api/teams/"+f.otherTeam.ID, f.adminCookie, ""); rec.Code != 403 {
		t.Fatalf("cross-team detail: %d", rec.Code)
	}
	if rec := f.do("GET", "/console/api/usage?teamId="+f.otherTeam.ID, f.adminCookie, ""); rec.Code != 403 {
		t.Fatalf("forged team query: %d", rec.Code)
	}
	if rec := f.do("GET", "/console/api/usage?teamId=invalid", f.adminCookie, ""); rec.Code != 400 {
		t.Fatalf("invalid team query: %d", rec.Code)
	}
	if rec := f.do("GET", "/console/api/usage", f.unassignedCookie, ""); rec.Code != 403 {
		t.Fatalf("global admin marker has no team: %d", rec.Code)
	}
	rows := dataOf(t, f.do("GET", "/console/api/teams", f.adminCookie, ""))["teams"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["id"] != f.team.ID {
		t.Fatalf("team directory leaked: %v", rows)
	}
}

func TestOnlyRootModelDirectoryResponseContainsPricing(t *testing.T) {
	f := newTenantFixture(t)
	if _, err := f.store.UpsertModel(context.Background(), store.UpsertModelInput{
		ModelID: "team-model", DisplayName: "Team model", InputPricePerMtok: "12.345678", OutputPricePerMtok: "67.890123",
	}); err != nil {
		t.Fatal(err)
	}
	teamResponse := f.do("GET", "/console/api/models", f.adminCookie, "")
	if teamResponse.Code != 200 {
		t.Fatalf("团队管理员模型目录: %d %s", teamResponse.Code, teamResponse.Body.String())
	}
	for _, secretField := range []string{"inputPricePerMtok", "outputPricePerMtok", "cacheReadPricePerMtok", "cacheWritePricePerMtok", "offPeakMultiplier"} {
		if strings.Contains(teamResponse.Body.String(), secretField) {
			t.Fatalf("非 root 模型目录不得包含价格字段 %q: %s", secretField, teamResponse.Body.String())
		}
	}
	rootResponse := f.do("GET", "/console/api/models?includeDisabled=1", f.rootCookie, "")
	if rootResponse.Code != 200 || !strings.Contains(rootResponse.Body.String(), "inputPricePerMtok") {
		t.Fatalf("root 模型目录需要保留受保护的价格字段: %d %s", rootResponse.Code, rootResponse.Body.String())
	}
}

func TestTenantUsageAndExportsAreScopedAndRedacted(t *testing.T) {
	f := newTenantFixture(t)
	ctx := context.Background()
	for i, item := range []struct {
		team, user string
		cost       int64
	}{{f.team.ID, f.admin.ID, 100}, {f.team.ID, f.member.ID, 200}, {f.otherTeam.ID, f.other.ID, 900}} {
		err := f.store.InsertUsage(ctx, store.UsageRecord{ID: uuid.NewString(), RequestID: fmt.Sprintf("request-%d", i), TeamID: item.team, UserID: item.user, Model: "visible-model", UpstreamModel: "private-upstream-model", ProviderID: uuid.NewString(), ClientIP: "192.0.2.123", CreditsMicro: item.cost, InputTokens: 1, Status: store.UsageStatusOk, CreatedAt: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		cookie string
		count  int
		user   string
	}{{f.adminCookie, 2, ""}, {f.memberCookie, 1, f.member.ID}} {
		rec := f.do("GET", "/console/api/usage?userId="+f.other.ID, item.cookie, "")
		if item.user == "" {
			rec = f.do("GET", "/console/api/usage", item.cookie, "")
		}
		data := dataOf(t, rec)
		rows := data["records"].([]any)
		if len(rows) != item.count {
			t.Fatalf("scoped usage count: %s", rec.Body.String())
		}
		for _, raw := range rows {
			row := raw.(map[string]any)
			if row["teamId"] != f.team.ID {
				t.Fatal("other tenant usage leaked")
			}
			if item.user != "" && row["userId"] != item.user {
				t.Fatal("another member usage leaked")
			}
			for _, key := range []string{"upstreamModel", "providerId", "clientIp", "priceSnapshot"} {
				if _, exists := row[key]; exists {
					t.Fatalf("private field %s leaked", key)
				}
			}
		}
		csv := f.do("GET", "/console/api/usage/export", item.cookie, "")
		if csv.Code != 200 || strings.Contains(csv.Body.String(), "private-upstream") || strings.Contains(csv.Body.String(), "192.0.2.123") || strings.Contains(csv.Body.String(), f.otherTeam.ID) {
			t.Fatalf("CSV scope/redaction: %d %s", csv.Code, csv.Body.String())
		}
	}
	series := dataOf(t, f.do("GET", "/console/api/usage/series?granularity=day", f.adminCookie, ""))["series"].([]any)
	var total float64
	for _, raw := range series {
		total += raw.(map[string]any)["creditsMicro"].(float64)
	}
	if total != 300 {
		t.Fatalf("series leaked global usage: %v", total)
	}
	detail := dataOf(t, f.do("GET", "/console/api/teams/"+f.team.ID, f.memberCookie, ""))
	if len(detail["members"].([]any)) != 1 || len(detail["recentUsage"].([]any)) != 1 {
		t.Fatalf("member detail must only expose personal data: %v", detail)
	}
	for _, item := range []struct{ team, action string }{{f.team.ID, "member.update"}, {f.otherTeam.ID, "other.private"}, {"", "pricing.update"}} {
		if err := f.store.InsertAuditLog(ctx, store.InsertAuditLogInput{TeamID: item.team, ActorUserID: f.root.ID, Action: item.action, Detail: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	audit := dataOf(t, f.do("GET", "/console/api/audit", f.adminCookie, ""))["logs"].([]any)
	if len(audit) != 1 || audit[0].(map[string]any)["action"] != "member.update" {
		t.Fatalf("audit leaked: %v", audit)
	}
	if rec := f.do("GET", "/console/api/audit", f.memberCookie, ""); rec.Code != 403 {
		t.Fatalf("member audit access: %d", rec.Code)
	}
}

func TestTenantDevicesAndMemberLifecycle(t *testing.T) {
	f := newTenantFixture(t)
	ctx := context.Background()
	ids := map[string]string{}
	for _, user := range []*store.UserRecord{f.admin, f.member, f.other} {
		id := uuid.NewString()
		ids[user.ID] = id
		if err := f.store.CreateDeviceToken(ctx, store.DeviceTokenRecord{ID: id, UserID: user.ID, Name: user.Email, TokenHash: crypto.HashToken(id), CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	rows := dataOf(t, f.do("GET", "/console/api/devices", f.adminCookie, ""))["devices"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["id"] != ids[f.admin.ID] {
		t.Fatalf("device directory leaked: %v", rows)
	}
	rootRows := dataOf(t, f.do("GET", "/console/api/devices?teamId="+f.team.ID, f.rootCookie, ""))["devices"].([]any)
	if len(rootRows) != 2 {
		t.Fatalf("root team device scope: %v", rootRows)
	}
	for _, raw := range rootRows {
		if raw.(map[string]any)["id"] == ids[f.other.ID] {
			t.Fatal("selected team includes another team's device")
		}
	}
	if rec := f.do("POST", "/console/api/devices/"+ids[f.member.ID]+"/revoke", f.adminCookie, ""); rec.Code != 403 {
		t.Fatalf("admin must not revoke another global credential: %d", rec.Code)
	}
	if rec := f.do("POST", "/console/api/devices/"+ids[f.admin.ID]+"/revoke", f.adminCookie, ""); rec.Code != 200 {
		t.Fatalf("self revoke: %d", rec.Code)
	}
	path := "/console/api/teams/" + f.team.ID + "/members/" + f.member.ID
	if rec := f.do("PATCH", path, f.adminCookie, `{"monthlyBudgetCredits":"10","disabled":true}`); rec.Code != 200 {
		t.Fatalf("scoped member suspend: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("GET", "/console/api/usage", f.memberCookie, ""); rec.Code != 403 {
		t.Fatalf("suspended team member: %d", rec.Code)
	}
	for _, endpoint := range []string{"/auth/me", "/profile"} {
		data := dataOf(t, f.do("GET", "/console/api"+endpoint, f.memberCookie, ""))
		if len(data["memberships"].([]any)) != 0 {
			t.Fatalf("suspended membership leaks team information via %s: %v", endpoint, data)
		}
	}
	device, _ := f.store.FindDeviceTokenByID(ctx, ids[f.member.ID])
	user, _ := f.store.FindUserByID(ctx, f.member.ID)
	if device.RevokedAt != nil || user.DisabledAt != nil {
		t.Fatal("team suspension must preserve global account and credentials")
	}
	if rec := f.do("PATCH", path, f.adminCookie, `{"disabled":false}`); rec.Code != 200 {
		t.Fatalf("member resume: %d", rec.Code)
	}
	if rec := f.do("PATCH", path, f.adminCookie, `{"role":"admin"}`); rec.Code != 403 {
		t.Fatalf("admin may not appoint managers: %d", rec.Code)
	}
	if rec := f.do("DELETE", "/console/api/teams/"+f.team.ID+"/members/"+f.admin.ID, f.rootCookie, ""); rec.Code != 409 {
		t.Fatalf("protect last manager: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTenantInvitationsAndProfile(t *testing.T) {
	f := newTenantFixture(t)
	ctx := context.Background()
	invitePath := "/console/api/teams/" + f.team.ID + "/invitations"
	if rec := f.do("POST", invitePath, f.memberCookie, `{"email":"new@tenant.test"}`); rec.Code != 403 {
		t.Fatalf("member invite: %d", rec.Code)
	}
	rec := f.do("POST", invitePath, f.adminCookie, `{"email":"new@tenant.test"}`)
	data := dataOf(t, rec)
	url := data["inviteUrl"].(string)
	token := strings.Split(url, "token=")[1]
	if strings.Contains(rec.Body.String(), "tokenHash") {
		t.Fatal("invitation hash must never be returned")
	}
	if rec := f.do("POST", "/console/api/invitations/accept", f.otherCookie, fmt.Sprintf(`{"token":%q}`, token)); rec.Code != 403 {
		t.Fatalf("wrong recipient: %d", rec.Code)
	}
	accept := fmt.Sprintf(`{"token":%q,"email":"new@tenant.test","displayName":"新成员","password":"new-password-123","role":"root"}`, token)
	rec = f.do("POST", "/console/api/invitations/accept", "", accept)
	created := dataOf(t, rec)["user"].(map[string]any)
	if created["role"] != "user" {
		t.Fatal("invitation cannot grant root")
	}
	newCookie := sessionCookie(rec)
	if rec := f.do("POST", "/console/api/invitations/accept", newCookie, accept); rec.Code != 410 {
		t.Fatalf("invitation replay: %d", rec.Code)
	}
	if rec := f.do("PATCH", "/console/api/profile", newCookie, `{"currentPassword":"bad","password":"new-password-456"}`); rec.Code != 403 {
		t.Fatalf("old password check: %d", rec.Code)
	}
	rec = f.do("PATCH", "/console/api/profile", newCookie, `{"displayName":"已更新","currentPassword":"new-password-123","password":"new-password-456"}`)
	if rec.Code != 200 || sessionCookie(rec) == "" {
		t.Fatalf("change password should issue new session: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("GET", "/console/api/auth/me", newCookie, ""); rec.Code != 401 {
		t.Fatalf("old session survived password change: %d", rec.Code)
	}
	bad := fmt.Sprintf(`{"token":%q,"email":"invalid@tenant.test","displayName":"无效","password":"new-password-123"}`, strings.Repeat("x", 40))
	if rec := f.do("POST", "/console/api/invitations/accept", "", bad); rec.Code != 410 {
		t.Fatalf("invalid invite: %d", rec.Code)
	}
	if user, _ := f.store.FindUserByEmail(ctx, "invalid@tenant.test"); user != nil {
		t.Fatal("invalid invite created account")
	}
	policy := fmt.Sprintf(`{"dailyBudgetCredits":"5","monthlyBudgetCredits":"30","allowedModelIds":[],"budgetAlertPercent":50}`)
	if rec := f.do("PUT", "/console/api/teams/"+f.team.ID+"/policy", f.adminCookie, policy); rec.Code != 200 {
		t.Fatalf("policy write: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("PUT", "/console/api/teams/"+f.otherTeam.ID+"/policy", f.adminCookie, policy); rec.Code != 403 {
		t.Fatalf("foreign policy: %d", rec.Code)
	}
	user, _ := f.store.FindUserByID(ctx, created["id"].(string))
	var profile map[string]any
	if err := json.Unmarshal(f.do("GET", "/console/api/profile", sessionCookie(rec), "").Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	if user.DisplayName != "已更新" || !crypto.VerifyPassword("new-password-456", user.PasswordHash) {
		t.Fatal("profile update not persisted")
	}
}
