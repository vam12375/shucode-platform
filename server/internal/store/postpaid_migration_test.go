package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPostpaidMigrationPreservesExecutionIdentity(t *testing.T) {
	baseURL := os.Getenv("TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("未设置 TEST_DATABASE_URL,跳过后付费升级测试")
	}
	migrationsDir := os.Getenv("TEST_MIGRATIONS_DIR")
	if migrationsDir == "" {
		migrationsDir = "../../migrations"
	}
	previousDir := t.TempDir()
	names, err := listMigrationFiles(migrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "0025_postpaid_usage.sql" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(previousDir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, closeStore := newPostgresForTest(t, baseURL, previousDir)
	defer closeStore()
	ctx := context.Background()
	team, user := postpaidFixture(t, ctx, s, "migration")
	at := time.Now().UTC().Truncate(time.Millisecond)
	usageID := uuid.NewString()
	if err := s.InsertUsage(ctx, UsageRecord{ID: usageID, TeamID: team.ID, UserID: user.ID, RequestID: "historical-usage", Model: "m", UpstreamModel: "m", CreditsMicro: 250_000, Status: UsageStatusOk, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	identities := map[QuotaReservationStatus]string{}
	for _, state := range []QuotaReservationStatus{ReservationReserved, ReservationExpired, ReservationSettled, ReservationReleased} {
		id := uuid.NewString()
		identities[state] = id
		var settled, released *time.Time
		var historicalUsage any
		if state == ReservationSettled {
			settled = &at
			historicalUsage = usageID
		}
		if state == ReservationReleased || state == ReservationExpired {
			released = &at
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO quota_reservations
			(id,team_id,user_id,request_id,reserved_credits,status,created_at,expires_at,settled_at,released_at,usage_id)
			VALUES($1::uuid,$2::uuid,$3::uuid,$4,0.800000,$5,$6,$6,$7,$8,$9::uuid)`, id, team.ID, user.ID, "migrate-"+string(state), state, at, settled, released, historicalUsage); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE teams SET used_credits=0.250000,reserved_credits=0.800000 WHERE id=$1::uuid`, team.ID); err != nil {
		t.Fatal(err)
	}
	activeID := identities[ReservationReserved]
	actual := UsageRecord{ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID, RequestID: "migrate-reserved", Model: "m", UpstreamModel: "m", CreditsMicro: 1_500_000, Status: UsageStatusOk, CreatedAt: at}
	if err := s.EnqueuePendingSettlement(ctx, activeID, actual); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBackgroundGeneration(ctx, BackgroundGenerationJob{ReservationID: activeID, TeamID: team.ID, UserID: user.ID, ResponseID: "migration-response", BaseURL: "https://provider.test", Record: actual, NextPollAt: at, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = RunMigrations(ctx, conn.Conn(), migrationsDir, nil)
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.GetTeam(ctx, team.ID)
	if err != nil || updated.ReservedCredits != "0.000000" || updated.UsedCredits != "0.250000" {
		t.Fatalf("迁移只移除占款，不改历史实扣: %+v %v", updated, err)
	}
	for state, id := range identities {
		identity, err := s.GetQuotaReservation(ctx, id)
		if err != nil || identity == nil || identity.Status != state || identity.UserID != user.ID || identity.RequestID != "migrate-"+string(state) {
			t.Fatalf("原身份与归属不能变更: %+v %v", identity, err)
		}
		if state == ReservationReserved || state == ReservationExpired {
			if !identity.Postpaid || identity.ReservedMicro != 0 || !identity.RetainUntilSettled {
				t.Fatalf("未结算身份必须无占款且可恢复: %+v", identity)
			}
		} else if identity.Postpaid || identity.ReservedMicro != 800_000 {
			t.Fatalf("历史终态金额不改写: %+v", identity)
		}
		if state == ReservationSettled && identity.UsageID != usageID {
			t.Fatalf("历史 usage 关联不能丢失: %+v", identity)
		}
	}
	pending, err := s.ListPendingSettlements(ctx, PendingSettlementFilter{})
	if err != nil || len(pending) != 1 || pending[0].ReservationID != activeID || pending[0].Record.CreditsMicro != actual.CreditsMicro {
		t.Fatalf("补偿原身份和费用不能丢失: %+v %v", pending, err)
	}
	job, err := s.FindBackgroundGeneration(ctx, team.ID, "migration-response")
	if err != nil || job == nil || job.ReservationID != activeID || job.Record.CreditsMicro != actual.CreditsMicro {
		t.Fatalf("后台原身份和费用不能丢失: %+v %v", job, err)
	}
	if err := s.SettleReservation(ctx, activeID, actual); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleReservation(ctx, activeID, actual); err != nil {
		t.Fatal(err)
	}
	actual.ID = uuid.NewString()
	actual.RequestID = "migrate-expired"
	if err := s.SettleReservation(ctx, identities[ReservationExpired], actual); err != nil {
		t.Fatal(err)
	}
	updated, _ = s.GetTeam(ctx, team.ID)
	if updated.UsedCredits != "3.250000" || updated.ReservedCredits != "0.000000" {
		t.Fatalf("升级后的旧请求完整实扣且不重复扣款: %+v", updated)
	}
}
