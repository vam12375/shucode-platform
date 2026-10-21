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
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Kind       string             `json:"kind"`
	Mime       string             `json:"mime"`
	Size       int64              `json:"size"`
	CreatedAt  time.Time          `json:"createdAt"`
	UploadedBy CloudAssetUploader `json:"uploadedBy"`
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
