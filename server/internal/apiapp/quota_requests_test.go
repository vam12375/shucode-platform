package apiapp

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

type quotaRequestAPIFixture struct {
	f                                                  *apiFixture
	rootCookie, adminCookie, otherCookie, memberCookie string
	teamID, otherTeamID                                string
}

func newQuotaRequestAPIFixture(t *testing.T) quotaRequestAPIFixture {
	t.Helper()
	f := newAPIEmptyFixture(t)
	ctx := context.Background()
	createUser := func(email string, role store.UserRole) (*store.UserRecord, string) {
		t.Helper()
		user, err := f.store.CreateUser(ctx, store.UserInput{Email: email, PasswordHash: "x", DisplayName: email, Role: role})
		if err != nil {
			t.Fatal(err)
		}
		token := uuid.NewString()
		now := time.Now()
		if err := f.store.CreateSession(ctx, store.SessionRecord{TokenHash: crypto.HashToken(token), UserID: user.ID, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		return user, sessionCookieName + "=" + token
	}
	_, rootCookie := createUser("request-root@test.local", store.RoleRoot)
	admin, adminCookie := createUser("request-admin@test.local", store.RoleUser)
	other, otherCookie := createUser("request-other@test.local", store.RoleAdmin)
	member, memberCookie := createUser("request-member@test.local", store.RoleUser)
	team, err := f.store.CreateTeam(ctx, store.CreateTeamInput{Name: "申请团队A", QuotaCredits: "5"})
	if err != nil {
		t.Fatal(err)
	}
	otherTeam, err := f.store.CreateTeam(ctx, store.CreateTeamInput{Name: "申请团队B", QuotaCredits: "7"})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		team, user string
		role       store.TeamMemberRole
	}{{team.ID, admin.ID, store.MemberRoleAdmin}, {team.ID, member.ID, store.MemberRoleMember}, {otherTeam.ID, other.ID, store.MemberRoleOwner}} {
		if err := f.store.AddMember(ctx, entry.team, entry.user, entry.role); err != nil {
			t.Fatal(err)
		}
	}
	return quotaRequestAPIFixture{f: f, rootCookie: rootCookie, adminCookie: adminCookie, otherCookie: otherCookie, memberCookie: memberCookie, teamID: team.ID, otherTeamID: otherTeam.ID}
}
func TestQuotaRequestAPITenantIsolationAndApproval(t *testing.T) {
	fixture := newQuotaRequestAPIFixture(t)
	f := fixture.f
	create := func(team, cookie string) string {
		t.Helper()
		response := f.do("POST", "/console/api/teams/"+team+"/quota-requests", cookie, `{"amountCredits":"2.500000","reason":"项目扩容申请人工授信"}`)
		if response.Code != 200 {
			t.Fatalf("申请失败: %d %s", response.Code, response.Body.String())
		}
		record := dataOf(t, response)["request"].(map[string]any)
		if record["status"] != "pending" {
			t.Fatal("申请应待人工审批")
		}
		return record["id"].(string)
	}
	id := create(fixture.teamID, fixture.adminCookie)
	create(fixture.otherTeamID, fixture.otherCookie)
	for _, request := range []struct{ method, path, cookie, body string }{{"GET", "/console/api/quota-requests?teamId=" + fixture.otherTeamID, fixture.adminCookie, ""}, {"POST", "/console/api/teams/" + fixture.otherTeamID + "/quota-requests", fixture.adminCookie, `{"amountCredits":"1","reason":"跨租户"}`}, {"GET", "/console/api/quota-requests", fixture.memberCookie, ""}, {"POST", "/console/api/teams/" + fixture.teamID + "/quota-requests", fixture.memberCookie, `{"amountCredits":"1","reason":"普通成员"}`}, {"POST", "/console/api/quota-requests/" + id + "/review", fixture.adminCookie, `{"decision":"approve"}`}} {
		response := f.do(request.method, request.path, request.cookie, request.body)
		if response.Code != 403 {
			t.Fatalf("越权请求必须403: %s %s got=%d %s", request.method, request.path, response.Code, response.Body.String())
		}
	}
	own := f.do("GET", "/console/api/quota-requests", fixture.adminCookie, "")
	if own.Code != 200 {
		t.Fatalf("查询本团队: %d %s", own.Code, own.Body.String())
	}
	records := dataOf(t, own)["requests"].([]any)
	if len(records) != 1 || records[0].(map[string]any)["teamId"] != fixture.teamID {
		t.Fatalf("列表只应包含本团队: %s", own.Body.String())
	}
	all := f.do("GET", "/console/api/quota-requests", fixture.rootCookie, "")
	if all.Code != 200 || len(dataOf(t, all)["requests"].([]any)) != 2 {
		t.Fatalf("root应看到全部申请: %d %s", all.Code, all.Body.String())
	}
	var success, conflict atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan string, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := f.do("POST", "/console/api/quota-requests/"+id+"/review", fixture.rootCookie, `{"decision":"approve","note":"人工核准授信"}`)
			switch response.Code {
			case 200:
				success.Add(1)
			case 409:
				conflict.Add(1)
			default:
				errs <- fmt.Sprintf("%d %s", response.Code, response.Body.String())
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if success.Load() != 1 || conflict.Load() != 15 {
		t.Fatalf("API并发审批应只成功一次: success=%d conflict=%d", success.Load(), conflict.Load())
	}
	team, _ := f.store.GetTeam(context.Background(), fixture.teamID)
	flows, _ := f.store.ListQuotaAdjustments(context.Background(), fixture.teamID, 10)
	if team.QuotaCredits != "7.500000" || len(flows) != 1 {
		t.Fatalf("批准不能重复加款: team=%+v flows=%+v", team, flows)
	}
	other, _ := f.store.GetTeam(context.Background(), fixture.otherTeamID)
	if other.QuotaCredits != "7.000000" {
		t.Fatalf("不能影响其他团队: %+v", other)
	}
}
func TestQuotaRequestAPIValidatesAmountAndReason(t *testing.T) {
	fixture := newQuotaRequestAPIFixture(t)
	f := fixture.f
	path := "/console/api/teams/" + fixture.teamID + "/quota-requests"
	for _, body := range []string{`{"amountCredits":"0","reason":"金额为零"}`, `{"amountCredits":"1000000000000","reason":"超出上限"}`, `{"amountCredits":"1.0000001","reason":"超精度"}`, `{"amountCredits":"1","reason":" "}`, `{"amountCredits":"1","reason":"正常","status":"approved"}`} {
		response := f.do("POST", path, fixture.adminCookie, body)
		if response.Code != 400 {
			t.Fatalf("非法参数必须400: body=%s got=%d %s", body, response.Code, response.Body.String())
		}
	}
	records, _ := f.store.ListQuotaRequests(context.Background(), fixture.teamID)
	if len(records) != 0 {
		t.Fatal("非法请求不应落库")
	}
}
