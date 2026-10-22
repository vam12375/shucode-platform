package teamassets

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/contracts"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func deletionProjection(r store.TeamAssetDeletionRecord) contracts.CloudAssetDeletionRequest {
	out := contracts.CloudAssetDeletionRequest{ID: r.ID, AssetID: r.AssetID, AssetName: r.AssetName, RequestedBy: Uploader{ID: r.RequestedByID, Name: r.RequestedByName}, CreatedAt: r.CreatedAt, Status: r.Status, ReviewedAt: r.ReviewedAt}
	if r.ReviewedAt != nil {
		out.ReviewedBy = &Uploader{ID: r.ReviewedByID, Name: r.ReviewedByName}
	}
	return out
}

func (s Service) RequestDeletion(ctx context.Context, team, actorID, assetID string) (contracts.CloudAssetDeletionRequest, error) {
	r, err := s.Repository.Store.RequestTeamAssetDeletion(ctx, team, actorID, assetID, uuid.NewString(), s.Now())
	if err != nil {
		return contracts.CloudAssetDeletionRequest{}, mapStoreError(err)
	}
	return deletionProjection(r), nil
}

type deletionCursor struct {
	Team      string    `json:"team"`
	Actor     string    `json:"actor"`
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
}

func (s Service) ListDeletionRequests(ctx context.Context, team, actorID, cursor string) (contracts.CloudAssetDeletionOverview, error) {
	var beforeAt *time.Time
	var beforeID string
	if cursor != "" {
		if len(cursor) > 4096 {
			return contracts.CloudAssetDeletionOverview{}, ErrInvalid
		}
		raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
		if err != nil {
			return contracts.CloudAssetDeletionOverview{}, ErrInvalid
		}
		var position deletionCursor
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&position) != nil || decoder.Decode(new(any)) != io.EOF || position.Team != team || position.Actor != actorID || position.CreatedAt.IsZero() || !validID(position.ID) {
			return contracts.CloudAssetDeletionOverview{}, ErrInvalid
		}
		beforeAt, beforeID = &position.CreatedAt, position.ID
	}
	page, canReview, err := s.Repository.Store.ListTeamAssetDeletionRequests(ctx, team, actorID, beforeAt, beforeID)
	if err != nil {
		return contracts.CloudAssetDeletionOverview{}, mapStoreError(err)
	}
	out := contracts.CloudAssetDeletionOverview{TeamID: team, Viewer: contracts.CloudAssetDeletionViewer{ID: actorID, CanReview: canReview}, Requests: make([]contracts.CloudAssetDeletionRequest, 0, len(page.Records)), PendingCount: page.PendingCount}
	var lastPending store.TeamAssetDeletionRecord
	// Unicode 名称按真实 JSON 字节计量；pending 优先，极端长展示名只裁减最近历史。
	// 预留信封与游标开销，不能通过放大远程 RPC frame 来掩盖无界响应。
	wireBytes := 1024
	for _, r := range page.Records {
		projection := deletionProjection(r)
		encoded, err := json.Marshal(projection)
		if err != nil {
			return contracts.CloudAssetDeletionOverview{}, err
		}
		if wireBytes+len(encoded)+1 > 500*1024 {
			break
		}
		wireBytes += len(encoded) + 1
		out.Requests = append(out.Requests, projection)
		if r.Status == "pending" {
			lastPending = r
		}
	}
	if page.HasMore {
		raw, err := json.Marshal(deletionCursor{Team: team, Actor: actorID, CreatedAt: lastPending.CreatedAt, ID: lastPending.ID})
		if err != nil {
			return contracts.CloudAssetDeletionOverview{}, err
		}
		next := base64.RawURLEncoding.EncodeToString(raw)
		out.NextCursor = &next
	}
	// 清理任务不受历史100条展示上限影响；数据库approved是唯一可删除字节的依据。
	cleanup, err := s.Repository.Store.ListTeamAssetBlobCleanups(ctx, team)
	if err == nil {
		for _, r := range cleanup {
			s.cleanupDeletion(ctx, team, r)
		}
	} else if s.Logger != nil {
		s.Logger.Warn("读取资产删除清理任务失败", "error", err)
	}
	return out, nil
}

func (s Service) ReviewDeletion(ctx context.Context, team, actorID, requestID, decision string) (contracts.CloudAssetDeletionRequest, error) {
	r, err := s.Repository.Store.ReviewTeamAssetDeletion(ctx, team, actorID, requestID, decision, s.Now())
	if err != nil {
		return contracts.CloudAssetDeletionRequest{}, mapStoreError(err)
	}
	// 仅确认提交成功后清理。提交结果不明时不删字节，后续读取持久化审批事实再重试。
	s.cleanupDeletion(ctx, team, r)
	return deletionProjection(r), nil
}

func (s Service) cleanupDeletion(ctx context.Context, team string, r store.TeamAssetDeletionRecord) {
	if r.Status != "approved" || r.BlobCleanedAt != nil || !validID(team) || !validID(r.BlobID) {
		return
	}
	err := os.Remove(s.Blobs.Path(team, r.BlobID))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		err = s.Repository.Store.MarkTeamAssetDeletionBlobCleaned(ctx, team, r.ID, s.Now())
	}
	if err != nil && s.Logger != nil {
		s.Logger.Warn("资产审批已完成，字节清理等待重试", "error", err)
	}
}
