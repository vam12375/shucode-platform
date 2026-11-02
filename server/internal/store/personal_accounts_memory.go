package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

func personalTeamName(accountID string) string { return "__personal__:" + accountID }

func (s *memoryStore) EnsurePersonalAccount(_ context.Context, userID string) (*TeamRecord, error) {
	if !canonicalUUID(userID) {
		return nil, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.personalAccounts[userID]; ok {
		team := s.teams[id]
		return cloneTeam(&team), nil
	}
	if _, ok := s.users[userID]; !ok {
		return nil, ErrInvalidInput
	}
	id := uuid.NewString()
	// 使用账本自身的随机身份命名，避免普通团队占用可预测的用户名称后阻断个人开户。
	team := TeamRecord{ID: id, Name: personalTeamName(id), QuotaCredits: "0.000000", UsedCredits: "0.000000", ReservedCredits: "0.000000", CreatedAt: time.Now().UTC()}
	s.teams[id] = team
	s.members[id] = map[string]struct{}{}
	s.personalAccounts[userID] = id
	s.billingModes[userID] = "team"
	return cloneTeam(&team), nil
}

func (s *memoryStore) GetPersonalAccount(_ context.Context, userID string) (*TeamRecord, error) {
	if !canonicalUUID(userID) {
		return nil, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.personalAccounts[userID]
	if !ok {
		return nil, nil
	}
	team, ok := s.teams[id]
	if !ok {
		return nil, nil
	}
	return cloneTeam(&team), nil
}

func (s *memoryStore) GetBillingMode(_ context.Context, userID string) (string, error) {
	if !canonicalUUID(userID) {
		return "", ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mode := s.billingModes[userID]
	if mode == "" {
		return "team", nil
	}
	return mode, nil
}

func (s *memoryStore) SetBillingMode(ctx context.Context, userID, mode string) error {
	if mode != "team" && mode != "personal" {
		return ErrBillingModeInvalid
	}
	if !canonicalUUID(userID) {
		return ErrInvalidInput
	}
	if mode == "personal" {
		if _, err := s.EnsurePersonalAccount(ctx, userID); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[userID]; !ok {
		return fmt.Errorf("%w: user", ErrInvalidInput)
	}
	s.billingModes[userID] = mode
	return nil
}

func (s *memoryStore) personalOwnerLocked(teamID string) string {
	for owner, id := range s.personalAccounts {
		if id == teamID {
			return owner
		}
	}
	return ""
}

func (s *memoryStore) IsPersonalAccountTeam(_ context.Context, teamID string) (bool, error) {
	if !canonicalUUID(teamID) {
		return false, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.personalOwnerLocked(teamID) != "", nil
}
