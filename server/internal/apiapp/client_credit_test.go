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
	// 旧记录兼容字段不能再影响面向用户的已结算余额。
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
	if data["reservedCredits"] != "0.000000" {
		t.Fatalf("预留额度不符: %v", data["reservedCredits"])
	}
	if data["remainingCredits"] != "10.000000" {
		t.Fatalf("剩余额度不符: %v", data["remainingCredits"])
	}
	usedRatio, ok := data["usedRatio"].(float64)
	if !ok || usedRatio != 0 {
		t.Fatalf("已用比例不符: %v", data["usedRatio"])
	}

	if unauthorized := requestClientCredit(t, f, ""); unauthorized.Code != 401 {
		t.Fatalf("缺少凭据应 401: %d", unauthorized.Code)
	}
}

func TestClientCreditShowsFullPostpaidDebt(t *testing.T) {
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
	requestID := "postpaid-debt"
	identityID := uuid.NewString()
	result, err := f.store.AdmitUsage(ctx, store.ReserveQuotaInput{
		ID: identityID, TeamID: team.ID, UserID: user.ID, RequestID: requestID, CreatedAt: time.Now(), TTL: time.Minute,
	})
	if err != nil || result != store.ReserveReserved {
		t.Fatalf("正余额应准入且不占用余额: %s %v", result, err)
	}
	if err := f.store.SettleReservation(ctx, identityID, store.UsageRecord{
		UserID: user.ID, TeamID: team.ID, Model: "m", UpstreamModel: "m",
		RequestID: requestID, CreditsMicro: 12_500_000,
		Status: store.UsageStatusOk, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("已准入真实费用完整入账: %v", err)
	}
	token := createDeviceTokenForTest(t, f, user.ID)
	rec := requestClientCredit(t, f, token)
	if rec.Code != 200 {
		t.Fatalf("额度接口: %d %s", rec.Code, rec.Body.String())
	}
	data := dataOf(t, rec)
	if data["remainingCredits"] != "-2.500000" {
		t.Fatalf("负余额必须准确展示完整费用: %v", data["remainingCredits"])
	}
	if data["usedRatio"] != float64(1) {
		t.Fatalf("负余额的已用比例应饱和到 100%%: %v", data["usedRatio"])
	}
	result, err = f.store.AdmitUsage(ctx, store.ReserveQuotaInput{
		ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID, RequestID: "after-debt", CreatedAt: time.Now(), TTL: time.Minute,
	})
	if err != nil || result != store.ReserveDenied {
		t.Fatalf("已结算负余额必须阻止新请求: result=%s err=%v", result, err)
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
	remainingMicro := quotaMicro - usedMicro
	if data["remainingCredits"] != credits.FormatMicroCredits(remainingMicro) {
		t.Fatalf("剩余额度不符: got %v want %s", data["remainingCredits"], credits.FormatMicroCredits(remainingMicro))
	}
}
