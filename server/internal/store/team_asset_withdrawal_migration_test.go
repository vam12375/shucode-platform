package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTeamAssetWithdrawalMigrationPreservesExistingRequests(t *testing.T) {
	baseURL := os.Getenv("TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("未设置 TEST_DATABASE_URL,跳过撤回升级测试")
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
		if name >= "0030_team_asset_deletion_withdrawals.sql" {
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
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "withdraw-upgrade-" + uuid.NewString(), QuotaCredits: "0"})
	if err != nil {
		t.Fatal(err)
	}
	author := mustUser(t, ctx, s, uuid.NewString()+"@withdraw-upgrade.test")
	if err := s.AddMember(ctx, team.ID, author.ID, MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	requests := map[string]string{}
	for _, status := range []string{"pending", "approved", "rejected"} {
		id := uuid.NewString()
		requests[status] = id
		var reviewer, reviewedAt, cleanedAt any
		if status != "pending" {
			reviewer, reviewedAt = author.ID, at
		}
		if status == "approved" {
			cleanedAt = at
		}
		// 升级夹具按已部署0029列建数，不调用依赖0030新列的当前写入接口。
		if _, err := s.pool.Exec(ctx, `INSERT INTO team_asset_deletion_requests
			(team_id,id,asset_id,asset_name,requested_by_id,requested_by_name,created_at,status,reviewed_by_id,reviewed_at,blob_id,size,blob_cleaned_at)
			VALUES($1::uuid,$2::uuid,$3::uuid,'E2E_UPGRADE.md',$4::uuid,'author',$5,$6,$7::uuid,$8,$9::uuid,7,$10)`, team.ID, id, uuid.NewString(), author.ID, at, status, reviewer, reviewedAt, uuid.NewString(), cleanedAt); err != nil {
			t.Fatal(err)
		}
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
	page, _, err := s.ListTeamAssetDeletionRequests(ctx, team.ID, author.ID, nil, "")
	if err != nil || len(page.Records) != 3 || page.PendingCount != 1 {
		t.Fatalf("migration lost requests: %+v %v", page, err)
	}
	for _, r := range page.Records {
		if r.ID != requests[r.Status] || r.WithdrawnAt != nil || (r.ReviewedAt == nil) != (r.Status == "pending") || (r.BlobCleanedAt != nil) != (r.Status == "approved") {
			t.Fatalf("migration changed historical fact: %+v", r)
		}
	}
	if r, err := s.WithdrawTeamAssetDeletion(ctx, team.ID, author.ID, requests["pending"], at.Add(time.Hour)); err != nil || r.Status != "withdrawn" || r.WithdrawnAt == nil {
		t.Fatalf("historical pending cannot be withdrawn: %+v %v", r, err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE team_asset_deletion_requests SET reviewed_at=$3 WHERE team_id=$1::uuid AND id=$2::uuid`, team.ID, requests["pending"], at); err == nil {
		t.Fatal("database allowed withdrawal to masquerade as review")
	}
}
