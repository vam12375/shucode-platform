package store

import (
	"context"
	"sort"
	"time"
)

func (s *memoryStore) ListTeamAssetBlobCleanups(ctx context.Context, teamID string) ([]TeamAssetDeletionRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !canonicalUUID(teamID) {
		return nil, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []TeamAssetDeletionRecord{}
	for _, r := range s.teamAssetDeletions[teamID] {
		if r.Status == "approved" && r.BlobCleanedAt == nil {
			out = append(out, cloneTeamAssetDeletion(r))
		}
	}
	return out, nil
}

func (s *memoryStore) teamAssetActorLocked(teamID, actorID string) (*TeamMemberView, error) {
	team, ok := s.teams[teamID]
	if !ok || team.DisabledAt != nil {
		return nil, ErrTeamAssetForbidden
	}
	actor := s.memberViewLocked(teamID, actorID)
	if actor == nil || actor.MemberDisabledAt != nil || actor.User.DisabledAt != nil {
		return nil, ErrTeamAssetForbidden
	}
	return actor, nil
}

func (s *memoryStore) RequestTeamAssetDeletion(ctx context.Context, teamID, actorID, assetID, requestID string, at time.Time) (TeamAssetDeletionRecord, error) {
	if err := ctx.Err(); err != nil {
		return TeamAssetDeletionRecord{}, err
	}
	if !canonicalUUID(teamID) || !canonicalUUID(actorID) || !canonicalUUID(assetID) || !canonicalUUID(requestID) || at.IsZero() {
		return TeamAssetDeletionRecord{}, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	actor, err := s.teamAssetActorLocked(teamID, actorID)
	if err != nil {
		return TeamAssetDeletionRecord{}, err
	}
	asset, ok := s.teamAssets[teamID][assetID]
	if !ok {
		return TeamAssetDeletionRecord{}, ErrTeamAssetNotFound
	}
	if asset.UploadedByID != actorID {
		return TeamAssetDeletionRecord{}, ErrTeamAssetForbidden
	}
	for _, r := range s.teamAssetDeletions[teamID] {
		if r.AssetID == assetID && r.Status == "pending" {
			return cloneTeamAssetDeletion(r), nil
		}
	}
	if s.teamAssetDeletions[teamID] == nil {
		s.teamAssetDeletions[teamID] = map[string]TeamAssetDeletionRecord{}
	}
	if _, ok := s.teamAssetDeletions[teamID][requestID]; ok {
		return TeamAssetDeletionRecord{}, ErrTeamAssetConflict
	}
	record := TeamAssetDeletionRecord{ID: requestID, AssetID: assetID, AssetName: asset.Name, RequestedByID: actorID, RequestedByName: teamAssetDeletionDisplayName(actor.User.DisplayName), CreatedAt: at.UTC(), Status: "pending", BlobID: asset.BlobID, Size: asset.Size}
	s.teamAssetDeletions[teamID][requestID] = record
	return cloneTeamAssetDeletion(record), nil
}

func (s *memoryStore) ListTeamAssetDeletionRequests(ctx context.Context, teamID, actorID string, beforeAt *time.Time, beforeID string) (TeamAssetDeletionPage, bool, error) {
	if err := ctx.Err(); err != nil {
		return TeamAssetDeletionPage{}, false, err
	}
	if !canonicalUUID(teamID) || !canonicalUUID(actorID) || (beforeAt == nil && beforeID != "") || (beforeAt != nil && !canonicalUUID(beforeID)) {
		return TeamAssetDeletionPage{}, false, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	actor, err := s.teamAssetActorLocked(teamID, actorID)
	if err != nil {
		return TeamAssetDeletionPage{}, false, err
	}
	canReview := IsTeamAdmin(actor.Role)
	pending := []TeamAssetDeletionRecord{}
	history := []TeamAssetDeletionRecord{}
	page := TeamAssetDeletionPage{Records: []TeamAssetDeletionRecord{}}
	for _, r := range s.teamAssetDeletions[teamID] {
		if !canReview && r.RequestedByID != actorID {
			continue
		}
		if r.Status == "pending" {
			page.PendingCount++
			if beforeAt == nil || r.CreatedAt.Before(*beforeAt) || (r.CreatedAt.Equal(*beforeAt) && r.ID < beforeID) {
				pending = append(pending, cloneTeamAssetDeletion(r))
			}
		} else if beforeAt == nil {
			history = append(history, cloneTeamAssetDeletion(r))
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		a, b := pending[i], pending[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID > b.ID
	})
	sort.Slice(history, func(i, j int) bool {
		a, b := history[i], history[j]
		at, bt := teamAssetDeletionCompletedAt(a), teamAssetDeletionCompletedAt(b)
		if !at.Equal(bt) {
			return at.After(bt)
		}
		return a.ID > b.ID
	})
	page.HasMore = len(pending) > 100
	page.Records = append(page.Records, pending[:min(len(pending), 100)]...)
	page.Records = append(page.Records, history[:min(len(history), 100)]...)
	return page, canReview, nil
}

func teamAssetDeletionCompletedAt(r TeamAssetDeletionRecord) time.Time {
	// 撤回不伪造审批人或审批时间；历史按各终态真实完成时间统一排序。
	if r.WithdrawnAt != nil {
		return *r.WithdrawnAt
	}
	return *r.ReviewedAt
}

func (s *memoryStore) WithdrawTeamAssetDeletion(ctx context.Context, teamID, actorID, requestID string, at time.Time) (TeamAssetDeletionRecord, error) {
	if err := ctx.Err(); err != nil {
		return TeamAssetDeletionRecord{}, err
	}
	if !canonicalUUID(teamID) || !canonicalUUID(actorID) || !canonicalUUID(requestID) || at.IsZero() {
		return TeamAssetDeletionRecord{}, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.teamAssetActorLocked(teamID, actorID); err != nil {
		return TeamAssetDeletionRecord{}, err
	}
	r, ok := s.teamAssetDeletions[teamID][requestID]
	if !ok {
		return TeamAssetDeletionRecord{}, ErrTeamAssetNotFound
	}
	// 本人权限先于幂等判断，管理员也不能借重试撤回他人的申请。
	if r.RequestedByID != actorID {
		return TeamAssetDeletionRecord{}, ErrTeamAssetForbidden
	}
	if r.Status == "withdrawn" {
		return cloneTeamAssetDeletion(r), nil
	}
	if r.Status != "pending" {
		return TeamAssetDeletionRecord{}, ErrTeamAssetConflict
	}
	// 与审批共用同一临界区，只转换 pending；不改动资产、用量或清理事实。
	r.Status = "withdrawn"
	utc := at.UTC()
	r.WithdrawnAt = &utc
	s.teamAssetDeletions[teamID][requestID] = r
	return cloneTeamAssetDeletion(r), nil
}

func (s *memoryStore) ReviewTeamAssetDeletion(ctx context.Context, teamID, actorID, requestID, decision string, at time.Time) (TeamAssetDeletionRecord, error) {
	if err := ctx.Err(); err != nil {
		return TeamAssetDeletionRecord{}, err
	}
	if !canonicalUUID(teamID) || !canonicalUUID(actorID) || !canonicalUUID(requestID) || at.IsZero() || (decision != "approve" && decision != "reject") {
		return TeamAssetDeletionRecord{}, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	actor, err := s.teamAssetActorLocked(teamID, actorID)
	if err != nil {
		return TeamAssetDeletionRecord{}, err
	}
	if !IsTeamAdmin(actor.Role) {
		return TeamAssetDeletionRecord{}, ErrTeamAssetForbidden
	}
	r, ok := s.teamAssetDeletions[teamID][requestID]
	if !ok {
		return TeamAssetDeletionRecord{}, ErrTeamAssetNotFound
	}
	status := "rejected"
	if decision == "approve" {
		status = "approved"
	}
	if r.Status != "pending" {
		if r.Status != status {
			return TeamAssetDeletionRecord{}, ErrTeamAssetConflict
		}
		return cloneTeamAssetDeletion(r), nil
	}
	if status == "approved" {
		asset, ok := s.teamAssets[teamID][r.AssetID]
		if !ok || asset.BlobID != r.BlobID || asset.UploadedByID != r.RequestedByID || asset.Size != r.Size {
			return TeamAssetDeletionRecord{}, ErrTeamAssetConflict
		}
		if s.teamAssetUsedBytes[teamID] < asset.Size {
			return TeamAssetDeletionRecord{}, ErrTeamAssetConflict
		}
		// 审批与容量释放在同一临界区完成，拒绝不触碰资产事实。
		delete(s.teamAssets[teamID], r.AssetID)
		delete(s.teamAssetBlobIDs[teamID], asset.BlobID)
		s.teamAssetUsedBytes[teamID] -= asset.Size
	}
	r.Status = status
	r.ReviewedByID = actorID
	r.ReviewedByName = teamAssetDeletionDisplayName(actor.User.DisplayName)
	utc := at.UTC()
	r.ReviewedAt = &utc
	s.teamAssetDeletions[teamID][requestID] = r
	return cloneTeamAssetDeletion(r), nil
}

func (s *memoryStore) MarkTeamAssetDeletionBlobCleaned(ctx context.Context, teamID, requestID string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !canonicalUUID(teamID) || !canonicalUUID(requestID) || at.IsZero() {
		return ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.teamAssetDeletions[teamID][requestID]
	if !ok {
		return ErrTeamAssetNotFound
	}
	if r.Status != "approved" {
		return ErrTeamAssetConflict
	}
	if r.BlobCleanedAt == nil {
		utc := at.UTC()
		r.BlobCleanedAt = &utc
		s.teamAssetDeletions[teamID][requestID] = r
	}
	return nil
}
