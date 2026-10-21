package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMemoryTeamAssetsConformance(t *testing.T) {
	s, closeStore := newMemoryForTest(t)
	defer closeStore()
	runTeamAssetConformance(t, s)
}

func TestPostgresTeamAssetsConformance(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 TEST_DATABASE_URL,跳过 Postgres 团队资产 conformance")
	}
	s, closeStore := newPostgresForTest(t, url, os.Getenv("TEST_MIGRATIONS_DIR"))
	defer closeStore()
	runTeamAssetConformance(t, s)
}

func runTeamAssetConformance(t *testing.T, s PlatformStore) {
	t.Helper()
	ctx := context.Background()
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "team-assets-" + uuid.NewString(), QuotaCredits: "0"})
	if err != nil {
		t.Fatal(err)
	}
	uploader := mustUser(t, ctx, s, "asset-"+uuid.NewString()+"@store.test")
	otherUploader := mustUser(t, ctx, s, "asset-other-"+uuid.NewString()+"@store.test")
	base := time.Date(2026, 10, 21, 12, 0, 0, 123000000, time.UTC)

	if _, _, err := s.CommitTeamAsset(ctx, uuid.NewString(), testTeamAsset(uploader.ID, 1, base, 1), 100); !errors.Is(err, ErrTeamAssetTeamNotFound) {
		t.Fatalf("unknown team commit should return semantic error: %v", err)
	}
	if _, err := s.GetTeamAsset(ctx, uuid.NewString(), uuid.NewString()); !errors.Is(err, ErrTeamAssetTeamNotFound) {
		t.Fatalf("unknown team get should return semantic error: %v", err)
	}
	if _, err := s.ListTeamAssetPage(ctx, uuid.NewString(), nil, "", 10); !errors.Is(err, ErrTeamAssetTeamNotFound) {
		t.Fatalf("unknown team list should return semantic error: %v", err)
	}
	validRecord := testTeamAsset(uploader.ID, 90, base, 1)
	for label, mutate := range map[string]func(*TeamAssetRecord){
		"asset id":        func(r *TeamAssetRecord) { r.ID = "not-a-uuid" },
		"uploader id":     func(r *TeamAssetRecord) { r.UploadedByID = "not-a-uuid" },
		"blob id":         func(r *TeamAssetRecord) { r.BlobID = "not-a-uuid" },
		"noncanonical id": func(r *TeamAssetRecord) { r.ID = strings.ToUpper("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa") },
		"long name":       func(r *TeamAssetRecord) { r.Name = strings.Repeat("界", 256) },
		"kind":            func(r *TeamAssetRecord) { r.Kind = "unknown" },
		"long mime":       func(r *TeamAssetRecord) { r.Mime = strings.Repeat("a", 256) },
		"sha256":          func(r *TeamAssetRecord) { r.SHA256 = strings.ToUpper(r.SHA256) },
	} {
		invalid := validRecord
		mutate(&invalid)
		if _, _, err := s.CommitTeamAsset(ctx, team.ID, invalid, 100); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid %s should return ErrInvalidInput: %v", label, err)
		}
	}
	if _, err := s.GetTeamAsset(ctx, team.ID, "not-a-uuid"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid asset id should return ErrInvalidInput: %v", err)
	}
	if _, err := s.ListTeamAssetPage(ctx, team.ID, &base, "not-a-uuid", 10); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid cursor id should return ErrInvalidInput: %v", err)
	}
	if _, _, err := s.CommitTeamAsset(ctx, "not-a-uuid", validRecord, 100); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid team id on commit should return ErrInvalidInput: %v", err)
	}
	if _, err := s.GetTeamAsset(ctx, "not-a-uuid", validRecord.ID); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid team id on get should return ErrInvalidInput: %v", err)
	}
	if _, err := s.ListTeamAssetPage(ctx, "not-a-uuid", nil, "", 10); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid team id on list should return ErrInvalidInput: %v", err)
	}

	initialRecords := make([]TeamAssetRecord, 0, 6)
	for i := 1; i <= 6; i++ {
		record := testTeamAsset(uploader.ID, i, base.Add(time.Duration(i/2)*time.Second), int64(i))
		if _, created, err := s.CommitTeamAsset(ctx, team.ID, record, 100); err != nil || !created {
			t.Fatalf("commit %d: created=%v error=%v", i, created, err)
		}
		initialRecords = append(initialRecords, record)
	}
	duplicateBlob := testTeamAsset(uploader.ID, 91, base, 1)
	duplicateBlob.BlobID = initialRecords[0].BlobID
	if _, _, err := s.CommitTeamAsset(ctx, team.ID, duplicateBlob, 100); !errors.Is(err, ErrTeamAssetConflict) {
		t.Fatalf("a blob cannot be referenced by two assets in one team: %v", err)
	}
	first := initialRecords[0]
	retry := first
	retry.UploadedByName = "changed display name"
	retry.BlobID = uuid.NewString()
	saved, created, err := s.CommitTeamAsset(ctx, team.ID, retry, 100)
	if err != nil || created || saved.BlobID != first.BlobID || saved.UploadedByName != first.UploadedByName {
		t.Fatalf("same immutable upload should reuse first row: saved=%+v created=%v error=%v", saved, created, err)
	}
	for label, mutate := range map[string]func(*TeamAssetRecord){
		"name":     func(r *TeamAssetRecord) { r.Name = "different.md" },
		"hash":     func(r *TeamAssetRecord) { r.SHA256 = fmt.Sprintf("%064x", 99) },
		"uploader": func(r *TeamAssetRecord) { r.UploadedByID = otherUploader.ID },
		"size":     func(r *TeamAssetRecord) { r.Size++ },
	} {
		conflict := first
		mutate(&conflict)
		if _, _, err := s.CommitTeamAsset(ctx, team.ID, conflict, 100); !errors.Is(err, ErrTeamAssetConflict) {
			t.Fatalf("same ID with changed %s should conflict: %v", label, err)
		}
	}

	page1, err := s.ListTeamAssetPage(ctx, team.ID, nil, "", 3)
	if err != nil || len(page1.Records) != 3 || page1.UsedBytes != 21 {
		t.Fatalf("first page/total usage: page=%+v error=%v", page1, err)
	}
	last := page1.Records[len(page1.Records)-1]
	page2, err := s.ListTeamAssetPage(ctx, team.ID, &last.CreatedAt, last.ID, 3)
	if err != nil || len(page2.Records) != 3 || page2.UsedBytes != 21 {
		t.Fatalf("second page must report full usage: page=%+v error=%v", page2, err)
	}
	seen := map[string]bool{}
	for _, record := range append(page1.Records, page2.Records...) {
		if seen[record.ID] {
			t.Fatalf("cursor pages repeated asset %s", record.ID)
		}
		seen[record.ID] = true
	}
	if len(seen) != 6 {
		t.Fatalf("cursor pages should include six distinct assets: %d", len(seen))
	}
	t.Run("more-than-one-page-without-count-cap", func(t *testing.T) {
		unboundedTeam, err := s.CreateTeam(ctx, CreateTeamInput{Name: "team-assets-unbounded-" + uuid.NewString(), QuotaCredits: "0"})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 125; i++ {
			record := testTeamAsset(uploader.ID, i+3000, base.Add(time.Duration(i)*time.Nanosecond), 1)
			if _, created, err := s.CommitTeamAsset(ctx, unboundedTeam.ID, record, 5*1024*1024*1024); err != nil || !created {
				t.Fatalf("commit asset %d: created=%v error=%v", i, created, err)
			}
		}
		page, err := s.ListTeamAssetPage(ctx, unboundedTeam.ID, nil, "", 100)
		if err != nil || len(page.Records) != 100 || page.UsedBytes != 125 {
			t.Fatalf("first page should report all 125 one-byte assets: page=%+v error=%v", page, err)
		}
		last := page.Records[len(page.Records)-1]
		at := last.CreatedAt
		next, err := s.ListTeamAssetPage(ctx, unboundedTeam.ID, &at, last.ID, 100)
		if err != nil || len(next.Records) != 25 || next.UsedBytes != 125 {
			t.Fatalf("second page should complete the unbounded listing: page=%+v error=%v", next, err)
		}
	})
	got, err := s.GetTeamAsset(ctx, team.ID, first.ID)
	if err != nil || got == nil || !sameTeamAssetUpload(*got, first) || got.Kind != first.Kind || got.Mime != first.Mime ||
		got.UploadedByName != first.UploadedByName || got.BlobID != first.BlobID || !got.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("get should return the stored immutable record: got=%+v error=%v", got, err)
	}
	missing, err := s.GetTeamAsset(ctx, team.ID, uuid.NewString())
	if err != nil || missing != nil {
		t.Fatalf("missing asset should return nil: got=%+v error=%v", missing, err)
	}

	t.Run("exact-five-gib-and-no-count-cap", func(t *testing.T) {
		quotaTeam, err := s.CreateTeam(ctx, CreateTeamInput{Name: "team-assets-quota-" + uuid.NewString(), QuotaCredits: "0"})
		if err != nil {
			t.Fatal(err)
		}
		const storageLimit int64 = 5 * 1024 * 1024 * 1024
		const fullAsset int64 = TeamAssetMaxBytes
		used := int64(0)
		for i := 0; used < storageLimit; i++ {
			size := fullAsset
			if remaining := storageLimit - used; size > remaining {
				size = remaining
			}
			record := testTeamAsset(uploader.ID, i+100, base.Add(time.Duration(i)*time.Second), size)
			if _, created, err := s.CommitTeamAsset(ctx, quotaTeam.ID, record, storageLimit); err != nil || !created {
				t.Fatalf("commit quota record %d (size=%d): created=%v error=%v", i, size, created, err)
			}
			used += size
		}
		if _, _, err := s.CommitTeamAsset(ctx, quotaTeam.ID, testTeamAsset(uploader.ID, 999, base, 1), storageLimit); !errors.Is(err, ErrTeamAssetStorageLimit) {
			t.Fatalf("one byte over 5 GiB must fail: %v", err)
		}
		fullRetry := testTeamAsset(uploader.ID, 100, base, fullAsset)
		fullRetry.BlobID = uuid.NewString()
		if _, created, err := s.CommitTeamAsset(ctx, quotaTeam.ID, fullRetry, storageLimit); err != nil || created {
			t.Fatalf("idempotent retry at full capacity must succeed: created=%v error=%v", created, err)
		}
		page, err := s.ListTeamAssetPage(ctx, quotaTeam.ID, nil, "", 100)
		if err != nil || page.UsedBytes != storageLimit || len(page.Records) != 52 {
			t.Fatalf("all 52 records should fit exactly without a count cap: records=%d bytes=%d error=%v", len(page.Records), page.UsedBytes, err)
		}
	})

	t.Run("concurrent-capacity-and-idempotency", func(t *testing.T) {
		concurrentTeam, err := s.CreateTeam(ctx, CreateTeamInput{Name: "team-assets-concurrent-" + uuid.NewString(), QuotaCredits: "0"})
		if err != nil {
			t.Fatal(err)
		}
		const workers = 16
		var wg sync.WaitGroup
		var mu sync.Mutex
		createdCount, limitedCount := 0, 0
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, created, err := s.CommitTeamAsset(ctx, concurrentTeam.ID, testTeamAsset(uploader.ID, i+1000, base, 1), 10)
				mu.Lock()
				defer mu.Unlock()
				if errors.Is(err, ErrTeamAssetStorageLimit) {
					limitedCount++
					return
				}
				if err != nil || !created {
					t.Errorf("concurrent commit: created=%v error=%v", created, err)
					return
				}
				createdCount++
			}(i)
		}
		wg.Wait()
		page, err := s.ListTeamAssetPage(ctx, concurrentTeam.ID, nil, "", workers)
		if err != nil || createdCount != 10 || limitedCount != workers-10 || len(page.Records) != 10 || page.UsedBytes != 10 {
			t.Fatalf("capacity race exceeded limit: created=%d limited=%d page=%+v error=%v", createdCount, limitedCount, page, err)
		}

		idempotentTeam, err := s.CreateTeam(ctx, CreateTeamInput{Name: "team-assets-idempotent-" + uuid.NewString(), QuotaCredits: "0"})
		if err != nil {
			t.Fatal(err)
		}
		baseRecord := testTeamAsset(uploader.ID, 2000, base, 1)
		var newCount, replayCount int
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				retry := baseRecord
				retry.BlobID = uuid.NewString()
				_, created, err := s.CommitTeamAsset(ctx, idempotentTeam.ID, retry, 1)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					t.Errorf("concurrent identical commit: %v", err)
				} else if created {
					newCount++
				} else {
					replayCount++
				}
			}()
		}
		wg.Wait()
		page, err = s.ListTeamAssetPage(ctx, idempotentTeam.ID, nil, "", workers)
		if err != nil || newCount != 1 || replayCount != workers-1 || len(page.Records) != 1 || page.UsedBytes != 1 {
			t.Fatalf("concurrent idempotency must commit exactly once: new=%d replay=%d page=%+v error=%v", newCount, replayCount, page, err)
		}
	})
}

func testTeamAsset(uploaderID string, id int, createdAt time.Time, size int64) TeamAssetRecord {
	return TeamAssetRecord{
		ID:             fmt.Sprintf("00000000-0000-4000-8000-%012d", id),
		Name:           fmt.Sprintf("asset-%d.md", id),
		Kind:           "document",
		Mime:           "text/markdown",
		Size:           size,
		CreatedAt:      createdAt,
		UploadedByID:   uploaderID,
		UploadedByName: "Uploader",
		SHA256:         fmt.Sprintf("%064x", id),
		BlobID:         uuid.NewString(),
	}
}
