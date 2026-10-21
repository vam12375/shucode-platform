package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (s *memoryStore) protectAdminLocked(teamID, userID string) error {
	key := memberKey(teamID, userID)
	if !IsTeamAdmin(s.memberRoles[key]) || s.memberDisabled[key] != nil || s.users[userID].DisabledAt != nil {
		return nil
	}
	for otherID := range s.members[teamID] {
		if otherID != userID && IsTeamAdmin(s.memberRoles[memberKey(teamID, otherID)]) && s.memberDisabled[memberKey(teamID, otherID)] == nil && s.users[otherID].DisabledAt == nil {
			return nil
		}
	}
	return ErrLastTeamAdmin
}

func (s *memoryStore) addMemberLocked(teamID, userID string, role TeamMemberRole) error {
	if !IsTeamAdmin(role) && role != MemberRoleMember {
		return fmt.Errorf("成员角色无效")
	}
	if _, ok := s.teams[teamID]; !ok {
		return fmt.Errorf("团队不存在: %s", teamID)
	}
	if _, ok := s.users[userID]; !ok {
		return fmt.Errorf("用户不存在: %s", userID)
	}
	for otherID, members := range s.members {
		if otherID != teamID {
			if _, ok := members[userID]; ok {
				return ErrUserAlreadyInTeam
			}
		}
	}
	key := memberKey(teamID, userID)
	if !IsTeamAdmin(role) {
		if err := s.protectAdminLocked(teamID, userID); err != nil {
			return err
		}
	}
	if s.members[teamID] == nil {
		s.members[teamID] = map[string]struct{}{}
	}
	if _, ok := s.members[teamID][userID]; !ok {
		s.memberJoinedAt[key] = time.Now()
	}
	s.members[teamID][userID] = struct{}{}
	s.memberRoles[key] = role
	return nil
}

func (s *memoryStore) memberViewLocked(teamID, userID string) *TeamMemberView {
	if _, ok := s.members[teamID][userID]; !ok {
		return nil
	}
	user, ok := s.users[userID]
	if !ok {
		return nil
	}
	key := memberKey(teamID, userID)
	return &TeamMemberView{User: *cloneUser(&user), Role: s.memberRoles[key], CreatedAt: s.memberJoinedAt[key], MemberDisabledAt: copyTime(s.memberDisabled[key]), MonthlyBudgetCredits: s.memberBudgets[key]}
}

func (s *memoryStore) GetTeamMember(_ context.Context, teamID, userID string) (*TeamMemberView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.memberViewLocked(teamID, userID), nil
}

func (s *memoryStore) UpdateTeamMember(_ context.Context, teamID, userID string, patch TeamMemberPatch) (*TeamMemberView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.memberViewLocked(teamID, userID) == nil {
		return nil, nil
	}
	key := memberKey(teamID, userID)
	if patch.Role != nil && !IsTeamAdmin(*patch.Role) && *patch.Role != MemberRoleMember {
		return nil, fmt.Errorf("成员角色无效")
	}
	if (patch.Role != nil && !IsTeamAdmin(*patch.Role)) || (patch.HasDisabled && patch.DisabledAt != nil) {
		if err := s.protectAdminLocked(teamID, userID); err != nil {
			return nil, err
		}
	}
	var budget string
	if patch.MonthlyBudgetCredits != nil {
		var err error
		budget, err = normalizeBudget(*patch.MonthlyBudgetCredits)
		if err != nil {
			return nil, err
		}
	}
	if patch.Role != nil {
		s.memberRoles[key] = *patch.Role
	}
	if patch.HasDisabled {
		s.memberDisabled[key] = copyTime(patch.DisabledAt)
	}
	if patch.MonthlyBudgetCredits != nil {
		s.memberBudgets[key] = budget
	}
	return s.memberViewLocked(teamID, userID), nil
}

func (s *memoryStore) GetTeamPolicy(_ context.Context, teamID string) (*TeamPolicyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.teams[teamID]; !ok {
		return nil, nil
	}
	record, ok := s.teamPolicies[teamID]
	if !ok {
		record = TeamPolicyRecord{TeamID: teamID, BudgetAlertPercent: 80, UpdatedAt: s.teams[teamID].CreatedAt}
	}
	record.AllowedModelIDs = append([]string{}, record.AllowedModelIDs...)
	return &record, nil
}

func (s *memoryStore) SaveTeamPolicy(_ context.Context, record TeamPolicyRecord) (*TeamPolicyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.teams[record.TeamID]; !ok {
		return nil, nil
	}
	normalized, err := normalizeTeamPolicy(record)
	if err != nil {
		return nil, err
	}
	s.teamPolicies[record.TeamID] = normalized
	normalized.AllowedModelIDs = append([]string{}, normalized.AllowedModelIDs...)
	return &normalized, nil
}

func cloneInvitation(record TeamInvitationRecord) TeamInvitationRecord {
	record.AcceptedAt = copyTime(record.AcceptedAt)
	record.RevokedAt = copyTime(record.RevokedAt)
	return record
}

