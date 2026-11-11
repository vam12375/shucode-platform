package media

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/contracts"
)

var (
	ErrAssetForbidden   = errors.New("media_asset_forbidden")
	ErrAssetNotFound    = errors.New("media_asset_not_found")
	ErrAssetNameInvalid = errors.New("media_asset_name_invalid")
	ErrAssetInUse       = errors.New("media_asset_in_use")
	ErrAssetConflict    = errors.New("media_asset_conflict")
)

type AssetActor struct {
	UserID string
	Name   string
	Admin  bool
}
type assetDeletionRecord struct {
	contracts.MediaAssetDeletionRequest
	RequesterID string `json:"requesterId"`
	ReviewerID  string `json:"reviewerId,omitempty"`
}

// 管理事实与原目录共用同一个 CAS 值；公共 Asset 不暴露内部身份或墓碑。
type assetRecord struct {
	Asset
	UploaderID       string                `json:"uploaderId,omitempty"`
	UploaderName     string                `json:"uploaderName,omitempty"`
	DeletedAt        string                `json:"deletedAt,omitempty"`
	DeletionRequests []assetDeletionRecord `json:"deletionRequests,omitempty"`
}

func (a assetRecord) editable(actor AssetActor) bool {
	return !a.Internal && a.UploaderID != "" && actor.UserID != "" && (a.UploaderID == actor.UserID || actor.Admin)
}
func (a assetRecord) public(actor AssetActor) Asset {
	out := a.Asset
	management := &contracts.MediaAssetManagement{CanRename: a.editable(actor), CanRequestDeletion: a.editable(actor) && a.UploaderID == actor.UserID, UploaderName: a.UploaderName}
	for _, request := range a.DeletionRequests {
		if request.Status == "pending" {
			management.PendingDeletionID = request.ID
			management.CanRequestDeletion = false
			break
		}
	}
	out.Management = management
	return out
}
func (r Repository) assetRecords(ctx context.Context, team string) ([]assetRecord, error) {
	records := []assetRecord{}
	raw, err := r.Store.GetConfigValue(ctx, assetKey(team))
	if err != nil {
		return nil, err
	}
	if len(raw) > 0 {
		err = json.Unmarshal(raw, &records)
	}
	return records, err
}
func (r Repository) AssetsForPlayback(ctx context.Context, team string) ([]Asset, error) {
	records, err := r.assetRecords(ctx, team)
	if err != nil {
		return nil, err
	}
	assets := make([]Asset, 0, len(records))
	for _, a := range records {
		assets = append(assets, a.Asset)
	}
	return assets, nil
}
func (r Repository) MediaLibrary(ctx context.Context, team string, actor AssetActor) ([]Asset, contracts.MediaLibrary, error) {
	records, err := r.assetRecords(ctx, team)
	library := contracts.MediaLibrary{CanReview: actor.Admin, DeletionRequests: []contracts.MediaAssetDeletionRequest{}}
	assets := []Asset{}
	if err != nil {
		return nil, library, err
	}
	for _, a := range records {
		if a.Internal {
			continue
		}
		if a.DeletedAt == "" {
			assets = append(assets, a.public(actor))
		}
		for _, request := range a.DeletionRequests {
			if actor.Admin || request.RequesterID == actor.UserID {
				library.DeletionRequests = append(library.DeletionRequests, request.MediaAssetDeletionRequest)
			}
		}
	}
	return assets, library, nil
}
func (r Repository) mutateAsset(ctx context.Context, team, id string, mutate func(*assetRecord) error) error {
	err := updateJSON(ctx, r.Store, assetKey(team), []assetRecord{}, func(records *[]assetRecord) error {
		for i := range *records {
			if (*records)[i].ID == id && (*records)[i].DeletedAt == "" && !(*records)[i].Internal {
				return mutate(&(*records)[i])
			}
		}
		return ErrAssetNotFound
	})
	if errors.Is(err, ErrConflict) {
		return ErrAssetConflict
	}
	return err
}
func (r Repository) RenameAsset(ctx context.Context, team, id, name string, actor AssetActor) (Asset, error) {
	name, ok := contracts.NormalizeMediaAssetName(name)
	if !ok {
		return Asset{}, ErrAssetNameInvalid
	}
	var result Asset
	err := r.mutateAsset(ctx, team, id, func(a *assetRecord) error {
		if !a.editable(actor) {
			return ErrAssetForbidden
		}
		a.Name = name
		result = a.public(actor)
		return nil
	})
	return result, err
}
func (r Repository) RequestAssetDeletion(ctx context.Context, team, id string, actor AssetActor) (contracts.MediaAssetDeletionRequest, error) {
	var result contracts.MediaAssetDeletionRequest
	err := r.mutateAsset(ctx, team, id, func(a *assetRecord) error {
		if !a.editable(actor) || a.UploaderID != actor.UserID {
			return ErrAssetForbidden
		}
		for _, request := range a.DeletionRequests {
			if request.Status == "pending" {
				result = request.MediaAssetDeletionRequest
				return nil
			}
		}
		name := strings.TrimSpace(actor.Name)
		if name == "" {
			name = "Team member"
		}
		result = contracts.MediaAssetDeletionRequest{ID: uuid.NewString(), AssetID: a.ID, AssetName: a.Name, RequesterName: name, Status: "pending", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		a.DeletionRequests = append(a.DeletionRequests, assetDeletionRecord{MediaAssetDeletionRequest: result, RequesterID: actor.UserID})
		return nil
	})
	return result, err
}
func (r Repository) ReviewAssetDeletion(ctx context.Context, team, id, decision string, actor AssetActor) (contracts.MediaAssetDeletionRequest, error) {
	var result contracts.MediaAssetDeletionRequest
	if !actor.Admin || actor.UserID == "" {
		return result, ErrAssetForbidden
	}
	status := "approved"
	if decision == "reject" {
		status = "rejected"
	} else if decision != "approve" {
		return result, ErrInvalid
	}
	err := updateJSON(ctx, r.Store, assetKey(team), []assetRecord{}, func(records *[]assetRecord) error {
		for i := range *records {
			a := &(*records)[i]
			for j := range a.DeletionRequests {
				request := &a.DeletionRequests[j]
				if request.ID != id {
					continue
				}
				if request.Status != "pending" {
					if request.Status != status {
						return ErrAssetConflict
					}
					result = request.MediaAssetDeletionRequest
					return nil
				}
				if status == "approved" {
					inUse, err := r.assetInUse(ctx, team, a.ID)
					if err != nil {
						return err
					}
					if inUse {
						return ErrAssetInUse
					}
				}
				request.Status = status
				request.ReviewedAt = time.Now().UTC().Format(time.RFC3339Nano)
				request.ReviewerID = actor.UserID
				if status == "approved" {
					a.DeletedAt = request.ReviewedAt
				}
				result = request.MediaAssetDeletionRequest
				return nil
			}
		}
		return ErrAssetNotFound
	})
	if errors.Is(err, ErrConflict) {
		err = ErrAssetConflict
	}
	return result, err
}
func (r Repository) assetInUse(ctx context.Context, team, id string) (bool, error) {
	projects, err := r.All(ctx)
	if err != nil {
		return false, err
	}
	for _, p := range projects {
		if p.TeamID != team {
			continue
		}
		switch p.Status {
		case "completed", "failed", "cancelled", "deleted", "expired":
			continue
		}
		// 同时检查画布、制作单、音频、参考片和品牌 logo；新增 *AssetId 字段自动进入在用保护。
		raw, err := json.Marshal(p)
		if err != nil {
			return false, err
		}
		var value any
		if err = json.Unmarshal(raw, &value); err != nil {
			return false, err
		}
		if referencesAsset(value, "", id) {
			return true, nil
		}
	}
	return false, nil
}
func referencesAsset(value any, key, id string) bool {
	switch v := value.(type) {
	case string:
		lower := strings.ToLower(key)
		return (strings.HasSuffix(lower, "assetid") || strings.HasSuffix(lower, "assetids")) && v == id
	case []any:
		for _, item := range v {
			if referencesAsset(item, key, id) {
				return true
			}
		}
	case map[string]any:
		for child, item := range v {
			if referencesAsset(item, child, id) {
				return true
			}
		}
	}
	return false
}
