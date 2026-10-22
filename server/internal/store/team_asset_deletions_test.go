package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMemoryTeamAssetDeletionsConformance(t *testing.T) {
	s, closeStore := newMemoryForTest(t)
	defer closeStore()
	runTeamAssetDeletionConformance(t, s)
}
func TestPostgresTeamAssetDeletionsConformance(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 TEST_DATABASE_URL,跳过 Postgres 删除审批 conformance")
	}
	s, closeStore := newPostgresForTest(t, url, os.Getenv("TEST_MIGRATIONS_DIR"))
	defer closeStore()
	runTeamAssetDeletionConformance(t, s)
}

func listTeamAssetDeletions(s PlatformStore, ctx context.Context, team, actor string) ([]TeamAssetDeletionRecord, bool, error) {
	page, canReview, err := s.ListTeamAssetDeletionRequests(ctx, team, actor, nil, "")
	return page.Records, canReview, err
}
func runTeamAssetDeletionConformance(t *testing.T, s PlatformStore) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "asset-review-" + uuid.NewString(), QuotaCredits: "0"})
	if err != nil {
		t.Fatal(err)
	}
	admin := mustUser(t, ctx, s, uuid.NewString()+"@review.test")
	author := mustUser(t, ctx, s, uuid.NewString()+"@review.test")
	other := mustUser(t, ctx, s, uuid.NewString()+"@review.test")
	for id, role := range map[string]TeamMemberRole{admin.ID: MemberRoleOwner, author.ID: MemberRoleMember, other.ID: MemberRoleMember} {
		if err := s.AddMember(ctx, team.ID, id, role); err != nil {
			t.Fatal(err)
		}
	}
	asset := testTeamAsset(author.ID, 8000, now, 7)
	if _, _, err := s.CommitTeamAsset(ctx, team.ID, asset, 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestTeamAssetDeletion(ctx, team.ID, other.ID, asset.ID, uuid.NewString(), now); !errors.Is(err, ErrTeamAssetForbidden) {
		t.Fatalf("other member request: %v", err)
	}
	var wg sync.WaitGroup
	requests := make(chan TeamAssetDeletionRecord, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.RequestTeamAssetDeletion(ctx, team.ID, author.ID, asset.ID, uuid.NewString(), now)
			if err != nil {
				t.Error(err)
				return
			}
			requests <- r
		}()
	}
	wg.Wait()
	close(requests)
	var request TeamAssetDeletionRecord
	for r := range requests {
		if request.ID != "" && request.ID != r.ID {
			t.Fatal("duplicate pending")
		}
		request = r
	}
	if request.Status != "pending" {
		t.Fatalf("not pending: %+v", request)
	}
	own, canReview, err := listTeamAssetDeletions(s, ctx, team.ID, author.ID)
	if err != nil || canReview || len(own) != 1 {
		t.Fatalf("own list: %+v %v %v", own, canReview, err)
	}
	hidden, _, err := listTeamAssetDeletions(s, ctx, team.ID, other.ID)
	if err != nil || len(hidden) != 0 {
		t.Fatalf("other member list: %+v %v", hidden, err)
	}
	if _, err := s.ReviewTeamAssetDeletion(ctx, team.ID, other.ID, request.ID, "approve", now); !errors.Is(err, ErrTeamAssetForbidden) {
		t.Fatalf("member review: %v", err)
	}
	rejected, err := s.ReviewTeamAssetDeletion(ctx, team.ID, admin.ID, request.ID, "reject", now)
	if err != nil || rejected.Status != "rejected" {
		t.Fatalf("reject: %+v %v", rejected, err)
	}
	if got, _ := s.GetTeamAsset(ctx, team.ID, asset.ID); got == nil {
		t.Fatal("rejection deleted asset")
	}
	if _, err := s.ReviewTeamAssetDeletion(ctx, team.ID, admin.ID, request.ID, "approve", now); !errors.Is(err, ErrTeamAssetConflict) {
		t.Fatalf("reverse decision: %v", err)
	}
	request, err = s.RequestTeamAssetDeletion(ctx, team.ID, author.ID, asset.ID, uuid.NewString(), now.Add(time.Second))
	if err != nil || request.ID == rejected.ID {
		t.Fatalf("retry after reject: %+v %v", request, err)
	}
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.ReviewTeamAssetDeletion(ctx, team.ID, admin.ID, request.ID, "approve", now.Add(2*time.Second))
			if err != nil || r.Status != "approved" {
				t.Errorf("concurrent approve: %+v %v", r, err)
			}
		}()
	}
	wg.Wait()
	page, err := s.ListTeamAssetPage(ctx, team.ID, nil, "", 10)
	if err != nil || len(page.Records) != 0 || page.UsedBytes != 0 {
		t.Fatalf("atomic removal/quota: %+v %v", page, err)
	}
	if _, _, err := s.CommitTeamAsset(ctx, team.ID, asset, 1000); !errors.Is(err, ErrTeamAssetConflict) {
		t.Fatalf("approved id resurrected: %v", err)
	}
	reuse := asset
	reuse.ID = uuid.NewString()
	if _, _, err := s.CommitTeamAsset(ctx, team.ID, reuse, 1000); !errors.Is(err, ErrTeamAssetConflict) {
		t.Fatalf("approved blob reused: %v", err)
	}
	if err := s.MarkTeamAssetDeletionBlobCleaned(ctx, team.ID, request.ID, now); err != nil {
		t.Fatal(err)
	}
	rows, canReview, err := listTeamAssetDeletions(s, ctx, team.ID, admin.ID)
	if err != nil || !canReview || len(rows) != 2 {
		t.Fatalf("admin list: %+v %v %v", rows, canReview, err)
	}
	for _, r := range rows {
		if r.Status == "approved" && r.BlobCleanedAt == nil {
			t.Fatal("cleanup fact lost")
		}
		if r.ReviewedAt != nil {
			*r.ReviewedAt = time.Time{}
		}
	}
	again, _, _ := listTeamAssetDeletions(s, ctx, team.ID, admin.ID)
	for _, r := range again {
		if r.ReviewedAt == nil || r.ReviewedAt.IsZero() {
			t.Fatal("returned pointer aliases persisted fact")
		}
	}
	if _, err := s.RequestTeamAssetDeletion(ctx, team.ID, author.ID, uuid.NewString(), uuid.NewString(), now); !errors.Is(err, ErrTeamAssetNotFound) {
		t.Fatalf("unknown/cross team asset: %v", err)
	}
	if _, err := s.ReviewTeamAssetDeletion(ctx, team.ID, admin.ID, uuid.NewString(), "approve", now); !errors.Is(err, ErrTeamAssetNotFound) {
		t.Fatalf("unknown/cross team request: %v", err)
	}
	disabled := now
	if _, err := s.UpdateTeamMember(ctx, team.ID, author.ID, TeamMemberPatch{HasDisabled: true, DisabledAt: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := listTeamAssetDeletions(s, ctx, team.ID, author.ID); !errors.Is(err, ErrTeamAssetForbidden) {
		t.Fatalf("disabled member list: %v", err)
	}

	t.Run("all-pending-and-review-time-history", func(t *testing.T) {
		var first TeamAssetDeletionRecord
		for i := range 103 {
			asset := testTeamAsset(admin.ID, 8100+i, now, 1)
			if _, _, err := s.CommitTeamAsset(ctx, team.ID, asset, 1000); err != nil {
				t.Fatal(err)
			}
			r, err := s.RequestTeamAssetDeletion(ctx, team.ID, admin.ID, asset.ID, uuid.NewString(), now)
			if err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				first = r
			}
		}
		firstPage, _, err := s.ListTeamAssetDeletionRequests(ctx, team.ID, admin.ID, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		rows := firstPage.Records
		pending := 0
		for _, r := range rows {
			if r.Status == "pending" {
				pending++
			}
		}
		if pending != 100 || firstPage.PendingCount != 103 || !firstPage.HasMore {
			t.Fatalf("pending page/count: %d %+v", pending, firstPage)
		}
		last := firstPage.Records[99]
		next, _, err := s.ListTeamAssetDeletionRequests(ctx, team.ID, admin.ID, &last.CreatedAt, last.ID)
		if err != nil || len(next.Records) != 3 || next.PendingCount != 103 || next.HasMore {
			t.Fatalf("next page: %+v %v", next, err)
		}
		seen := map[string]bool{}
		for _, r := range append(firstPage.Records[:100], next.Records...) {
			if seen[r.ID] {
				t.Fatal("duplicate pending across pages")
			}
			seen[r.ID] = true
		}
		if _, err := s.ReviewTeamAssetDeletion(ctx, team.ID, admin.ID, first.ID, "reject", now); err != nil {
			t.Fatal(err)
		}
		for i := range 102 {
			r, err := s.RequestTeamAssetDeletion(ctx, team.ID, admin.ID, first.AssetID, uuid.NewString(), now.Add(time.Duration(i+1)*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReviewTeamAssetDeletion(ctx, team.ID, admin.ID, r.ID, "reject", now.Add(time.Duration(i+1)*time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
		// 旧申请刚完成，应按审批时间进入历史首位，而非被申请创建时间挤掉。
		var oldestPending TeamAssetDeletionRecord
		rows, _, _ = listTeamAssetDeletions(s, ctx, team.ID, admin.ID)
		for _, r := range rows {
			if r.Status == "pending" {
				oldestPending = r
				break
			}
		}
		if _, err := s.ReviewTeamAssetDeletion(ctx, team.ID, admin.ID, oldestPending.ID, "reject", now.Add(200*time.Hour)); err != nil {
			t.Fatal(err)
		}
		rows, _, err = listTeamAssetDeletions(s, ctx, team.ID, admin.ID)
		if err != nil {
			t.Fatal(err)
		}
		terminal := 0
		for _, r := range rows {
			if r.Status != "pending" {
				terminal++
				if terminal == 1 && r.ID != oldestPending.ID {
					t.Fatal("history sorted by request time")
				}
			}
		}
		if terminal != 100 {
			t.Fatalf("terminal history count %d", terminal)
		}
	})
}
