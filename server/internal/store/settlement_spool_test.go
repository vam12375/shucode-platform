package store_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/compensation"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

type unavailableSettlementInbox struct{ store.PlatformStore }

func (unavailableSettlementInbox) EnqueuePendingSettlement(context.Context, string, store.UsageRecord) error {
	return errors.New("inbox unavailable")
}

func TestPendingSettlementSpoolToPostgres(t *testing.T) {
	ctx := context.Background()
	st := store.NewIntegrationPostgres(t)
	user, err := st.CreateUser(ctx, store.UserInput{Email: "spool@example.test", PasswordHash: "test", Role: store.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	team, err := st.CreateTeam(ctx, store.CreateTeamInput{Name: "spool", QuotaCredits: "1.000000"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddMember(ctx, team.ID, user.ID, store.MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	id, now := uuid.NewString(), time.Now().UTC()
	input := store.ReserveQuotaInput{ID: id, UserID: user.ID, TeamID: team.ID, RequestID: "spool-request", CreatedAt: now, TTL: time.Hour}
	if result, err := st.AdmitUsage(ctx, input); err != nil || result != store.ReserveReserved {
		t.Fatalf("准入失败: %v %v", result, err)
	}
	record := store.UsageRecord{RequestID: input.RequestID, UserID: user.ID, TeamID: team.ID, Model: "test", UpstreamModel: "test",
		Status: store.UsageStatusError, ErrorType: "background_submission_unknown", BackgroundResponseID: "resp_saved", CreatedAt: now}
	dir := t.TempDir()
	failing := compensation.NewSettlementCompensator(unavailableSettlementInbox{st}, dir, nil, nil)
	unknownID := record
	unknownID.BackgroundResponseID = ""
	if err := failing.Persist(ctx, id, unknownID); err == nil {
		t.Fatal("模拟数据库中断，应保留原始补偿文件")
	}
	if err := failing.Persist(ctx, id, record); err == nil {
		t.Fatal("恢复到的任务号也应落补偿文件")
	}
	worker := compensation.NewSettlementCompensator(st, dir, nil, func() time.Time { return now.Add(time.Minute) })
	if err := worker.Persist(ctx, id, unknownID); err != nil {
		t.Fatal(err)
	}
	if err := worker.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := st.ListPendingSettlements(ctx, store.PendingSettlementFilter{Status: store.PendingSettlementFailed})
	if err != nil || len(pending) != 1 || pending[0].Record.BackgroundResponseID != record.BackgroundResponseID ||
		pending[0].Record.RequestID != input.RequestID || pending[0].LastError != "background_submission_requires_review" {
		t.Fatalf("入库回灌后必须保留可核对身份: %+v %v", pending, err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("入库后应清除已回灌文件: %+v %v", files, err)
	}
	records, err := st.ListUsage(ctx, store.UsageFilter{TeamID: team.ID})
	if err != nil || len(records) != 0 {
		t.Fatalf("待核对请求不应产生用量: %+v %v", records, err)
	}
	wallet, err := st.GetTeam(ctx, team.ID)
	if err != nil || wallet.UsedCredits != "0.000000" || wallet.ReservedCredits != "0.000000" {
		t.Fatalf("待核对请求不应占款或扣款: %+v %v", wallet, err)
	}
}
