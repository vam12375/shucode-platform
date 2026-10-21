// Package teamassets owns immutable team assets independently of media projects.
package teamassets

import (
	"errors"

	"github.com/vam12375/shucode-platform/server/internal/contracts"
)

const (
	MaxAssetBytes         = contracts.CloudAssetMaxBytes
	TeamStorageLimitBytes = contracts.CloudAssetTeamStorageLimitBytes
	PageSize              = contracts.CloudAssetPageSize
)

var (
	ErrInvalid      = errors.New("team_asset_invalid_input")
	ErrSizeLimit    = errors.New("team_asset_size_limit")
	ErrStorageLimit = errors.New("team_asset_storage_limit")
	ErrConflict     = errors.New("team_asset_id_conflict")
	ErrBusy         = errors.New("team_asset_write_busy")
	ErrNotFound     = errors.New("team_asset_not_found")
)

type Asset = contracts.CloudAsset
type Uploader = contracts.CloudAssetUploader
type Storage = contracts.CloudAssetStorage

type Record struct {
	Asset
	SHA256 string `json:"sha256"`
	BlobID string `json:"blobId"`
}

type Page struct {
	Assets     []Asset
	NextCursor *string
	Storage    Storage
}
