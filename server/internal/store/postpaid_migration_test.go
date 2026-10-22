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
	// 根因：升级夹具只有 0024 表结构，不能调用依赖后续字段的当前写入接口；按历史列直接建数。
	if _, err := s.pool.Exec(ctx, `INSERT INTO usage_records
		(id,team_id,user_id,request_id,model,upstream_model,credits_micro,status,created_at)
		VALUES($1::uuid,$2::uuid,$3::uuid,'historical-usage','m','m',250000,'ok',$4)`, usageID, team.ID, user.ID, at); err != nil {
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
	if _, err := s.pool.Exec(ctx, `INSERT INTO pending_settlements
		(reservation_id,request_id,user_id,team_id,model,upstream_model,credits_micro,usage_status,usage_created_at)
		VALUES($1::uuid,$2,$3::uuid,$4::uuid,$5,$6,$7,$8,$9)`, activeID, actual.RequestID, actual.UserID, actual.TeamID, actual.Model, actual.UpstreamModel, actual.CreditsMicro, actual.Status, actual.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO gateway_background_generations
		(reservation_id,team_id,user_id,response_id,base_url,record_template,charge_rates,next_poll_at,created_at)
		VALUES($1::uuid,$2::uuid,$3::uuid,'migration-response','https://provider.test',
			jsonb_build_object('id',$4::text,'teamId',$2::text,'userId',$3::text,'requestId',$5::text,
				'model','m','upstreamModel','m','creditsMicro',$6::bigint,'status','ok','createdAt',$7::timestamptz),
			'{}'::jsonb,$7,$7)`, activeID, team.ID, user.ID, actual.ID, actual.RequestID, actual.CreditsMicro, at); err != nil {
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
	// 0028 只能新增未知参数列，不能以当前模型配置回填旧账本或补偿记录。
	for _, legacy := range []struct{ query, id string }{
		{`SELECT reasoning_effort,reasoning_budget_tokens FROM usage_records WHERE id=$1::uuid`, usageID},
		{`SELECT reasoning_effort,reasoning_budget_tokens FROM pending_settlements WHERE reservation_id=$1::uuid`, activeID},
	} {
		var effort *string
		var budget *int64
		if err := s.pool.QueryRow(ctx, legacy.query, legacy.id).Scan(&effort, &budget); err != nil || effort != nil || budget != nil {
			t.Fatalf("历史思考参数必须保留 SQL NULL: effort=%v budget=%v err=%v", effort, budget, err)
		}
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
	if pending[0].Record.ReasoningEffort != "" || pending[0].Record.ReasoningBudgetTokens != nil {
		t.Fatalf("历史补偿不能伪造思考参数: %+v", pending[0].Record)
	}
	job, err := s.FindBackgroundGeneration(ctx, team.ID, "migration-response")
	if err != nil || job == nil || job.ReservationID != activeID || job.Record.CreditsMicro != actual.CreditsMicro {
		t.Fatalf("后台原身份和费用不能丢失: %+v %v", job, err)
	}
	if job.Record.ReasoningEffort != "" || job.Record.ReasoningBudgetTokens != nil {
		t.Fatalf("历史后台模板不能伪造思考参数: %+v", job.Record)
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
