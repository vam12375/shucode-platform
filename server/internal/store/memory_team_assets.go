package store

import (
	"context"
	"sort"
	"time"
)

func sameTeamAssetUpload(existing, incoming TeamAssetRecord) bool {
	return existing.UploadedByID == incoming.UploadedByID &&
		existing.Name == incoming.Name &&
		existing.SHA256 == incoming.SHA256 &&
		existing.Size == incoming.Size
}

func (s *memoryStore) CommitTeamAsset(ctx context.Context, teamID string, record TeamAssetRecord, limitBytes int64) (TeamAssetRecord, bool, error) {
	return s.commitTeamAsset(ctx, teamID, record, limitBytes, false)
}

func (s *memoryStore) CommitTeamAssetForActiveMember(ctx context.Context, teamID string, record TeamAssetRecord, limitBytes int64) (TeamAssetRecord, bool, error) {
	return s.commitTeamAsset(ctx, teamID, record, limitBytes, true)
}

func (s *memoryStore) commitTeamAsset(ctx context.Context, teamID string, record TeamAssetRecord, limitBytes int64, activeMember bool) (TeamAssetRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return TeamAssetRecord{}, false, err
	}
	if err := validateTeamAssetRecord(teamID, record, limitBytes); err != nil {
		return TeamAssetRecord{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.teams[teamID]; !ok {
		return TeamAssetRecord{}, false, ErrTeamAssetTeamNotFound
	}
	if activeMember {
		// 后台归档没有路由鉴权快照，成员失效与资产登记必须在同一临界区裁决。
		actor, err := s.teamAssetActorLocked(teamID, record.UploadedByID)
		if err != nil {
			return TeamAssetRecord{}, false, err
		}
		record.UploadedByName = actor.User.DisplayName
	}
	assets := s.teamAssets[teamID]
	// 已批准身份和 blob 永久保留墓碑，阻止迟到上传恢复资产或复用待清理字节。
	for _, deletion := range s.teamAssetDeletions[teamID] {
		if deletion.Status == "approved" && (deletion.AssetID == record.ID || deletion.BlobID == record.BlobID) {
			return TeamAssetRecord{}, false, ErrTeamAssetConflict
		}
	}
	if existing, ok := assets[record.ID]; ok {
		if !sameTeamAssetUpload(existing, record) {
			return TeamAssetRecord{}, false, ErrTeamAssetConflict
		}
		return existing, false, nil
	}
	blobIDs := s.teamAssetBlobIDs[teamID]
	if _, ok := blobIDs[record.BlobID]; ok {
		return TeamAssetRecord{}, false, ErrTeamAssetConflict
	}
	used := s.teamAssetUsedBytes[teamID]
	if used > limitBytes || record.Size > limitBytes-used {
		return TeamAssetRecord{}, false, ErrTeamAssetStorageLimit
	}
	if assets == nil {
		assets = make(map[string]TeamAssetRecord)
		s.teamAssets[teamID] = assets
	}
	assets[record.ID] = record
	if blobIDs == nil {
		blobIDs = make(map[string]string)
		s.teamAssetBlobIDs[teamID] = blobIDs
	}
	blobIDs[record.BlobID] = record.ID
	s.teamAssetUsedBytes[teamID] = used + record.Size
	return record, true, nil
}

func (s *memoryStore) IsTeamAssetDeleted(ctx context.Context, teamID, id string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !canonicalUUID(teamID) || !canonicalUUID(id) {
		return false, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.teamAssetDeletions[teamID] {
		if r.AssetID == id && r.Status == "approved" {
			return true, nil
		}
	}
	return false, nil
}

func (s *memoryStore) GetTeamAsset(ctx context.Context, teamID, id string) (*TeamAssetRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !canonicalUUID(teamID) || !canonicalUUID(id) {
		return nil, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.teams[teamID]; !ok {
		return nil, ErrTeamAssetTeamNotFound
	}
	if record, ok := s.teamAssets[teamID][id]; ok {
		copy := record
		return &copy, nil
	}
	return nil, nil
}

func (s *memoryStore) ListTeamAssetPage(ctx context.Context, teamID string, beforeCreatedAt *time.Time, beforeID string, limit int) (TeamAssetPage, error) {
	if err := ctx.Err(); err != nil {
		return TeamAssetPage{}, err
	}
	if !canonicalUUID(teamID) || limit < 1 || (beforeCreatedAt == nil && beforeID != "") ||
		(beforeCreatedAt != nil && !canonicalUUID(beforeID)) {
		return TeamAssetPage{}, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.teams[teamID]; !ok {
		return TeamAssetPage{}, ErrTeamAssetTeamNotFound
	}
	all := s.teamAssets[teamID]
	page := TeamAssetPage{Records: make([]TeamAssetRecord, 0, limit), UsedBytes: s.teamAssetUsedBytes[teamID]}
	eligible := make([]TeamAssetRecord, 0, len(all))
	for _, record := range all {
		if beforeCreatedAt == nil || record.CreatedAt.Before(*beforeCreatedAt) ||
			(record.CreatedAt.Equal(*beforeCreatedAt) && record.ID < beforeID) {
			eligible = append(eligible, record)
		}
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].CreatedAt.Equal(eligible[j].CreatedAt) {
			return eligible[i].ID > eligible[j].ID
		}
		return eligible[i].CreatedAt.After(eligible[j].CreatedAt)
	})
	if len(eligible) > limit {
		eligible = eligible[:limit]
	}
	page.Records = append(page.Records, eligible...)
	return page, nil
}
