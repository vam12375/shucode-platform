package teamassets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func newTestService(t *testing.T) Service {
	t.Helper()
	return Service{
		Repository: newTestRepository(t), Blobs: media.Blobs{Root: t.TempDir()},
		Now: func() time.Time { return time.Date(2026, 10, 21, 1, 2, 3, 0, time.UTC) },
	}
}

func blobCount(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func TestServiceUploadPreservesBytesAndUsesContentMIME(t *testing.T) {
	for _, tt := range []struct {
		name, kind, mime string
		body             []byte
	}{
		{"分析.md", "document", "text/plain", []byte("# 分析\n\n团队资产\n")},
		{"picture.png", "image", "image/png", append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 24)...)},
		{"movie.mp4", "video", "video/mp4", []byte("\x00\x00\x00\x18ftypisom\x00\x00\x00\x00isommp42")},
		{"renamed.png", "document", "text/plain", []byte("This is a plain text document, despite its extension.")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(t)
			ctx := context.Background()
			team, id := createTestTeam(t, svc.Repository), uuid.NewString()
			user := uuid.NewString()
			asset, err := svc.Upload(ctx, team, user, "Member", id, tt.name, bytes.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			if asset.ID != id || asset.Name != tt.name || asset.Kind != tt.kind || !strings.HasPrefix(asset.Mime, tt.mime) || asset.Size != int64(len(tt.body)) || asset.UploadedBy.ID != user {
				t.Fatalf("metadata: %+v", asset)
			}
			opened, file, err := svc.Open(ctx, team, id)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			body, err := io.ReadAll(file)
			if err != nil || !bytes.Equal(body, tt.body) || opened.ID != id {
				t.Fatalf("raw content changed: %q %v", body, err)
			}
			if _, _, err = svc.Open(ctx, createTestTeam(t, svc.Repository), id); !errors.Is(err, ErrNotFound) {
				t.Fatalf("cross-team open: %v", err)
			}
			if blobCount(t, svc.Blobs.Root) != 1 {
				t.Fatal("one upload must keep one private blob")
			}
		})
	}
}

func TestServiceIdempotentUploadAndRejectionsCleanUnusedBlobs(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	team, id := createTestTeam(t, svc.Repository), uuid.NewString()
	user, otherUser := uuid.NewString(), uuid.NewString()
	body := []byte("# analysis\n")
	first, err := svc.Upload(ctx, team, user, "First Name", id, "analysis.md", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.Upload(ctx, team, user, "New Display Name", id, "analysis.md", bytes.NewReader(body))
	if err != nil || again != first {
		t.Fatalf("idempotent upload must retain the first metadata: %+v %v", again, err)
	}
	if _, err = svc.Upload(ctx, team, user, "Member", id, "analysis.md", strings.NewReader("different")); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed content: %v", err)
	}
	if _, err = svc.Upload(ctx, team, otherUser, "Member", id, "analysis.md", bytes.NewReader(body)); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed uploader: %v", err)
	}
	if blobCount(t, svc.Blobs.Root) != 1 {
		t.Fatal("retries and rejected uploads left unreferenced blobs")
	}
	fillRepository(t, svc.Repository, team, TeamStorageLimitBytes-int64(len(body)))
	if _, err = svc.Upload(ctx, team, user, "Member", uuid.NewString(), "extra.md", strings.NewReader("x")); !errors.Is(err, ErrStorageLimit) {
		t.Fatalf("full storage: %v", err)
	}
	if blobCount(t, svc.Blobs.Root) != 1 {
		t.Fatal("quota rejection left an unreferenced blob")
	}
	if again, err = svc.Upload(ctx, team, user, "Member", id, "analysis.md", bytes.NewReader(body)); err != nil || again != first {
		t.Fatalf("accepted retry at capacity: %+v %v", again, err)
	}
	if blobCount(t, svc.Blobs.Root) != 1 {
		t.Fatal("full-team retry leaked a blob")
	}
}

