package store

import (
	"context"
	"github.com/google/uuid"
	"time"
)

func (s *memoryStore) CreateBillingSessionBinding(_ context.Context, raw BillingSessionBindingInput) (*BillingSessionBinding, error) {
	input, err := normalizeBillingBindingInput(raw)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if user, ok := s.users[input.ActorUserID]; !ok || user.DisabledAt != nil {
		return nil, ErrBillingBindingForbidden
	}
	if token, ok := s.deviceTokens[input.DeviceTokenID]; !ok || token.UserID != input.ActorUserID || token.RevokedAt != nil {
		return nil, ErrBillingBindingForbidden
	}
	// 登录会轮换设备 token；账本仍归原 actor，设备授权独立登记，避免覆盖在途请求身份。
	var existing *BillingSessionBinding
	for _, b := range s.billingBindings {
		if b.SessionID == input.SessionID && b.WorkspaceIdentity == input.WorkspaceIdentity && b.RemoteSessionID == input.RemoteSessionID {
			copy := b
			existing = &copy
			break
		}
	}
	if existing != nil {
		if err := validateBillingBindingReplay(existing, input); err != nil {
			return nil, err
		}
		s.billingBindingDevices[billingBindingDeviceKey{existing.ID, input.DeviceTokenID}] = struct{}{}
		return cloneBillingBinding(*existing), nil
	}
	var account *TeamRecord
	if input.ParentSessionID != "" {
		for _, b := range s.billingBindings {
			if b.SessionID == input.ParentSessionID && b.WorkspaceIdentity == input.WorkspaceIdentity && b.RemoteSessionID == input.RemoteSessionID {
				if b.ActorUserID != input.ActorUserID {
					return nil, ErrBillingBindingActorMismatch
				}
				team, ok := s.teams[b.BillingAccountID]
				if ok {
					copy := team
					account = &copy
				}
				break
			}
		}
		if account == nil {
			return nil, ErrBillingBindingNotFound
		}
	} else {
		mode := s.billingModes[input.ActorUserID]
		if mode == "personal" {
			id := s.personalAccounts[input.ActorUserID]
			if id == "" {
				return nil, ErrBillingBindingAccountUnavailable
			}
			a, ok := s.teams[id]
			if !ok || a.DisabledAt != nil {
				return nil, ErrBillingBindingAccountUnavailable
			}
			account = &a
		} else {
			var selected *TeamRecord
			for teamID, members := range s.members {
				if _, ok := members[input.ActorUserID]; !ok {
					continue
				}
				team := s.teams[teamID]
				if team.DisabledAt != nil || s.memberDisabled[memberKey(teamID, input.ActorUserID)] != nil {
					continue
				}
				if selected != nil {
					return nil, ErrAmbiguousTeam
				}
				copy := team
				selected = &copy
			}
			if selected == nil {
				return nil, ErrBillingBindingNoActiveTeam
			}
			account = selected
		}
	}
	now := time.Now().UTC()
	b := BillingSessionBinding{ID: uuid.NewString(), SessionID: input.SessionID, WorkspaceIdentity: input.WorkspaceIdentity, RemoteSessionID: input.RemoteSessionID, ParentSessionID: input.ParentSessionID, BillingAccountID: account.ID, BillingAccountType: "team", BillingAccountName: account.Name, ActorUserID: input.ActorUserID, DeviceTokenID: input.DeviceTokenID, CreatedAt: now}
	if owner := s.personalOwnerLocked(account.ID); owner != "" {
		b.BillingAccountType = "personal"
	}
	s.billingBindings[b.ID] = b
	s.billingBindingDevices[billingBindingDeviceKey{b.ID, input.DeviceTokenID}] = struct{}{}
	return cloneBillingBinding(b), nil
}
func (s *memoryStore) ResolveBillingSessionBinding(_ context.Context, id, userID, deviceID string) (*BillingSessionBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resolveBillingSessionBindingLocked(id, userID, deviceID)
}

func (s *memoryStore) resolveBillingSessionBindingLocked(id, userID, deviceID string) (*BillingSessionBinding, error) {
	if !canonicalUUID(id) || !canonicalUUID(userID) || !canonicalUUID(deviceID) {
		return nil, ErrBillingBindingForbidden
	}
	b, ok := s.billingBindings[id]
	if !ok {
		return nil, ErrBillingBindingNotFound
	}
	if b.ActorUserID != userID {
		return nil, ErrBillingBindingActorMismatch
	}
	if _, ok := s.billingBindingDevices[billingBindingDeviceKey{id, deviceID}]; !ok {
		return nil, ErrBillingBindingDeviceMismatch
	}
	if token, ok := s.deviceTokens[deviceID]; !ok || token.UserID != userID || token.RevokedAt != nil {
		return nil, ErrBillingBindingForbidden
	}
	return cloneBillingBinding(b), nil
}