func (s *memoryStore) CreateTeamInvitation(_ context.Context, record TeamInvitationRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.teams[record.TeamID]; !ok {
		return fmt.Errorf("团队不存在")
	}
	if _, ok := s.users[record.CreatedBy]; !ok {
		return fmt.Errorf("创建用户不存在")
	}
	for _, invitation := range s.invitations {
		if invitation.TokenHash == record.TokenHash {
			return fmt.Errorf("邀请凭据已存在")
		}
	}
	if record.ID == "" {
		record.ID = uuid.NewString()
	}
	if _, ok := s.invitations[record.ID]; ok {
		return fmt.Errorf("邀请已存在")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now()
	}
	record.Email = strings.ToLower(strings.TrimSpace(record.Email))
	s.invitations[record.ID] = cloneInvitation(record)
	return nil
}

func (s *memoryStore) ListTeamInvitations(_ context.Context, teamID string) ([]TeamInvitationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []TeamInvitationRecord{}
	for _, invitation := range s.invitations {
		if invitation.TeamID == teamID {
			out = append(out, cloneInvitation(invitation))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *memoryStore) RevokeTeamInvitation(_ context.Context, teamID, id string, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.invitations[id]
	if !ok || record.TeamID != teamID || record.AcceptedAt != nil || record.RevokedAt != nil {
		return false, nil
	}
	record.RevokedAt = copyTime(&at)
	s.invitations[id] = record
	return true, nil
}

func (s *memoryStore) AcceptTeamInvitation(_ context.Context, tokenHash, userID string, at time.Time) (*TeamInvitationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var record TeamInvitationRecord
	found := false
	for _, invitation := range s.invitations {
		if invitation.TokenHash == tokenHash {
			record = invitation
			found = true
			break
		}
	}
	if !found || record.AcceptedAt != nil || record.RevokedAt != nil || !record.ExpiresAt.After(at) {
		return nil, ErrInvitationInvalid
	}
	user, ok := s.users[userID]
	team, teamOK := s.teams[record.TeamID]
	if !ok || !teamOK || team.DisabledAt != nil || user.DisabledAt != nil {
		return nil, ErrInvitationInvalid
	}
	if !strings.EqualFold(user.Email, record.Email) {
		return nil, ErrInvitationEmailMismatch
	}
	for otherID, members := range s.members {
		if otherID != record.TeamID {
			if _, exists := members[userID]; exists {
				return nil, ErrUserAlreadyInTeam
			}
		}
	}
	// 已有成员不能消费新邀请，避免邀请流程改变管理身份或绕过成员停用。
	if _, exists := s.members[record.TeamID][userID]; exists {
		return nil, ErrInvitationInvalid
	}
	if err := s.addMemberLocked(record.TeamID, userID, MemberRoleMember); err != nil {
		return nil, err
	}
	record.AcceptedAt = copyTime(&at)
	s.invitations[record.ID] = record
	copy := cloneInvitation(record)
	return &copy, nil
}

func (s *memoryStore) reservationBudgetAllowedLocked(input ReserveQuotaInput, at time.Time) (ReserveResult, error) {
	var memberBudget string
	if input.UserID != "" {
		member := s.memberViewLocked(input.TeamID, input.UserID)
		if member == nil || member.MemberDisabledAt != nil || member.User.DisabledAt != nil {
			return ReserveDeniedInactive, nil
		}
		memberBudget = member.MonthlyBudgetCredits
	}
	policy := s.teamPolicies[input.TeamID]
	if memberBudget == "" && policy.DailyBudgetCredits == "" && policy.MonthlyBudgetCredits == "" {
		if input.Micro >= 0 {
			return ReserveReserved, nil
		}
		return ReserveDeniedBudget, nil
	}
	day, endDay, month, endMonth := budgetWindow(at)
	var daily, monthly, personal int64
	for _, usage := range s.usage {
		if usage.TeamID != input.TeamID {
			continue
		}
		if !usage.CreatedAt.Before(day) && usage.CreatedAt.Before(endDay) {
			daily = addBudgetSpent(daily, usage.CreditsMicro)
		}
		if !usage.CreatedAt.Before(month) && usage.CreatedAt.Before(endMonth) {
			monthly = addBudgetSpent(monthly, usage.CreditsMicro)
			if usage.UserID == input.UserID {
				personal = addBudgetSpent(personal, usage.CreditsMicro)
			}
		}
	}
	for _, reservation := range s.reservations {
		if input.Postpaid || reservation.TeamID != input.TeamID || reservation.Status != ReservationReserved {
			continue
		}
		// 跨上海日/月界的在途请求可能在新周期结算，活动预留必须继续占用新周期预算。
		daily = addBudgetSpent(daily, reservation.ReservedMicro)
		monthly = addBudgetSpent(monthly, reservation.ReservedMicro)
		if reservation.UserID == input.UserID {
			personal = addBudgetSpent(personal, reservation.ReservedMicro)
		}
	}
	for _, check := range []struct {
		limit string
		spent int64
	}{{policy.DailyBudgetCredits, daily}, {policy.MonthlyBudgetCredits, monthly}, {memberBudget, personal}} {
		allowBudget := budgetAllows
		if input.Postpaid {
			allowBudget = postpaidBudgetAllows
		}
		allowed, err := allowBudget(check.limit, check.spent, input.Micro)
		if err != nil {
			return "", err
		}
		if !allowed {
			return ReserveDeniedBudget, nil
		}
	}
	return ReserveReserved, nil
}

func (s *memoryStore) FindTeamInvitationByHash(_ context.Context, tokenHash string) (*TeamInvitationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.invitations {
		if record.TokenHash == tokenHash {
			copy := cloneInvitation(record)
			return &copy, nil
		}
	}
	return nil, nil
}
