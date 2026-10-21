package teamassets

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func newTestRepository(t *testing.T) Repository {
	t.Helper()
	st, err := store.NewMemoryStore(context.Background(), store.MemoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Dispose(context.Background()) })
	return Repository{Store: st}
}

func createTestTeam(t *testing.T, repo Repository) string {
	t.Helper()
	team, err := repo.Store.CreateTeam(context.Background(), store.CreateTeamInput{
		Name: "team-assets-" + uuid.NewString(), QuotaCredits: "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	return team.ID
}

func testRecord(size int64) Record {
	return Record{
		Asset: Asset{
			ID: uuid.NewString(), Name: "asset.bin", Kind: "other", Mime: "application/octet-stream", Size: size,
			CreatedAt: time.Date(2026, 10, 21, 1, 2, 3, 0, time.UTC), UploadedBy: Uploader{ID: uuid.NewString(), Name: "Uploader"},
		},
		SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BlobID: uuid.NewString(),
	}
}

func fillRepository(t *testing.T, repo Repository, team string, total int64) []Record {
	t.Helper()
	var records []Record
	for total > 0 {
		size := min(total, MaxAssetBytes)
		record := testRecord(size)
		committed, err := repo.Commit(context.Background(), team, record)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, committed)
		total -= size
	}
	return records
}

func TestRepositoryStorageLimitAndIdempotency(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	team := createTestTeam(t, repo)
	fillRepository(t, repo, team, TeamStorageLimitBytes-1)
	last := testRecord(1)
	committed, err := repo.Commit(ctx, team, last)
	if err != nil {
		t.Fatalf("exactly full must succeed: %v", err)
	}
	if _, err = repo.Commit(ctx, team, testRecord(1)); !errors.Is(err, ErrStorageLimit) {
		t.Fatalf("one byte beyond capacity: %v", err)
	}
	retry := last
	retry.BlobID = uuid.NewString()
	retry.CreatedAt = retry.CreatedAt.Add(time.Hour)
	again, err := repo.Commit(ctx, team, retry)
	if err != nil || again.BlobID != committed.BlobID || !again.CreatedAt.Equal(committed.CreatedAt) {
		t.Fatalf("full-team retry must return first commit: %+v %v", again, err)
	}
	for _, change := range []struct {
		name string
		edit func(*Record)
	}{
		{"content", func(r *Record) { r.SHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" }},
		{"name", func(r *Record) { r.Name = "changed.bin" }},
		{"uploader", func(r *Record) { r.UploadedBy.ID = uuid.NewString() }},
	} {
		t.Run(change.name, func(t *testing.T) {
			changed := last
			change.edit(&changed)
			if _, err := repo.Commit(ctx, team, changed); !errors.Is(err, ErrConflict) {
				t.Fatalf("same ID with different %s must conflict before quota: %v", change.name, err)
			}
		})
	}
	records, err := repo.Records(ctx, team)
	if err != nil {
		t.Fatal(err)
	}
	var used int64
	for _, record := range records {
		used += record.Size
	}
	if used != TeamStorageLimitBytes {
		t.Fatalf("usage changed after rejects/retries: %d", used)
	}
	otherTeam := createTestTeam(t, repo)
	if other, err := repo.Records(ctx, otherTeam); err != nil || len(other) != 0 {
		t.Fatalf("team isolation: %+v %v", other, err)
	}
	if other, err := repo.Get(ctx, otherTeam, last.ID); err != nil || other != nil {
		t.Fatalf("cross-team metadata: %+v %v", other, err)
	}
}

func TestRepositoryConcurrentQuotaAdmission(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	team := createTestTeam(t, repo)
	fillRepository(t, repo, team, TeamStorageLimitBytes-1)
	const count = 8
	start := make(chan struct{})
	results := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := repo.Commit(ctx, team, testRecord(1))
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrStorageLimit) {
			t.Fatalf("unexpected admission error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("expected one admitted byte, got %d successes", succeeded)
	}
	records, err := repo.Records(ctx, team)
	if err != nil {
		t.Fatal(err)
	}
	var used int64
	for _, record := range records {
		used += record.Size
	}
	if used != TeamStorageLimitBytes {
		t.Fatalf("concurrent admission exceeded or lost capacity: %d", used)
	}
}

func TestRepositoryConcurrentSameIDReturnsOneRecord(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	team := createTestTeam(t, repo)
	record := testRecord(17)
	const count = 8
	start := make(chan struct{})
	type result struct {
		record Record
		err    error
	}
	results := make(chan result, count)
	for range count {
		go func() {
			<-start
			copy := record
			copy.BlobID = uuid.NewString()
			committed, err := repo.Commit(ctx, team, copy)
			results <- result{committed, err}
		}()
	}
	close(start)
	var firstBlob string
	for range count {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if firstBlob == "" {
			firstBlob = got.record.BlobID
		}
		if got.record.BlobID != firstBlob {
			t.Fatalf("concurrent retry returned a different blob: %+v", got.record)
		}
	}
	records, err := repo.Records(ctx, team)
	if err != nil || len(records) != 1 || records[0].Size != 17 {
		t.Fatalf("duplicate usage: %+v %v", records, err)
	}
}

func TestRepositoryRejectsInvalidSizes(t *testing.T) {
	repo := newTestRepository(t)
	for _, size := range []int64{-1, 0, MaxAssetBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			if _, err := repo.Commit(context.Background(), uuid.NewString(), testRecord(size)); err == nil {
				t.Fatal("invalid record size must not enter logical storage accounting")
			}
		})
	}
}
