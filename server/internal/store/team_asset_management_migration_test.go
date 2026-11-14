package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTeamAssetManagementMigrationPreservesExistingAssets(t *testing.T) {
	baseURL := os.Getenv("TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("未设置 TEST_DATABASE_URL，跳过团队资产管理升级测试")
	}
	dir := os.Getenv("TEST_MIGRATIONS_DIR")
	if dir == "" {
		dir = "../../migrations"
	}
	previous := t.TempDir()
	names, err := listMigrationFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "0040_team_asset_management.sql" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(previous, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, closeStore := newPostgresForTest(t, baseURL, previous)
	defer closeStore()
	ctx := context.Background()
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "asset-upgrade-" + uuid.NewString(), QuotaCredits: "0"})
	if err != nil {
		t.Fatal(err)
	}
	owner := mustUser(t, ctx, s, uuid.NewString()+"@asset-upgrade.test")
	if err := s.AddMember(ctx, team.ID, owner.ID, MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	record := testTeamAsset(owner.ID, 1, time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC), 7)
	// 当前写入依赖新 revision 列；升级夹具必须按已部署 0039 的列建立，保留真实内容和容量。
	if _, err := s.pool.Exec(ctx, `INSERT INTO team_assets(team_id,id,name,kind,mime,size,created_at,uploaded_by_id,uploaded_by_name,sha256,blob_id)
		VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8::uuid,$9,$10,$11::uuid)`, team.ID, record.ID, record.Name, record.Kind, record.Mime, record.Size, record.CreatedAt, record.UploadedByID, record.UploadedByName, record.SHA256, record.BlobID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE teams SET team_asset_used_bytes=$2 WHERE id=$1::uuid`, team.ID, record.Size); err != nil {
		t.Fatal(err)
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	for range 2 {
		if err := RunMigrations(ctx, conn.Conn(), dir, nil); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.QueryTeamAssets(ctx, team.ID, owner.ID, TeamAssetQuery{Limit: 100})
	if err != nil || len(page.Records) != 1 || page.UsedBytes != record.Size || page.Revision != "0" {
		t.Fatalf("migration changed asset or storage: %+v %v", page, err)
	}
	got := page.Records[0]
	// Postgres 返回的 Location 可以不同；原始时间点必须一致，其余资产字段仍逐项比较。
	if !got.CreatedAt.Equal(record.CreatedAt) {
		t.Fatal("migration changed creation time")
	}
	got.CreatedAt = record.CreatedAt
	if !reflect.DeepEqual(got.TeamAssetRecord, record) || len(got.Tags) != 0 || got.Favorite {
		t.Fatalf("migration changed existing content: %+v", got)
	}
	if err := s.SetTeamAssetFavorite(ctx, team.ID, owner.ID, record.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTeamAssetTags(ctx, team.ID, owner.ID, record.ID, []string{"campaign"}); err != nil {
		t.Fatal(err)
	}
	page, err = s.QueryTeamAssets(ctx, team.ID, owner.ID, TeamAssetQuery{Limit: 100, FavoritesOnly: true, Tag: "campaign"})
	if err != nil || len(page.Records) != 1 || page.Revision != "2" || page.UsedBytes != record.Size {
		t.Fatalf("upgraded asset cannot be managed: %+v %v", page, err)
	}
}
