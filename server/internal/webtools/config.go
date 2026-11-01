// Package webtools owns the platform web tool configuration and provider binding.
package webtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

const (
	ConfigKey            = "gateway:deepseek-web-tools:v1"
	MissingVersion       = "none"
	MaxPriceMicro  int64 = 9_007_199_254_740_991
)

var (
	ErrInvalid     = errors.New("invalid_web_tools_configuration")
	ErrUnavailable = errors.New("web_tools_unavailable")
	ErrConflict    = errors.New("web_tools_configuration_conflict")
)

type Config struct {
	Version         string `json:"version"`
	Enabled         bool   `json:"enabled"`
	ProviderID      string `json:"providerId"`
	SearchEnabled   bool   `json:"searchEnabled"`
	FetchEnabled    bool   `json:"fetchEnabled"`
	SearchSaleMicro int64  `json:"searchSaleMicro"`
	SearchCostMicro int64  `json:"searchCostMicro"`
	FetchSaleMicro  int64  `json:"fetchSaleMicro"`
	FetchCostMicro  int64  `json:"fetchCostMicro"`
}

type Store interface {
	GetConfigValue(context.Context, string) ([]byte, error)
	CompareAndSwapConfigValue(context.Context, string, []byte, []byte) (bool, error)
	GetUpstreamProvider(context.Context, string) (*store.UpstreamProviderRecord, error)
}

// Snapshot is private runtime material; it must never be serialized in responses.
type Snapshot struct {
	Config
	ProviderName string `json:"-"`
	BaseURL      string `json:"-"`
	APIKey       string `json:"-"`
	TimeoutMs    int64  `json:"-"`
}

func Defaults() Config {
	return Config{Version: MissingVersion, SearchEnabled: true, FetchEnabled: true, SearchSaleMicro: 5_000, SearchCostMicro: 5_000, FetchSaleMicro: 5_000, FetchCostMicro: 5_000}
}

func Validate(c Config) error {
	if c.Version == "" || len(c.Version) > 128 || strings.TrimSpace(c.Version) != c.Version {
		return ErrInvalid
	}
	if c.ProviderID != "" {
		if _, err := uuid.Parse(c.ProviderID); err != nil {
			return ErrInvalid
		}
	}
	if c.Enabled && c.ProviderID == "" {
		return ErrInvalid
	}
	for _, price := range []int64{c.SearchSaleMicro, c.SearchCostMicro, c.FetchSaleMicro, c.FetchCostMicro} {
		if price < 0 || price > MaxPriceMicro {
			return ErrInvalid
		}
	}
	return nil
}

func Parse(raw []byte) (Config, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 9 {
		return Config{}, ErrInvalid
	}
	for _, name := range []string{"version", "enabled", "providerId", "searchEnabled", "fetchEnabled", "searchSaleMicro", "searchCostMicro", "fetchSaleMicro", "fetchCostMicro"} {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return Config{}, ErrInvalid
		}
	}
	var c Config
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil || decoder.Decode(new(any)) != io.EOF {
		return Config{}, ErrInvalid
	}
	if err := Validate(c); err != nil {
		return Config{}, err
	}
	return c, nil
}

func Load(ctx context.Context, s Store) (Config, bool, error) {
	raw, err := s.GetConfigValue(ctx, ConfigKey)
	if err != nil {
		return Config{}, false, err
	}
	if len(raw) == 0 {
		return Defaults(), false, nil
	}
	c, err := Parse(raw)
	return c, true, err
}

// Save changes the whole document under its expected version.
func Save(ctx context.Context, s Store, expectedVersion string, c Config) (Config, error) {
	if expectedVersion == "" {
		return Config{}, ErrInvalid
	}
	raw, err := s.GetConfigValue(ctx, ConfigKey)
	if err != nil {
		return Config{}, err
	}
	current := Defaults()
	if len(raw) > 0 {
		current, err = Parse(raw)
		if err != nil {
			return Config{}, err
		}
	}
	if current.Version != expectedVersion {
		return Config{}, ErrConflict
	}
	c.Version = uuid.NewString()
	if err := Validate(c); err != nil {
		return Config{}, err
	}
	next, err := json.Marshal(c)
	if err != nil {
		return Config{}, ErrInvalid
	}
	swapped, err := s.CompareAndSwapConfigValue(ctx, ConfigKey, raw, next)
	if err != nil {
		return Config{}, err
	}
	if !swapped {
		return Config{}, ErrConflict
	}
	return c, nil
}

func NormalizeBaseURL(raw string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", ErrInvalid
	}
	if strings.HasSuffix(strings.ToLower(u.Path), "/v1") {
		u.Path = u.Path[:len(u.Path)-3]
		u.RawPath = ""
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func Resolve(ctx context.Context, s Store, box *crypto.SecretBox, c Config) (Snapshot, error) {
	if err := Validate(c); err != nil {
		return Snapshot{}, err
	}
	if !c.Enabled || box == nil {
		return Snapshot{}, ErrUnavailable
	}
	p, err := s.GetUpstreamProvider(ctx, c.ProviderID)
	if err != nil {
		return Snapshot{}, err
	}
	if p == nil || !p.Enabled || p.APIKeyCiphertext == "" {
		return Snapshot{}, ErrUnavailable
	}
	base, err := NormalizeBaseURL(p.BaseURL)
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	key, err := box.Decrypt(p.APIKeyCiphertext)
	if err != nil || strings.TrimSpace(key) == "" {
		return Snapshot{}, ErrUnavailable
	}
	for _, char := range key {
		if char <= 32 || char > 126 {
			return Snapshot{}, ErrUnavailable
		}
	}
	return Snapshot{Config: c, ProviderName: p.Name, BaseURL: base, APIKey: key, TimeoutMs: p.TimeoutMs}, nil
}
