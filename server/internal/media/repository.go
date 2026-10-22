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

type Repository struct{ Store store.PlatformStore }

func ProjectKey(team, id string) string { return "media:project:" + team + ":" + id }
func assetKey(team string) string       { return "media:assets:" + team }
func (r Repository) Models(ctx context.Context) ([]Model, error) {
	models := []Model{}
	raw, err := r.Store.GetConfigValue(ctx, ConfigKey)
	if err != nil {
		return nil, err
	}
	if len(raw) > 0 {
		err = json.Unmarshal(raw, &models)
	}
	return models, err
}
func (r Repository) Get(ctx context.Context, team, id string) (*Project, error) {
	raw, err := r.Store.GetConfigValue(ctx, ProjectKey(team, id))
	if err != nil || len(raw) == 0 {
		return nil, err
	}
	var p Project
	err = json.Unmarshal(raw, &p)
	return &p, err
}
func (r Repository) Save(ctx context.Context, p *Project, previous *Project) error {
	var old []byte
	var err error
	if previous != nil {
		old, err = json.Marshal(previous)
		if err != nil {
			return err
		}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	ok, err := r.Store.CompareAndSwapConfigValue(ctx, ProjectKey(p.TeamID, p.ID), old, raw)
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
	out := []Asset{}
	raw, err := r.Store.GetConfigValue(ctx, assetKey(team))
	if err != nil {
		return nil, err
	}
	if len(raw) > 0 {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}
func (r Repository) AddAsset(ctx context.Context, team string, a Asset) error {
	return updateJSON(ctx, r.Store, assetKey(team), []Asset{}, func(v *[]Asset) error {
		for _, item := range *v {
			if item.ID == a.ID {
				return nil
			}
		}
		if len(*v) >= 10000 {
			return fmt.Errorf("asset_limit")
		}
		*v = append(*v, a)
		return nil
	})
}

func (r Repository) UpsertInternalAsset(ctx context.Context, team string, asset Asset) error {
	if !asset.Internal || asset.ID == "" || asset.Kind != "video" {
		return ErrInvalid
	}
	return updateJSON(ctx, r.Store, assetKey(team), []Asset{}, func(assets *[]Asset) error {
		for i := range *assets {
			if (*assets)[i].ID == asset.ID {
				if !(*assets)[i].Internal {
					return ErrConflict
				}
				(*assets)[i] = asset
				return nil
			}
		}
		if len(*assets) >= 10000 {
			return fmt.Errorf("asset_limit")
		}
		*assets = append(*assets, asset)
		return nil
	})
}
