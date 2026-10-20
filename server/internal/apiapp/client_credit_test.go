package apiapp

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/credits"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func createDeviceTokenForTest(t *testing.T, f *apiFixture, userID string) string {
	t.Helper()
	token := crypto.GenerateDeviceToken()
	ciphertext, err := f.deps.SecretBox.Encrypt(token)
	if err != nil {
		t.Fatalf("加密设备凭据: %v", err)
	}
	if err := f.store.CreateDeviceToken(context.Background(), store.DeviceTokenRecord{
		ID:              uuid.NewString(),
		UserID:          userID,
		Name:            "额度测试设备",
		TokenHash:       crypto.HashToken(token),
		TokenCiphertext: ciphertext,
		TokenHint:       crypto.DeviceTokenHint(token),
		CreatedAt:       time.Now(),
	}); err != nil {
		t.Fatalf("写入设备凭据: %v", err)
	}
	return token
}

func requestClientCredit(t *testing.T, f *apiFixture, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/client/credit", nil)
	if token != "" {
		req.Header.Set("authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	return rec
}

func TestClientCreditSnapshot(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	user, err := f.store.CreateUser(ctx, store.UserInput{
		Email: "credit-member@example.com", PasswordHash: "x", DisplayName: "Credit Member", Role: store.RoleUser,
	})
	if err != nil {
		t.Fatalf("创建用户: %v", err)
	}
	team, err := f.store.CreateTeam(ctx, store.CreateTeamInput{
		Name: "额度团队", QuotaCredits: "10", Unlimited: false,
	})
	if err != nil {
		t.Fatalf("创建团队: %v", err)
	}
	if err := f.store.AddMember(ctx, team.ID, user.ID, store.MemberRoleMember); err != nil {
		t.Fatalf("加入团队: %v", err)
	}
	// 预留 0.25 credit，验证接口把 reserved 计入余额和已用比例。
	reserved, err := f.store.TryReserveQuota(ctx, team.ID, 250_000)
	if err != nil || !reserved {
		t.Fatalf("写入预留: ok=%v err=%v", reserved, err)
	}
	token := createDeviceTokenForTest(t, f, user.ID)

	rec := requestClientCredit(t, f, token)
	if rec.Code != 200 {
		t.Fatalf("额度接口: %d %s", rec.Code, rec.Body.String())
	}
	data := dataOf(t, rec)
	if data["teamId"] != team.ID || data["teamName"] != "额度团队" {
		t.Fatalf("团队信息不符: %v", data)
	}
	if data["unlimited"] != false {
		t.Fatalf("unlimited 不符: %v", data["unlimited"])
	}
	if data["quotaCredits"] != "10.000000" {
		t.Fatalf("总额度不符: %v", data["quotaCredits"])
	}
	if data["usedCredits"] != "0.000000" {
		t.Fatalf("已用额度不符: %v", data["usedCredits"])
	}
	if data["reservedCredits"] != "0.250000" {
		t.Fatalf("预留额度不符: %v", data["reservedCredits"])
	}
	if data["remainingCredits"] != "9.750000" {
		t.Fatalf("剩余额度不符: %v", data["remainingCredits"])
	}
	usedRatio, ok := data["usedRatio"].(float64)
	if !ok || usedRatio != 0.025 {
		t.Fatalf("已用比例不符: %v", data["usedRatio"])
	}

	if unauthorized := requestClientCredit(t, f, ""); unauthorized.Code != 401 {
		t.Fatalf("缺少凭据应 401: %d", unauthorized.Code)
	}
}

func TestClientCreditShowsConfiguredDebtFloor(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	user, err := f.store.CreateUser(ctx, store.UserInput{
		Email: "credit-floor@example.com", PasswordHash: "x", DisplayName: "Credit Floor", Role: store.RoleUser,
	})
	if err != nil {
		t.Fatalf("创建用户: %v", err)
	}
	team, err := f.store.CreateTeam(ctx, store.CreateTeamInput{Name: "额度下限团队", QuotaCredits: "10", Unlimited: false})
	if err != nil {
		t.Fatalf("创建团队: %v", err)
	}
	if err := f.store.AddMember(ctx, team.ID, user.ID, store.MemberRoleMember); err != nil {
		t.Fatalf("加入团队: %v", err)
	}
	reserved, err := f.store.TryReserveQuota(ctx, team.ID, 10_000_000)
	if err != nil || !reserved {
		t.Fatalf("应允许预留到 0 credit: reserved=%v err=%v", reserved, err)
	}
	if err := f.store.SettleUsage(ctx, store.UsageRecord{
		UserID: user.ID, TeamID: team.ID, Model: "m", UpstreamModel: "m",
		RequestID: "floor-settle", CreditsMicro: 10_000_000 + store.TeamCreditDebtLimitMicro,
		Status: store.UsageStatusOk, CreatedAt: time.Now(),
	}, 10_000_000); err != nil {
		t.Fatalf("已准入请求可结算至尾差下限: %v", err)
	}
	token := createDeviceTokenForTest(t, f, user.ID)
	rec := requestClientCredit(t, f, token)
	if rec.Code != 200 {
		t.Fatalf("额度接口: %d %s", rec.Code, rec.Body.String())
	}
	data := dataOf(t, rec)
	if data["remainingCredits"] != "-0.010000" {
		t.Fatalf("负余额必须准确展示至硬下限: %v", data["remainingCredits"])
	}
	if data["usedRatio"] != float64(1) {
		t.Fatalf("已用比例在下限处应饱和到 100%%: %v", data["usedRatio"])
	}
	allowed, err := f.store.TryReserveQuota(ctx, team.ID, 1)
	if err != nil || allowed {
		t.Fatalf("达到 -0.01 后必须阻止新请求: allowed=%v err=%v", allowed, err)
	}
}

func TestClientCreditRequiresTeam(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	user, err := f.store.CreateUser(ctx, store.UserInput{
		Email: "no-team@example.com", PasswordHash: "x", DisplayName: "No Team", Role: store.RoleUser,
	})
	if err != nil {
		t.Fatalf("创建用户: %v", err)
	}
	token := createDeviceTokenForTest(t, f, user.ID)
	rec := requestClientCredit(t, f, token)
	if rec.Code != 403 {
		t.Fatalf("无团队应 403: %d %s", rec.Code, rec.Body.String())
	}
}

func TestClientCreditFormatsExistingTeamValues(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	member, err := f.store.FindUserByEmail(ctx, "member@mendao.chat")
	if err != nil || member == nil {
		t.Fatalf("读取演示成员: %v", err)
	}
	membership, err := store.ResolveUserTeam(ctx, f.store, member.ID)
	if err != nil || membership == nil {
		t.Fatalf("读取演示团队: %v", err)
	}
	token := createDeviceTokenForTest(t, f, member.ID)
	rec := requestClientCredit(t, f, token)
	if rec.Code != 200 {
		t.Fatalf("额度接口: %d %s", rec.Code, rec.Body.String())
	}
	data := dataOf(t, rec)
	quotaMicro, err := credits.ParseCreditsToMicro(membership.Team.QuotaCredits)
	if err != nil {
		t.Fatal(err)
	}
	usedMicro, err := credits.ParseCreditsToMicro(membership.Team.UsedCredits)
	if err != nil {
		t.Fatal(err)
	}
	reservedMicro, err := credits.ParseCreditsToMicro(membership.Team.ReservedCredits)
	if err != nil {
		t.Fatal(err)
	}
	remainingMicro := quotaMicro - usedMicro - reservedMicro
	if data["remainingCredits"] != credits.FormatMicroCredits(remainingMicro) {
		t.Fatalf("剩余额度不符: got %v want %s", data["remainingCredits"], credits.FormatMicroCredits(remainingMicro))
	}
}
