package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCreditLedgerOpeningGrantMigration(t *testing.T) {
	baseURL := os.Getenv("TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("未设置 TEST_DATABASE_URL,跳过初始额度升级测试")
	}
	migrationsDir := os.Getenv("TEST_MIGRATIONS_DIR")
	if migrationsDir == "" {
		migrationsDir = "../../migrations"
	}
	const migration = "0037_credit_ledger_opening_grants.sql"
	body, err := os.ReadFile(filepath.Join(migrationsDir, migration))
	if err != nil {
		t.Fatal(err)
	}
	previousDir := t.TempDir()
	names, err := listMigrationFiles(migrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= migration {
			continue
		}
		previous, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(previousDir, name), previous, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, closeStore := newPostgresForTest(t, baseURL, previousDir)
	defer closeStore()
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	// 升级夹具直接写历史表，避免当前 CreateTeam 的初始流水逻辑掩盖待回填账户。
	makeTeam := func(quota string, unlimited bool) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := s.pool.Exec(ctx, `INSERT INTO teams(id,name,quota_credits,unlimited,created_at) VALUES($1::uuid,$2,$3::numeric,$4,$5)`, id, id, quota, unlimited, at); err != nil {
			t.Fatal(err)
		}
		return id
	}
	adjust := func(team, id, action, oldQuota, newQuota string, when time.Time) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, `INSERT INTO team_quota_adjustments(id,team_id,action,old_quota_credits,new_quota_credits,old_unlimited,new_unlimited,created_at) VALUES($1::uuid,$2::uuid,$3,$4::numeric,$5::numeric,false,false,$6)`, id, team, action, oldQuota, newQuota, when); err != nil {
			t.Fatal(err)
		}
	}
	unchanged := makeTeam("12.345678", true)
	zero := makeTeam("0", false)
	adjusted := makeTeam("99", false)
	// 相同时间戳按 id 选最早事实；后来额度不可被误当作开户额度。
	adjust(adjusted, "00000000-0000-4000-8000-000000000002", "set_quota", "20", "99", at.Add(time.Hour))
	adjust(adjusted, "00000000-0000-4000-8000-000000000001", "add_credit", "7.500001", "20", at.Add(time.Hour))
	zeroOpening := makeTeam("4", false)
	adjust(zeroOpening, uuid.NewString(), "add_credit", "0", "4", at.Add(time.Hour))
	existing := makeTeam("8", false)
	existingID := uuid.NewString()
	adjust(existing, existingID, "initial_grant", "0", "3", at.Add(time.Minute))
	adjust(existing, uuid.NewString(), "add_credit", "3", "8", at.Add(time.Hour))
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = RunMigrations(ctx, conn.Conn(), migrationsDir, nil)
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	assertGrant := func(team, want string, unlimited bool) {
		t.Helper()
		var oldQuota, quota string
		var createdAt time.Time
		var actor *string
		var oldUnlimited, newUnlimited bool
		if err := s.pool.QueryRow(ctx, `SELECT old_quota_credits::text,new_quota_credits::text,created_at,actor_user_id::text,old_unlimited,new_unlimited FROM team_quota_adjustments WHERE team_id=$1::uuid AND action='initial_grant'`, team).Scan(&oldQuota, &quota, &createdAt, &actor, &oldUnlimited, &newUnlimited); err != nil {
			t.Fatal(err)
		}
		if oldQuota != "0.000000" || quota != want || !createdAt.Equal(at) || actor != nil || oldUnlimited || newUnlimited != unlimited {
			t.Fatalf("错误初始额度: old=%s new=%s at=%s actor=%v unlimited=%v/%v", oldQuota, quota, createdAt, actor, oldUnlimited, newUnlimited)
		}
	}
	assertGrant(unchanged, "12.345678", true)
	assertGrant(adjusted, "7.500001", false)
	for _, team := range []string{zero, zeroOpening} {
		var count int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM team_quota_adjustments WHERE team_id=$1::uuid AND action='initial_grant'`, team).Scan(&count); err != nil || count != 0 {
			t.Fatalf("零初始额度不应入账: count=%d err=%v", count, err)
		}
	}
	var savedID, quota string
	var savedAt time.Time
	if err := s.pool.QueryRow(ctx, `SELECT id::text,new_quota_credits::text,created_at FROM team_quota_adjustments WHERE team_id=$1::uuid AND action='initial_grant'`, existing).Scan(&savedID, &quota, &savedAt); err != nil || savedID != existingID || quota != "3.000000" || !savedAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("已有初始事实不得重写: %s %s %s %v", savedID, quota, savedAt, err)
	}
	// 即使重复执行 SQL 也不能重复入账，不能依赖迁移记账表掩盖幂等缺陷。
	if _, err := s.pool.Exec(ctx, string(body)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM team_quota_adjustments`).Scan(&count); err != nil || count != 7 {
		t.Fatalf("迁移丢失或重复历史事实: count=%d err=%v", count, err)
	}
	var currentQuota string
	if err := s.pool.QueryRow(ctx, `SELECT quota_credits::text FROM teams WHERE id=$1::uuid`, adjusted).Scan(&currentQuota); err != nil || currentQuota != "99.000000" {
		t.Fatalf("回填不得改变当前额度: %s %v", currentQuota, err)
	}
}
