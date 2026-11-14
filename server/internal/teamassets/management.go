package teamassets

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/vam12375/shucode-platform/server/internal/contracts"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

var ErrSnapshotChanged = errors.New("team_asset_snapshot_changed")

type ManagedPage struct {
	Assets      []contracts.ManagedCloudAsset `json:"assets"`
	NextCursor  *string                       `json:"nextCursor"`
	Revision    string                        `json:"revision"`
	NotModified bool                          `json:"notModified,omitempty"`
	Facets      store.TeamAssetFacets         `json:"facets"`
	Storage     Storage                       `json:"-"`
}
type queryCursor struct {
	Scope, Revision string
	Offset          int
}

func (s Service) Query(ctx context.Context, team, actor, cursor string, q store.TeamAssetQuery) (ManagedPage, error) {
	scopeBytes, _ := json.Marshal([]any{team, actor, q.Q, q.Kind, q.UploadedBy, q.Tag, q.FavoritesOnly, q.Sort})
	digest := sha256.Sum256(scopeBytes)
	scope := hex.EncodeToString(digest[:])
	if cursor != "" {
		if len(cursor) > 4096 {
			return ManagedPage{}, ErrInvalid
		}
		raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
		if err != nil {
			return ManagedPage{}, ErrInvalid
		}
		var position queryCursor
		if json.Unmarshal(raw, &position) != nil || position.Scope != scope || position.Offset < 1 || position.Revision == "" {
			return ManagedPage{}, ErrInvalid
		}
		q.Offset = position.Offset
		q.ExpectedRevision = position.Revision
	}
	if q.KnownRevision != "" {
		prefix := scope + ":"
		if strings.HasPrefix(q.KnownRevision, prefix) {
			q.KnownRevision = strings.TrimPrefix(q.KnownRevision, prefix)
		} else {
			q.KnownRevision = ""
		}
	}
	q.Limit = PageSize + 1
	result, err := s.Repository.Store.QueryTeamAssets(ctx, team, actor, q)
	if err != nil {
		return ManagedPage{}, mapStoreError(err)
	}
	page := ManagedPage{Assets: []contracts.ManagedCloudAsset{}, Revision: scope + ":" + result.Revision, NotModified: result.NotModified, Facets: result.Facets, Storage: Storage{UsedBytes: result.UsedBytes, LimitBytes: TeamStorageLimitBytes}}
	records := result.Records
	if len(records) > PageSize {
		records = records[:PageSize]
		raw, _ := json.Marshal(queryCursor{Scope: scope, Revision: result.Revision, Offset: q.Offset + PageSize})
		next := base64.RawURLEncoding.EncodeToString(raw)
		page.NextCursor = &next
	}
	for _, r := range records {
		page.Assets = append(page.Assets, contracts.ManagedCloudAsset{CloudAsset: fromStoreRecord(r.TeamAssetRecord).Asset, Tags: r.Tags, Favorite: r.Favorite})
	}
	return page, nil
}
