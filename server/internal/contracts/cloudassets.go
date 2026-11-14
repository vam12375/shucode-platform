package contracts

import "time"

// 对齐客户端 packages/shared/src/cloudAsset.ts；内容地址不属于通用资产契约。
const (
	CloudAssetMaxBytes              int64 = 100 << 20
	CloudAssetTeamStorageLimitBytes int64 = 5 << 30
	CloudAssetPageSize                    = 100
)

type CloudAssetUploader struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type CloudAsset struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Kind        string             `json:"kind"`
	Mime        string             `json:"mime"`
	Size        int64              `json:"size"`
	CreatedAt   time.Time          `json:"createdAt"`
	UploadedBy  CloudAssetUploader `json:"uploadedBy"`
	ContentHash string             `json:"contentHash,omitempty"`
}

type ManagedCloudAsset struct {
	CloudAsset
	Tags     []string `json:"tags"`
	Favorite bool     `json:"favorite"`
}

type CloudAssetStorage struct {
	UsedBytes  int64 `json:"usedBytes"`
	LimitBytes int64 `json:"limitBytes"`
}

type CloudAssetTeam struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Storage CloudAssetStorage `json:"storage"`
}

type CloudAssetPage struct {
	Team       CloudAssetTeam `json:"team"`
	Assets     []CloudAsset   `json:"assets"`
	NextCursor *string        `json:"nextCursor"`
}

type CloudAssetDeletionRequest struct {
	ID          string              `json:"id"`
	AssetID     string              `json:"assetId"`
	AssetName   string              `json:"assetName"`
	RequestedBy CloudAssetUploader  `json:"requestedBy"`
	CreatedAt   time.Time           `json:"createdAt"`
	Status      string              `json:"status"`
	ReviewedBy  *CloudAssetUploader `json:"reviewedBy,omitempty"`
	ReviewedAt  *time.Time          `json:"reviewedAt,omitempty"`
	WithdrawnAt *time.Time          `json:"withdrawnAt,omitempty"`
}

type CloudAssetDeletionViewer struct {
	ID        string `json:"id"`
	CanReview bool   `json:"canReview"`
}

type CloudAssetDeletionOverview struct {
	TeamID       string                      `json:"teamId"`
	Viewer       CloudAssetDeletionViewer    `json:"viewer"`
	Requests     []CloudAssetDeletionRequest `json:"requests"`
	PendingCount int64                       `json:"pendingCount"`
	NextCursor   *string                     `json:"nextCursor"`
}

type CloudAssetKindFacet struct {
	Kind  string `json:"kind"`
	Count int64  `json:"count"`
	Bytes int64  `json:"bytes"`
}
type CloudAssetUploaderFacet struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}
type CloudAssetFacets struct {
	Total              int64                     `json:"total"`
	Kinds              []CloudAssetKindFacet     `json:"kinds"`
	Uploaders          []CloudAssetUploaderFacet `json:"uploaders"`
	Tags               []string                  `json:"tags"`
	TagsTruncated      bool                      `json:"tagsTruncated"`
	UploadersTruncated bool                      `json:"uploadersTruncated"`
}
type CloudAssetCapabilities struct {
	Management  bool `json:"management"`
	Thumbnail   bool `json:"thumbnail"`
	ContentHash bool `json:"contentHash"`
}
type ManagedCloudAssetPage struct {
	Team         CloudAssetTeam         `json:"team"`
	Assets       []ManagedCloudAsset    `json:"assets"`
	NextCursor   *string                `json:"nextCursor"`
	Revision     string                 `json:"revision"`
	NotModified  bool                   `json:"notModified"`
	Facets       CloudAssetFacets       `json:"facets"`
	Capabilities CloudAssetCapabilities `json:"capabilities"`
}
