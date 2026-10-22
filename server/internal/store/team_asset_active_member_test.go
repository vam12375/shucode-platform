package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMemoryTeamAssetActiveMemberCommit(t *testing.T) {
	s, closeStore := newMemoryForTest(t)
	defer closeStore()
	runTeamAssetActiveMemberCommit(t, s)
}

func TestPostgresTeamAssetActiveMemberCommit(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 TEST_DATABASE_URL,跳过后台归档成员提交验证")
	}
	s, closeStore := newPostgresForTest(t, url, os.Getenv("TEST_MIGRATIONS_DIR"))
	defer closeStore()
	runTeamAssetActiveMemberCommit(t, s)
}

func runTeamAssetActiveMemberCommit(t *testing.T, s PlatformStore) {
	ctx := context.Background()
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "active-assets-" + uuid.NewString(), QuotaCredits: "0"})
	if err != nil {
		t.Fatal(err)
	}
	user := mustUser(t, ctx, s, uuid.NewString()+"@active-assets.test")
	at := time.Now().UTC()
	record := testTeamAsset(user.ID, 20000, at, 7)
	if _, _, err := s.CommitTeamAssetForActiveMember(ctx, team.ID, record, 7); !errors.Is(err, ErrTeamAssetForbidden) {
		t.Fatalf("non-member committed: %v", err)
	}
	if err := s.AddMember(ctx, team.ID, user.ID, MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	admin := mustUser(t, ctx, s, uuid.NewString()+"@active-assets.test")
	if err := s.AddMember(ctx, team.ID, admin.ID, MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"member", "user", "team"} {
		setDisabled := func(disabled *time.Time) {
			t.Helper()
			var err error
			switch scope {
			case "member":
				_, err = s.UpdateTeamMember(ctx, team.ID, user.ID, TeamMemberPatch{HasDisabled: true, DisabledAt: disabled})
			case "user":
				_, err = s.UpdateUser(ctx, user.ID, UpdateUserPatch{HasDisabled: true, DisabledAt: disabled})
			case "team":
				_, err = s.UpdateTeam(ctx, team.ID, UpdateTeamPatch{HasDisabled: true, DisabledAt: disabled})
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		setDisabled(&at)
		if _, _, err := s.CommitTeamAssetForActiveMember(ctx, team.ID, record, 7); !errors.Is(err, ErrTeamAssetForbidden) {
			t.Fatalf("disabled %s committed: %v", scope, err)
		}
		setDisabled(nil)
	}
	name := "原生成者"
	if _, err := s.UpdateUser(ctx, user.ID, UpdateUserPatch{DisplayName: &name}); err != nil {
		t.Fatal(err)
	}
	saved, created, err := s.CommitTeamAssetForActiveMember(ctx, team.ID, record, 7)
	if err != nil || !created || saved.UploadedByID != user.ID || saved.UploadedByName != name {
		t.Fatalf("fresh owner snapshot: %+v %v %v", saved, created, err)
	}
	record.BlobID = uuid.NewString()
	if _, created, err := s.CommitTeamAssetForActiveMember(ctx, team.ID, record, 7); err != nil || created {
		t.Fatalf("quota-full idempotency: %v %v", created, err)
	}
	deleted, err := s.IsTeamAssetDeleted(ctx, team.ID, record.ID)
	if err != nil || deleted {
		t.Fatalf("live asset tombstone: %v %v", deleted, err)
	}
	r, err := s.RequestTeamAssetDeletion(ctx, team.ID, user.ID, record.ID, uuid.NewString(), at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewTeamAssetDeletion(ctx, team.ID, user.ID, r.ID, "approve", at); err != nil {
		t.Fatal(err)
	}
	if deleted, err := s.IsTeamAssetDeleted(ctx, team.ID, record.ID); err != nil || !deleted {
		t.Fatalf("approved tombstone missing: %v %v", deleted, err)
	}
	if _, _, err := s.CommitTeamAssetForActiveMember(ctx, team.ID, record, 7); !errors.Is(err, ErrTeamAssetConflict) {
		t.Fatalf("background archive resurrected approved deletion: %v", err)
	}
}