func TestServiceListCursorIsStableAndUsageCoversAllPages(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	team := createTestTeam(t, svc.Repository)
	const count = 151
	for i := range count {
		record := testRecord(1)
		record.CreatedAt = record.CreatedAt.Add(time.Duration(i) * time.Second)
		if _, err := svc.Repository.Commit(ctx, team, record); err != nil {
			t.Fatal(err)
		}
	}
	page, err := svc.List(ctx, team, "")
	if err != nil || len(page.Assets) != PageSize || page.NextCursor == nil {
		t.Fatalf("first page: %d %+v %v", len(page.Assets), page.NextCursor, err)
	}
	if page.Storage.UsedBytes != count || page.Storage.LimitBytes != TeamStorageLimitBytes {
		t.Fatalf("usage must cover all committed assets: %+v", page.Storage)
	}
	seen := map[string]bool{}
	for i, asset := range page.Assets {
		seen[asset.ID] = true
		if i > 0 && !page.Assets[i-1].CreatedAt.After(asset.CreatedAt) {
			t.Fatal("first page is not newest first")
		}
	}
	newer := testRecord(7)
	newer.CreatedAt = newer.CreatedAt.Add(time.Hour)
	if _, err := svc.Repository.Commit(ctx, team, newer); err != nil {
		t.Fatal(err)
	}
	next, err := svc.List(ctx, team, *page.NextCursor)
	if err != nil || len(next.Assets) != count-PageSize || next.NextCursor != nil {
		t.Fatalf("second page after new upload: %d %+v %v", len(next.Assets), next.NextCursor, err)
	}
	for _, asset := range next.Assets {
		if seen[asset.ID] || asset.ID == newer.ID {
			t.Fatalf("new upload shifted an existing cursor: %s", asset.ID)
		}
		seen[asset.ID] = true
	}
	if len(seen) != count || next.Storage.UsedBytes != count+7 {
		t.Fatalf("pagination lost assets or usage: %d %+v", len(seen), next.Storage)
	}
	if _, err := svc.List(ctx, uuid.NewString(), *page.NextCursor); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-team cursor: %v", err)
	}
	for _, cursor := range []string{"not-a-cursor", strings.Repeat("a", 4097)} {
		if _, err := svc.List(ctx, team, cursor); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid cursor accepted: %v", err)
		}
	}
}

func TestServiceCursorUsesIDWhenCreationTimesTie(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	team := createTestTeam(t, svc.Repository)
	for i := 1; i <= PageSize+1; i++ {
		record := testRecord(1)
		record.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		if _, err := svc.Repository.Commit(ctx, team, record); err != nil {
			t.Fatal(err)
		}
	}
	first, err := svc.List(ctx, team, "")
	if err != nil || first.NextCursor == nil || len(first.Assets) != PageSize {
		t.Fatalf("first page: %+v %v", first, err)
	}
	if first.Assets[0].ID != "00000000-0000-4000-8000-000000000101" || first.Assets[PageSize-1].ID != "00000000-0000-4000-8000-000000000002" {
		t.Fatalf("tied timestamps do not use descending IDs: %+v", first.Assets)
	}
	second, err := svc.List(ctx, team, *first.NextCursor)
	if err != nil || len(second.Assets) != 1 || second.Assets[0].ID != "00000000-0000-4000-8000-000000000001" || second.NextCursor != nil {
		t.Fatalf("cursor lost tied-time asset: %+v %v", second, err)
	}
}

func TestServiceConcurrentRetryRetainsOneBlobAndOneUsage(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	team, id := createTestTeam(t, svc.Repository), uuid.NewString()
	user := uuid.NewString()
	const count = 8
	const content = "concurrent upload content"
	start := make(chan struct{})
	type result struct {
		asset Asset
		err   error
	}
	results := make(chan result, count)
	for range count {
		go func() {
			<-start
			asset, err := svc.Upload(ctx, team, user, "Member", id, "shared.md", strings.NewReader(content))
			results <- result{asset, err}
		}()
	}
	close(start)
	var first Asset
	for range count {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if first.ID == "" {
			first = got.asset
		}
		if got.asset != first {
			t.Fatalf("retry returned different metadata: %+v / %+v", got.asset, first)
		}
	}
	page, err := svc.List(ctx, team, "")
	if err != nil || len(page.Assets) != 1 || page.Storage.UsedBytes != int64(len(content)) {
		t.Fatalf("duplicate concurrent usage: %+v %v", page, err)
	}
	if got := blobCount(t, svc.Blobs.Root); got != 1 {
		t.Fatalf("concurrent retries retained %d blobs", got)
	}
}

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestServiceIncompleteReaderAndEmptyUploadLeaveNoBlobOrRecord(t *testing.T) {
	for _, tt := range []struct {
		name   string
		reader io.Reader
	}{
		{"empty", strings.NewReader("")},
		{"reader failure", io.MultiReader(strings.NewReader("partial data"), failedReader{})},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(t)
			team := createTestTeam(t, svc.Repository)
			if _, err := svc.Upload(context.Background(), team, uuid.NewString(), "Member", uuid.NewString(), "analysis.md", tt.reader); err == nil {
				t.Fatal("incomplete/empty input must fail")
			}
			records, err := svc.Repository.Records(context.Background(), team)
			if err != nil || len(records) != 0 || blobCount(t, svc.Blobs.Root) != 0 {
				t.Fatalf("failed input left state: %+v %v", records, err)
			}
		})
	}
}

