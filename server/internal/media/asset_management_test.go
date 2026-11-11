package media

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestAssetLibraryPermissionsAndCAS(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if err := f.s.Repo.RemoveAsset(ctx, f.team, "a"); err != nil {
		t.Fatal(err)
	}
	owner := AssetActor{UserID: f.user, Name: "Uploader"}
	admin := AssetActor{UserID: "admin", Name: "Admin", Admin: true}
	if err := f.s.Repo.addOwnedAsset(ctx, f.team, Asset{ID: "owned", Name: "Original", Kind: "image", CreatedAt: time.Now()}, owner); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Repo.AddAsset(ctx, f.team, Asset{ID: "legacy", Name: "Legacy", Kind: "image"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		team, id string
		actor    AssetActor
		want     error
	}{
		{f.team, "legacy", admin, ErrAssetForbidden}, {f.team, "owned", AssetActor{UserID: "other"}, ErrAssetForbidden}, {"other-team", "owned", owner, ErrAssetNotFound},
	} {
		if _, err := f.s.Repo.RenameAsset(ctx, tc.team, tc.id, "Changed", tc.actor); !errors.Is(err, tc.want) {
			t.Fatalf("rename: %v", err)
		}
	}
	if _, err := f.s.Repo.RenameAsset(ctx, f.team, "owned", "a/b", owner); !errors.Is(err, ErrAssetNameInvalid) {
		t.Fatal(err)
	}
	if _, err := f.s.Repo.RenameAsset(ctx, f.team, "owned", " Renamed ", admin); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			request, err := f.s.Repo.RequestAssetDeletion(ctx, f.team, "owned", owner)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- request.ID
		})
	}
	wg.Wait()
	close(ids)
	requestID := ""
	for id := range ids {
		if requestID != "" && id != requestID {
			t.Fatal("duplicate pending request")
		}
		requestID = id
	}
	if _, err := f.s.Repo.ReviewAssetDeletion(ctx, f.team, requestID, "approve", owner); !errors.Is(err, ErrAssetForbidden) {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := f.s.Repo.ReviewAssetDeletion(ctx, f.team, requestID, "approve", admin); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.Repo.ReviewAssetDeletion(ctx, f.team, requestID, "reject", admin); !errors.Is(err, ErrAssetConflict) {
		t.Fatal(err)
	}
	// 同 ID 再写入也不能复活墓碑，历史签名仍可读取保留的媒体记录。
	if err := f.s.Repo.AddAsset(ctx, f.team, Asset{ID: "owned", Name: "Resurrect"}); err != nil {
		t.Fatal(err)
	}
	assets, library, err := f.s.Repo.MediaLibrary(ctx, f.team, admin)
	if err != nil || len(assets) != 1 || assets[0].ID != "legacy" || len(library.DeletionRequests) != 1 {
		t.Fatal(assets, library, err)
	}
	restored := Repository{Store: f.s.Repo.Store}
	records, err := restored.AssetsForPlayback(ctx, f.team)
	if err != nil || len(records) != 2 {
		t.Fatal(records, err)
	}
}

func TestAssetDeletionBlocksActiveReferencesAndAllowsRejection(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if err := f.s.Repo.RemoveAsset(ctx, f.team, "a"); err != nil {
		t.Fatal(err)
	}
	owner := AssetActor{UserID: f.user, Name: "Uploader"}
	admin := AssetActor{UserID: "admin", Admin: true}
	if err := f.s.Repo.addOwnedAsset(ctx, f.team, Asset{ID: "source", Name: "Source", Kind: "video"}, owner); err != nil {
		t.Fatal(err)
	}
	p := &Project{ID: "active", TeamID: f.team, Status: "running", Canvas: &CanvasRequest{VideoReferences: []CanvasVideoReference{{AssetID: "source"}}}}
	if err := f.s.Repo.Save(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Repo.Index(ctx, ProjectKey(f.team, p.ID)); err != nil {
		t.Fatal(err)
	}
	req, err := f.s.Repo.RequestAssetDeletion(ctx, f.team, "source", owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Repo.ReviewAssetDeletion(ctx, f.team, req.ID, "approve", admin); !errors.Is(err, ErrAssetInUse) {
		t.Fatal(err)
	}
	if _, err = f.s.Repo.ReviewAssetDeletion(ctx, f.team, req.ID, "reject", admin); err != nil {
		t.Fatal(err)
	}
	next, err := f.s.Repo.RequestAssetDeletion(ctx, f.team, "source", owner)
	if err != nil || next.ID == req.ID {
		t.Fatal(next, err)
	}
	previous := *p
	p.Status = "completed"
	if err = f.s.Repo.Save(ctx, p, &previous); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Repo.ReviewAssetDeletion(ctx, f.team, next.ID, "approve", admin); err != nil {
		t.Fatal(err)
	}
	assets, err := f.s.Repo.Assets(ctx, f.team)
	if err != nil || len(assets) != 0 {
		t.Fatal(assets, err)
	}
}
