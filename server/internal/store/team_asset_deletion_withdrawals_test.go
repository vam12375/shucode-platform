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

func TestMemoryTeamAssetDeletionWithdrawalsConformance(t *testing.T) {
	s, closeStore := newMemoryForTest(t)
	defer closeStore()
	runTeamAssetDeletionWithdrawalsConformance(t, s)
}

func TestPostgresTeamAssetDeletionWithdrawalsConformance(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 TEST_DATABASE_URL,跳过 Postgres 撤回 conformance")
	}
	s, closeStore := newPostgresForTest(t, url, os.Getenv("TEST_MIGRATIONS_DIR"))
	defer closeStore()
	runTeamAssetDeletionWithdrawalsConformance(t, s)
}

func runTeamAssetDeletionWithdrawalsConformance(t *testing.T, s PlatformStore) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "asset-withdraw-" + uuid.NewString(), QuotaCredits: "0"})
	if err != nil {
		t.Fatal(err)
	}
	admin := mustUser(t, ctx, s, uuid.NewString()+"@withdraw.test")
	author := mustUser(t, ctx, s, uuid.NewString()+"@withdraw.test")
	other := mustUser(t, ctx, s, uuid.NewString()+"@withdraw.test")
	for id, role := range map[string]TeamMemberRole{admin.ID: MemberRoleOwner, author.ID: MemberRoleMember, other.ID: MemberRoleMember} {
		if err := s.AddMember(ctx, team.ID, id, role); err != nil {
			t.Fatal(err)
		}
	}
	newRequest := func(index int, at time.Time) (TeamAssetRecord, TeamAssetDeletionRecord) {
		t.Helper()
		asset := testTeamAsset(author.ID, 10000+index, at, 7)
		if _, _, err := s.CommitTeamAsset(ctx, team.ID, asset, 10000); err != nil {
			t.Fatal(err)
		}
		r, err := s.RequestTeamAssetDeletion(ctx, team.ID, author.ID, asset.ID, uuid.NewString(), at)
		if err != nil {
			t.Fatal(err)
		}
		return asset, r
	}
	asset, request := newRequest(0, now)
	for _, actor := range []string{admin.ID, other.ID} {
		if _, err := s.WithdrawTeamAssetDeletion(ctx, team.ID, actor, request.ID, now); !errors.Is(err, ErrTeamAssetForbidden) {
			t.Fatalf("other actor withdrew request: %v", err)
		}
	}
	if _, err := s.WithdrawTeamAssetDeletion(ctx, team.ID, author.ID, uuid.NewString(), now); !errors.Is(err, ErrTeamAssetNotFound) {
		t.Fatalf("missing/cross-team request: %v", err)
	}
	if _, err := s.WithdrawTeamAssetDeletion(ctx, team.ID, author.ID, "invalid", now); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid request: %v", err)
	}
	at := now.Add(3 * time.Hour)
	withdrawn, err := s.WithdrawTeamAssetDeletion(ctx, team.ID, author.ID, request.ID, at)
	if err != nil || withdrawn.Status != "withdrawn" || withdrawn.WithdrawnAt == nil || !withdrawn.WithdrawnAt.Equal(at) || withdrawn.ReviewedAt != nil || withdrawn.ReviewedByID != "" {
		t.Fatalf("withdrawn fact: %+v %v", withdrawn, err)
	}
	*withdrawn.WithdrawnAt = time.Time{}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.WithdrawTeamAssetDeletion(ctx, team.ID, author.ID, request.ID, at.Add(time.Hour))
			if err != nil || r.WithdrawnAt == nil || !r.WithdrawnAt.Equal(at) {
				t.Errorf("idempotent withdrawal changed fact: %+v %v", r, err)
			}
		}()
	}
	wg.Wait()
	for _, actor := range []string{admin.ID, other.ID} {
		if _, err := s.WithdrawTeamAssetDeletion(ctx, team.ID, actor, request.ID, now); !errors.Is(err, ErrTeamAssetForbidden) {
			t.Fatalf("idempotency bypassed ownership: %v", err)
		}
	}
	page, err := s.ListTeamAssetPage(ctx, team.ID, nil, "", 100)
	if err != nil || len(page.Records) != 1 || page.UsedBytes != asset.Size {
		t.Fatalf("withdraw changed metadata/quota: %+v %v", page, err)
	}
	cleanups, err := s.ListTeamAssetBlobCleanups(ctx, team.ID)
	if err != nil || len(cleanups) != 0 {
		t.Fatalf("withdraw scheduled blob cleanup: %+v %v", cleanups, err)
	}
	if err := s.MarkTeamAssetDeletionBlobCleaned(ctx, team.ID, request.ID, now); !errors.Is(err, ErrTeamAssetConflict) {
		t.Fatalf("withdrawn blob marked cleaned: %v", err)
	}
	for _, actor := range []string{author.ID, admin.ID} {
		p, _, err := s.ListTeamAssetDeletionRequests(ctx, team.ID, actor, nil, "")
		if err != nil || p.PendingCount != 0 || len(p.Records) != 1 || p.Records[0].Status != "withdrawn" {
			t.Fatalf("withdraw history/count: %+v %v", p, err)
		}
	}
	for _, decision := range []string{"approve", "reject"} {
		if _, err := s.ReviewTeamAssetDeletion(ctx, team.ID, admin.ID, request.ID, decision, now); !errors.Is(err, ErrTeamAssetConflict) {
			t.Fatalf("withdrawn request reviewed: %v", err)
		}
	}
	reapplied, err := s.RequestTeamAssetDeletion(ctx, team.ID, author.ID, asset.ID, uuid.NewString(), now.Add(time.Hour))
	if err != nil || reapplied.ID == request.ID || reapplied.Status != "pending" {
		t.Fatalf("reapply after withdrawal: %+v %v", reapplied, err)
	}
	if _, err := s.ReviewTeamAssetDeletion(ctx, team.ID, admin.ID, reapplied.ID, "reject", now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WithdrawTeamAssetDeletion(ctx, team.ID, author.ID, reapplied.ID, now); !errors.Is(err, ErrTeamAssetConflict) {
		t.Fatalf("rejected request withdrawn: %v", err)
	}
	history, _, err := s.ListTeamAssetDeletionRequests(ctx, team.ID, admin.ID, nil, "")
	if err != nil || history.Records[0].ID != request.ID {
		t.Fatalf("mixed history not sorted by completion time: %+v %v", history, err)
	}
	_, approvedRequest := newRequest(1, now)
	if _, err := s.ReviewTeamAssetDeletion(ctx, team.ID, admin.ID, approvedRequest.ID, "approve", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WithdrawTeamAssetDeletion(ctx, team.ID, author.ID, approvedRequest.ID, now); !errors.Is(err, ErrTeamAssetConflict) {
		t.Fatalf("approved request withdrawn: %v", err)
	}
	for decisionIndex, decision := range []string{"approve", "reject"} {
		for i := range 8 {
			asset, request := newRequest(2+i+8*decisionIndex, now)
			before, err := s.ListTeamAssetPage(ctx, team.ID, nil, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, err := s.WithdrawTeamAssetDeletion(ctx, team.ID, author.ID, request.ID, now)
				results <- err
			}()
			go func() {
				defer wg.Done()
				<-start
				_, err := s.ReviewTeamAssetDeletion(ctx, team.ID, admin.ID, request.ID, decision, now)
				results <- err
			}()
			close(start)
			wg.Wait()
			a, b := <-results, <-results
			if (a == nil) == (b == nil) || (a != nil && !errors.Is(a, ErrTeamAssetConflict)) || (b != nil && !errors.Is(b, ErrTeamAssetConflict)) {
				t.Fatalf("race admitted multiple terminal outcomes: %v %v", a, b)
			}
			rows, _, err := s.ListTeamAssetDeletionRequests(ctx, team.ID, admin.ID, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			var terminal TeamAssetDeletionRecord
			for _, r := range rows.Records {
				if r.ID == request.ID {
					terminal = r
				}
			}
			after, err := s.ListTeamAssetPage(ctx, team.ID, nil, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			expected := before.UsedBytes
			if terminal.Status == "approved" {
				expected -= asset.Size
			}
			got, err := s.GetTeamAsset(ctx, team.ID, asset.ID)
			if err != nil || (got == nil) != (terminal.Status == "approved") || after.UsedBytes != expected {
				t.Fatalf("race fact/quota mismatch: status=%s used=%d want=%d asset=%+v err=%v", terminal.Status, after.UsedBytes, expected, got, err)
			}
			if _, err := s.WithdrawTeamAssetDeletion(ctx, team.ID, author.ID, request.ID, now); terminal.Status == "withdrawn" && err != nil || terminal.Status != "withdrawn" && !errors.Is(err, ErrTeamAssetConflict) {
				t.Fatalf("terminal withdrawal retry: %s %v", terminal.Status, err)
			}
		}
	}
	disabled := now
	if _, err := s.UpdateTeamMember(ctx, team.ID, author.ID, TeamMemberPatch{HasDisabled: true, DisabledAt: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WithdrawTeamAssetDeletion(ctx, team.ID, author.ID, request.ID, now); !errors.Is(err, ErrTeamAssetForbidden) {
		t.Fatalf("disabled author idempotency: %v", err)
	}
}
