package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

var ErrConflict = errors.New("media_version_conflict")
var ErrInvalid = errors.New("media_invalid_input")
var ErrCanvasToolPolicyDenied = errors.New("canvas_tool_policy_denied")
var ErrCanvasMediaQuoteLimit = errors.New("canvas_media_quote_exceeds_limit")

type Repository struct{ Store store.PlatformStore }

func ProjectKey(team, id string) string { return "media:project:" + team + ":" + id }
func assetKey(team string) string       { return "media:assets:" + team }
func (r Repository) Models(ctx context.Context) ([]Model, error) {
	models, _, err := r.ModelCatalog(ctx)
	return models, err
}

// ModelCatalog 从同一次读取派生控制台展示内容与 CAS 基线，避免显示值和版本属于两次不同的写入。
func (r Repository) ModelCatalog(ctx context.Context) ([]Model, []byte, error) {
	raw, err := r.Store.GetConfigValue(ctx, ConfigKey)
	if err != nil {
		return nil, nil, err
	}
	models := []Model{}
	if len(raw) > 0 {
		err = json.Unmarshal(raw, &models)
	}
	for index := range models {
		models[index] = NormalizeSeedanceDuration(models[index])
	}
	return models, raw, err
}
func (r Repository) Get(ctx context.Context, team, id string) (*Project, error) {
	p, _, err := r.readProject(ctx, team, id)
	return p, err
}

func (r Repository) readProject(ctx context.Context, team, id string) (*Project, []byte, error) {
	raw, err := r.Store.GetConfigValue(ctx, ProjectKey(team, id))
	if err != nil || len(raw) == 0 {
		return nil, nil, err
	}
	var p Project
	err = json.Unmarshal(raw, &p)
	return &p, raw, err
}

func (r Repository) Save(ctx context.Context, p *Project, expected []byte) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if expected != nil {
		raw, err = preserveProjectSnapshotFields(expected, raw)
		if err != nil {
			return err
		}
	}
	// 旧项目缺少后来新增的零值字段，按现版结构重编码会产生假冲突。比较本次读到的快照。
	ok, err := r.Store.CompareAndSwapConfigValue(ctx, ProjectKey(p.TeamID, p.ID), expected, raw)
	if err != nil {
		return err
	}
	if !ok {
		return ErrConflict
	}
	return nil
}
func updateJSON[T any](ctx context.Context, s store.PlatformStore, key string, empty T, mutate func(*T) error) error {
	for range 20 {
		raw, err := s.GetConfigValue(ctx, key)
		if err != nil {
			return err
		}
		value := empty
		if len(raw) > 0 {
			if err = json.Unmarshal(raw, &value); err != nil {
				return err
			}
		}
		if err = mutate(&value); err != nil {
			return err
		}
		out, err := json.Marshal(value)
		if err != nil {
			return err
		}
		ok, err := s.CompareAndSwapConfigValue(ctx, key, raw, out)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return ErrConflict
}
func (r Repository) Index(ctx context.Context, key string) error {
	return updateJSON(ctx, r.Store, IndexKey, []string{}, func(v *[]string) error {
		for _, item := range *v {
			if item == key {
				return nil
			}
		}
		*v = append(*v, key)
		return nil
	})
}
func (r Repository) All(ctx context.Context) ([]Project, error) {
	raw, err := r.Store.GetConfigValue(ctx, IndexKey)
	if err != nil {
		return nil, err
	}
	keys := []string{}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &keys); err != nil {
			return nil, err
		}
	}
	out := []Project{}
	for _, key := range keys {
		data, e := r.Store.GetConfigValue(ctx, key)
		if e != nil {
			return nil, e
		}
		if len(data) == 0 {
			continue
		}
		var p Project
		if e = json.Unmarshal(data, &p); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, nil
}
func (r Repository) Assets(ctx context.Context, team string) ([]Asset, error) {
	records, err := r.assetRecords(ctx, team)
	if err != nil {
		return nil, err
	}
	out := []Asset{}
	for _, record := range records {
		if record.DeletedAt == "" {
			out = append(out, record.Asset)
		}
	}
	return out, nil
}
func (r Repository) AddAsset(ctx context.Context, team string, a Asset) error {
	return r.addOwnedAsset(ctx, team, a, AssetActor{})
}
func (r Repository) addOwnedAsset(ctx context.Context, team string, a Asset, actor AssetActor) error {
	return updateJSON(ctx, r.Store, assetKey(team), []assetRecord{}, func(v *[]assetRecord) error {
		for _, item := range *v {
			if item.ID == a.ID {
				return nil
			}
		}
		if len(*v) >= 10000 {
			return fmt.Errorf("asset_limit")
		}
		*v = append(*v, assetRecord{Asset: a, UploaderID: actor.UserID, UploaderName: actor.Name})
		return nil
	})
}

func (r Repository) RemoveAsset(ctx context.Context, team, id string) error {
	return updateJSON(ctx, r.Store, assetKey(team), []assetRecord{}, func(v *[]assetRecord) error {
		kept := (*v)[:0]
		for _, item := range *v {
			if item.ID != id || item.DeletedAt != "" {
				kept = append(kept, item)
			}
		}
		*v = kept
		return nil
	})
}

func (r Repository) UpsertInternalAsset(ctx context.Context, team string, asset Asset) error {
	if !asset.Internal || asset.ID == "" || asset.Kind != "video" {
		return ErrInvalid
	}
	return updateJSON(ctx, r.Store, assetKey(team), []assetRecord{}, func(assets *[]assetRecord) error {
		for i := range *assets {
			if (*assets)[i].ID == asset.ID {
				if !(*assets)[i].Internal {
					return ErrConflict
				}
				(*assets)[i].Asset = asset
				return nil
			}
		}
		if len(*assets) >= 10000 {
			return fmt.Errorf("asset_limit")
		}
		*assets = append(*assets, assetRecord{Asset: asset})
		return nil
	})
}
