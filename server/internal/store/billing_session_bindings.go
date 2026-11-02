package store

import (
	"strings"
	"time"
)

type BillingSessionBindingInput struct {
	SessionID         string
	WorkspaceIdentity string
	RemoteSessionID   string
	ParentSessionID   string
	ActorUserID       string
	DeviceTokenID     string
}

type BillingSessionBinding struct {
	ID                 string
	SessionID          string
	WorkspaceIdentity  string
	RemoteSessionID    string
	ParentSessionID    string
	BillingAccountID   string
	BillingAccountType string
	BillingAccountName string
	ActorUserID        string
	DeviceTokenID      string
	CreatedAt          time.Time
}

func normalizeBillingBindingInput(input BillingSessionBindingInput) (BillingSessionBindingInput, error) {
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.WorkspaceIdentity = strings.TrimSpace(input.WorkspaceIdentity)
	input.RemoteSessionID = strings.TrimSpace(input.RemoteSessionID)
	input.ParentSessionID = strings.TrimSpace(input.ParentSessionID)
	if input.SessionID == "" || len(input.SessionID) > 128 || input.WorkspaceIdentity == "" || len(input.WorkspaceIdentity) > 2048 || len(input.RemoteSessionID) > 128 || len(input.ParentSessionID) > 128 || input.ParentSessionID == input.SessionID || !canonicalUUID(input.ActorUserID) || !canonicalUUID(input.DeviceTokenID) {
		return input, ErrInvalidInput
	}
	return input, nil
}

func cloneBillingBinding(binding BillingSessionBinding) *BillingSessionBinding { return &binding }
