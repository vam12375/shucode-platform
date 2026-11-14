package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMemoryTeamAssetManagement(t *testing.T) {
	s, closeStore := newMemoryForTest(t)
	defer closeStore()
	runTeamAssetManagement(t, s)
}

func TestPostgresTeamAssetManagement(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL 未设置")
	}
	s, closeStore := newPostgresForTest(t, os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_MIGRATIONS_DIR"))
	defer closeStore()
	runTeamAssetManagement(t, s)
}

func runTeamAssetManagement(t *testing.T, s PlatformStore) {
	ctx := context.Background()
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: uuid.NewString(), QuotaCredits: "0"})
	if err != nil {
		t.Fatal(err)
	}
	owner := mustUser(t, ctx, s, uuid.NewString()+"@test.local")
	member := mustUser(t, ctx, s, uuid.NewString()+"@test.local")
	for _, pair := range []struct {
		id   string
		role TeamMemberRole
	}{{owner.ID, MemberRoleOwner}, {member.ID, MemberRoleMember}} {
		if err := s.AddMember(ctx, team.ID, pair.id, pair.role); err != nil {
			t.Fatal(err)
		}
	}
	var id string
	for i := 0; i < 105; i++ {
		record := testTeamAsset(owner.ID, i+1, time.Unix(int64(i), 0), 1)
		if i == 0 {
			record.Name = "needle.md"
			id = record.ID
		}
		if _, _, err := s.CommitTeamAsset(ctx, team.ID, record, 1000); err != nil {
			t.Fatal(err)
		}
	}
	// 排序查询跨页读取，不能重复或漏掉第101条以后的记录。
	for _, order := range []string{"newest", "oldest", "name", "size"} {
		seen := map[string]bool{}
		for offset := 0; offset < 105; offset += 37 {
			p, err := s.QueryTeamAssets(ctx, team.ID, member.ID, TeamAssetQuery{Sort: order, Limit: 37, Offset: offset})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range p.Records {
				if seen[r.ID] {
					t.Fatal("duplicate page item")
				}
				seen[r.ID] = true
			}
		}
		if len(seen) != 105 {
			t.Fatalf("incomplete %s pages: %d", order, len(seen))
		}
	}
	if _, err := s.QueryTeamAssets(ctx, team.ID, member.ID, TeamAssetQuery{Limit: 10, Q: "bad\nquery"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("control query accepted")
	}
	if err := s.SetTeamAssetTags(ctx, team.ID, owner.ID, id, []string{"🎬🎬"}); err != nil {
		t.Fatal(err)
	}
	query := TeamAssetQuery{Q: "needle", Sort: "newest", Limit: 101}
	first, err := s.QueryTeamAssets(ctx, team.ID, member.ID, query)
	if err != nil || len(first.Records) != 1 || first.Records[0].ID != id || first.Facets.Total != 105 {
		t.Fatalf("query/facets: %+v %v", first, err)
	}
	if err := s.SetTeamAssetFavorite(ctx, team.ID, member.ID, id, true); err != nil {
		t.Fatal(err)
	}
	query.FavoritesOnly = true
	page, err := s.QueryTeamAssets(ctx, team.ID, member.ID, query)
	if err != nil || len(page.Records) != 1 || !page.Records[0].Favorite || page.Revision == first.Revision {
		t.Fatalf("favorite: %+v %v", page, err)
	}
	revision := page.Revision
	if err := s.SetTeamAssetFavorite(ctx, team.ID, member.ID, id, true); err != nil {
		t.Fatal(err)
	}
	page, _ = s.QueryTeamAssets(ctx, team.ID, member.ID, query)
	if page.Revision != revision {
		t.Fatal("idempotent favorite changed revision")
	}
	other, _ := s.QueryTeamAssets(ctx, team.ID, owner.ID, query)
	if len(other.Records) != 0 {
		t.Fatal("favorite leaked to another user")
	}
	if err := s.SetTeamAssetTags(ctx, team.ID, member.ID, id, []string{"shared"}); !errors.Is(err, ErrTeamAssetForbidden) {
		t.Fatalf("other member tags: %v", err)
	}
	if err := s.SetTeamAssetTags(ctx, team.ID, owner.ID, id, []string{" shared ", "shared"}); err != nil {
		t.Fatal(err)
	}
	query.Tag = "shared"
	page, err = s.QueryTeamAssets(ctx, team.ID, member.ID, query)
	if err != nil || len(page.Records) != 1 || len(page.Records[0].Tags) != 1 {
		t.Fatalf("tags: %+v %v", page, err)
	}
	page.Records[0].Tags[0] = "mutated"
	again, _ := s.QueryTeamAssets(ctx, team.ID, member.ID, query)
	if again.Records[0].Tags[0] != "shared" {
		t.Fatal("query returned mutable store alias")
	}
	query.ExpectedRevision = first.Revision
	if _, err := s.QueryTeamAssets(ctx, team.ID, member.ID, query); !errors.Is(err, ErrTeamAssetSnapshotChanged) {
		t.Fatalf("stale cursor: %v", err)
	}
}
