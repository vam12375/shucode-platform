package store

import (
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// TeamAssetRecord is the persisted metadata snapshot for one immutable team asset.
type TeamAssetRecord struct {
	ID             string
	Name           string
	Kind           string
	Mime           string
	Size           int64
	CreatedAt      time.Time
	UploadedByID   string
	UploadedByName string
	SHA256         string
	BlobID         string
}

type TeamAssetPage struct {
	Records   []TeamAssetRecord
	UsedBytes int64
}

const TeamAssetMaxBytes int64 = 100 * 1024 * 1024

func validateTeamAssetRecord(teamID string, record TeamAssetRecord, limitBytes int64) error {
	if !canonicalUUID(teamID) || !canonicalUUID(record.ID) || !canonicalUUID(record.UploadedByID) || !canonicalUUID(record.BlobID) ||
		!validTeamAssetText(record.Name, 255) || record.Name == "" || !validTeamAssetText(record.Mime, 255) || record.Mime == "" ||
		!validTeamAssetText(record.UploadedByName, 0) ||
		record.Size < 1 || record.Size > TeamAssetMaxBytes || limitBytes < 1 || !validTeamAssetKind(record.Kind) || !validTeamAssetSHA256(record.SHA256) {
		return ErrInvalidInput
	}
	return nil
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validTeamAssetText(value string, maxRunes int) bool {
	if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	if maxRunes == 0 {
		return true
	}
	runes := utf8.RuneCountInString(value)
	return runes <= maxRunes
}

func validTeamAssetKind(value string) bool {
	switch value {
	case "website", "document", "image", "video", "audio", "code", "other":
		return true
	default:
		return false
	}
}

func validTeamAssetSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
