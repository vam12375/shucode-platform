package store

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/credits"
)

func (s *memoryStore) CreateQuotaRequest(_ context.Context, record QuotaRequestRecord) error {
	normalized, err := normalizeQuotaRequest(record)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	team, ok := s.teams[normalized.TeamID]
	if !ok || team.DisabledAt != nil {
		return ErrQuotaRequestInvalid
	}
	user, ok := s.users[normalized.RequestedBy]
	if !ok || user.DisabledAt != nil {
		return ErrQuotaRequestInvalid
	}
	if user.Role != RoleRoot {
		member := s.memberViewLocked(team.ID, user.ID)
		if member == nil || member.MemberDisabledAt != nil || !IsTeamAdmin(member.Role) {
			return ErrQuotaRequestInvalid
		}
	}
	if normalized.ID == "" {
		normalized.ID = uuid.NewString()
	}
	if _, exists := s.quotaRequests[normalized.ID]; exists {
		return ErrQuotaRequestInvalid
	}
	s.quotaRequests[normalized.ID] = normalized
	return nil
}
func (s *memoryStore) ListQuotaRequests(_ context.Context, teamID string) ([]QuotaRequestRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []QuotaRequestRecord{}
	for _, record := range s.quotaRequests {
		if teamID == "" || record.TeamID == teamID {
			out = append(out, cloneQuotaRequest(record))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}
func (s *memoryStore) ReviewQuotaRequest(_ context.Context, id, reviewerID, decision, note string, at time.Time) (*QuotaRequestRecord, error) {
	note = strings.TrimSpace(note)
	if !validQuotaReview(decision, note) {
		return nil, ErrQuotaRequestInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	reviewer, ok := s.users[reviewerID]
	if !ok || reviewer.Role != RoleRoot || reviewer.DisabledAt != nil {
		return nil, ErrQuotaRequestInvalid
	}
	record, ok := s.quotaRequests[id]
	if !ok {
		return nil, ErrQuotaRequestInvalid
	}
	if record.Status != "pending" {
		return nil, ErrQuotaRequestReviewed
	}
	if decision == "approve" {
		team, ok := s.teams[record.TeamID]
		if !ok || team.DisabledAt != nil {
			return nil, ErrQuotaRequestInvalid
		}
		oldMicro, err := parseQuotaRequestMicro(team.QuotaCredits)
		if err != nil {
			return nil, ErrQuotaRequestInvalid
		}
		amountMicro, err := parseQuotaRequestMicro(record.AmountCredits)
		if err != nil || oldMicro > maxQuotaRequestMicro-amountMicro {
			return nil, ErrQuotaRequestInvalid
		}
		oldQuota := team.QuotaCredits
		team.QuotaCredits = credits.FormatMicroCredits(oldMicro + amountMicro)
		s.teams[team.ID] = team
		s.quotaAdjusts = append(s.quotaAdjusts, quotaAdjustmentRow{TeamID: team.ID, ActorUserID: reviewerID, TeamQuotaAdjustmentView: TeamQuotaAdjustmentView{ID: uuid.NewString(), Action: "approve_quota_request", OldQuotaCredits: oldQuota, NewQuotaCredits: team.QuotaCredits, OldUnlimited: team.Unlimited, NewUnlimited: team.Unlimited, CreatedAt: at}})
		record.Status = "approved"
	} else {
		record.Status = "rejected"
	}
	record.ReviewedBy = reviewerID
	record.ReviewNote = note
	record.ReviewedAt = copyTime(&at)
	s.quotaRequests[id] = record
	copy := cloneQuotaRequest(record)
	return &copy, nil
}