func TestServiceRejectsInvalidInputBeforeWritingBlob(t *testing.T) {
	for _, tt := range []struct{ id, name string }{
		{"invalid", "valid.md"},
		{uuid.NewString(), ""},
		{uuid.NewString(), "../secret.md"},
		{uuid.NewString(), "folder\\secret.md"},
		{uuid.NewString(), "line\nbreak.md"},
		{uuid.NewString(), strings.Repeat("字", 256)},
	} {
		t.Run(fmt.Sprintf("%s/%s", tt.id, tt.name), func(t *testing.T) {
			svc := newTestService(t)
			if _, err := svc.Upload(context.Background(), uuid.NewString(), "user", "Member", tt.id, tt.name, strings.NewReader("data")); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid input: %v", err)
			}
			if blobCount(t, svc.Blobs.Root) != 0 {
				t.Fatal("invalid metadata wrote a blob")
			}
		})
	}
}

var errTestStorage = errors.New("injected storage failure")

type failedCommitStore struct {
	store.PlatformStore
	commitFirst bool
	unreadable  bool
	attempted   bool
}

func (s *failedCommitStore) CommitTeamAsset(ctx context.Context, teamID string, record store.TeamAssetRecord, limitBytes int64) (store.TeamAssetRecord, bool, error) {
	s.attempted = true
	if s.commitFirst {
		if saved, created, err := s.PlatformStore.CommitTeamAsset(ctx, teamID, record, limitBytes); err != nil {
			return store.TeamAssetRecord{}, false, err
		} else if !created {
			return saved, false, errTestStorage
		}
	}
	return store.TeamAssetRecord{}, false, errTestStorage
}

func (s *failedCommitStore) GetTeamAsset(ctx context.Context, teamID, id string) (*store.TeamAssetRecord, error) {
	if s.attempted && s.unreadable {
		return nil, errTestStorage
	}
	return s.PlatformStore.GetTeamAsset(ctx, teamID, id)
}

func TestServiceCommitFailureCleansOnlyConfirmedUnreferencedBlobs(t *testing.T) {
	for _, tt := range []struct {
		name                    string
		commitFirst, unreadable bool
		wantBlobs               int
	}{
		{"confirmed rejection", false, false, 0},
		{"committed but response lost", true, false, 1},
		{"commit result cannot be verified", false, true, 1},
		{"committed and verification unavailable", true, true, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(t)
			underlying := svc.Repository.Store
			failing := &failedCommitStore{PlatformStore: underlying, commitFirst: tt.commitFirst, unreadable: tt.unreadable}
			svc.Repository.Store = failing
			team, id := createTestTeam(t, svc.Repository), uuid.NewString()
			_, err := svc.Upload(context.Background(), team, uuid.NewString(), "Member", id, "analysis.md", strings.NewReader("original bytes"))
			if err == nil && !tt.commitFirst {
				t.Fatal("failed commit must not report success")
			}
			if got := blobCount(t, svc.Blobs.Root); got != tt.wantBlobs {
				t.Fatalf("blob count after uncertain commit: got %d, want %d", got, tt.wantBlobs)
			}
			if tt.commitFirst {
				svc.Repository.Store = underlying
				asset, file, err := svc.Open(context.Background(), team, id)
				if err != nil {
					t.Fatalf("committed blob was deleted: %v", err)
				}
				defer file.Close()
				content, err := io.ReadAll(file)
				if err != nil || string(content) != "original bytes" || asset.ID != id {
					t.Fatalf("committed content unavailable: %q %v", content, err)
				}
			}
		})
	}
}
