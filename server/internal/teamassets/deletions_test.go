package teamassets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestTeamAssetDeletionCleanupFailureIsDurableAndRetriesOnList(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	team := createTestTeam(t, svc.Repository)
	user, err := svc.Repository.Store.CreateUser(ctx, store.UserInput{Email: uuid.NewString() + "@deletion.test", DisplayName: "Reviewer", PasswordHash: "x", Role: store.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Repository.Store.AddMember(ctx, team, user.ID, store.MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	asset, err := svc.Upload(ctx, team, user.ID, user.DisplayName, uuid.NewString(), "cleanup.md", strings.NewReader("cleanup"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := svc.Repository.Get(ctx, team, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	request, err := svc.RequestDeletion(ctx, team, user.ID, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	blobPath := svc.Blobs.Path(team, record.BlobID)
	// 非空目录模拟无法删除的持久卷对象，审批事实仍必须成功且保留清理任务。
	if err := os.Remove(blobPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(blobPath, 0700); err != nil {
		t.Fatal(err)
	}
	obstruction := filepath.Join(blobPath, "blocked")
	if err := os.WriteFile(obstruction, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	approved, err := svc.ReviewDeletion(ctx, team, user.ID, request.ID, "approve")
	if err != nil || approved.Status != "approved" {
		t.Fatalf("approval should survive cleanup failure: %+v %v", approved, err)
	}
	if _, _, err := svc.Open(ctx, team, asset.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approved content remains visible: %v", err)
	}
	cleanup, err := svc.Repository.Store.ListTeamAssetBlobCleanups(ctx, team)
	if err != nil || len(cleanup) != 1 {
		t.Fatalf("cleanup intent lost: %+v %v", cleanup, err)
	}
	if err := os.Remove(obstruction); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListDeletionRequests(ctx, team, user.ID, ""); err != nil {
		t.Fatal(err)
	}
	cleanup, err = svc.Repository.Store.ListTeamAssetBlobCleanups(ctx, team)
	if err != nil || len(cleanup) != 0 {
		t.Fatalf("cleanup did not retry: %+v %v", cleanup, err)
	}
	if _, err := os.Stat(blobPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blob remains: %v", err)
	}
	if _, err := svc.ReviewDeletion(ctx, team, user.ID, request.ID, "approve"); err != nil {
		t.Fatal("cleaned approval retry must be idempotent", err)
	}
}

type uncertainDeletionStore struct{ store.PlatformStore }

func (s uncertainDeletionStore) ReviewTeamAssetDeletion(ctx context.Context, team, actor, id, decision string, at time.Time) (store.TeamAssetDeletionRecord, error) {
	_, err := s.PlatformStore.ReviewTeamAssetDeletion(ctx, team, actor, id, decision, at)
	if err != nil {
		return store.TeamAssetDeletionRecord{}, err
	}
	return store.TeamAssetDeletionRecord{}, errors.New("synthetic commit response lost")
}

func TestTeamAssetDeletionUncertainCommitDoesNotCleanUntilConfirmed(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	team := createTestTeam(t, svc.Repository)
	user, err := svc.Repository.Store.CreateUser(ctx, store.UserInput{Email: uuid.NewString() + "@deletion.test", DisplayName: "Reviewer", PasswordHash: "x", Role: store.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Repository.Store.AddMember(ctx, team, user.ID, store.MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	asset, err := svc.Upload(ctx, team, user.ID, user.DisplayName, uuid.NewString(), "uncertain.md", strings.NewReader("uncertain"))
	if err != nil {
		t.Fatal(err)
	}
	request, err := svc.RequestDeletion(ctx, team, user.ID, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	svc.Repository.Store = uncertainDeletionStore{svc.Repository.Store}
	if _, err := svc.ReviewDeletion(ctx, team, user.ID, request.ID, "approve"); err == nil {
		t.Fatal("expected uncertain commit")
	}
	if count := blobCount(t, svc.Blobs.Root); count != 1 {
		t.Fatal("uncertain commit removed bytes")
	}
	if _, err := svc.ListDeletionRequests(ctx, team, user.ID, ""); err != nil {
		t.Fatal(err)
	}
	if count := blobCount(t, svc.Blobs.Root); count != 0 {
		t.Fatal("confirmed approved fact did not clean")
	}
}
