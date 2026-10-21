package teamassets

import (
	"context"
	"errors"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

type Repository struct{ Store store.PlatformStore }

func toStoreRecord(record Record) store.TeamAssetRecord {
	return store.TeamAssetRecord{
		ID: record.ID, Name: record.Name, Kind: record.Kind, Mime: record.Mime, Size: record.Size,
		CreatedAt: record.CreatedAt, UploadedByID: record.UploadedBy.ID, UploadedByName: record.UploadedBy.Name,
		SHA256: record.SHA256, BlobID: record.BlobID,
	}
}

func fromStoreRecord(record store.TeamAssetRecord) Record {
	return Record{
		Asset: Asset{
			ID: record.ID, Name: record.Name, Kind: record.Kind, Mime: record.Mime, Size: record.Size,
			CreatedAt: record.CreatedAt, UploadedBy: Uploader{ID: record.UploadedByID, Name: record.UploadedByName},
		},
		SHA256: record.SHA256, BlobID: record.BlobID,
	}
}

func sameUpload(a, b Record) bool {
	return a.ID == b.ID && a.UploadedBy.ID == b.UploadedBy.ID && a.Name == b.Name && a.SHA256 == b.SHA256 && a.Size == b.Size
}

func mapStoreError(err error) error {
	switch {
	case errors.Is(err, store.ErrTeamAssetStorageLimit):
		return ErrStorageLimit
	case errors.Is(err, store.ErrTeamAssetConflict):
		return ErrConflict
	case errors.Is(err, store.ErrTeamAssetTeamNotFound):
		return ErrNotFound
	case errors.Is(err, store.ErrInvalidInput):
		return ErrInvalid
	default:
		return err
	}
}

func (r Repository) Records(ctx context.Context, team string) ([]Record, error) {
	var (
		beforeAt *time.Time
		beforeID string
		out      []Record
	)
	for {
		page, err := r.Store.ListTeamAssetPage(ctx, team, beforeAt, beforeID, 500)
		if err != nil {
			return nil, mapStoreError(err)
		}
		for _, record := range page.Records {
			out = append(out, fromStoreRecord(record))
		}
		if len(page.Records) < 500 {
			return out, nil
		}
		last := page.Records[len(page.Records)-1]
		at := last.CreatedAt
		beforeAt, beforeID = &at, last.ID
	}
}

func (r Repository) Get(ctx context.Context, team, id string) (*Record, error) {
	record, err := r.Store.GetTeamAsset(ctx, team, id)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if record == nil {
		return nil, nil
	}
	converted := fromStoreRecord(*record)
	return &converted, nil
}

func (r Repository) Commit(ctx context.Context, team string, record Record) (Record, error) {
	if record.Size < 1 || record.Size > MaxAssetBytes {
		return Record{}, ErrInvalid
	}
	saved, _, err := r.Store.CommitTeamAsset(ctx, team, toStoreRecord(record), TeamStorageLimitBytes)
	if err != nil {
		return Record{}, mapStoreError(err)
	}
	return fromStoreRecord(saved), nil
}

func (r Repository) ListPage(ctx context.Context, team string, beforeAt *time.Time, beforeID string, limit int) ([]Record, int64, error) {
	page, err := r.Store.ListTeamAssetPage(ctx, team, beforeAt, beforeID, limit)
	if err != nil {
		return nil, 0, mapStoreError(err)
	}
	converted := make([]Record, len(page.Records))
	for i, record := range page.Records {
		converted[i] = fromStoreRecord(record)
	}
	return converted, page.UsedBytes, nil
}
