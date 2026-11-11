package contracts

import (
	"strings"
	"unicode/utf8"
)

// 与客户端 videoAssetManagement.ts 的公开管理投影保持一致。
type MediaAssetManagement struct {
	CanRename          bool   `json:"canRename"`
	CanRequestDeletion bool   `json:"canRequestDeletion"`
	UploaderName       string `json:"uploaderName,omitempty"`
	PendingDeletionID  string `json:"pendingDeletionId,omitempty"`
}
type MediaAssetDeletionRequest struct {
	ID            string `json:"id"`
	AssetID       string `json:"assetId"`
	AssetName     string `json:"assetName"`
	RequesterName string `json:"requesterName"`
	Status        string `json:"status"`
	CreatedAt     string `json:"createdAt"`
	ReviewedAt    string `json:"reviewedAt,omitempty"`
}
type MediaLibrary struct {
	CanReview        bool                        `json:"canReview"`
	DeletionRequests []MediaAssetDeletionRequest `json:"deletionRequests"`
}

func NormalizeMediaAssetName(name string) (string, bool) {
	// 客户端使用 JavaScript trim：它移除 BOM、保留 NEL；Go TrimSpace 的集合相反，需显式对齐。
	name = strings.Trim(name, "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff")
	if len(name) == 0 || utf8.RuneCountInString(name) > 200 {
		return "", false
	}
	for _, r := range name {
		if r < 32 || r == 127 || r == '/' || r == '\\' {
			return "", false
		}
	}
	return name, true
}
