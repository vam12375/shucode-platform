package webtools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func newConfigTestStore(t *testing.T) store.PlatformStore {
	t.Helper()
	s, err := store.NewMemoryStore(context.Background(), store.MemoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newConfigTestBox(t *testing.T) *crypto.SecretBox {
	t.Helper()
	box, err := crypto.NewSecretBox(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestConfigParseRequiresCompleteSafeIntegerDocument(t *testing.T) {
	config := Defaults()
	config.Version = "version-1"
	config.ProviderID = uuid.NewString()
	config.Enabled = true
	config.SearchSaleMicro = 0
	config.FetchSaleMicro = MaxPriceMicro
	raw, _ := json.Marshal(config)
	parsed, err := Parse(raw)
	if err != nil || parsed != config {
		t.Fatalf("valid configuration: %+v %v", parsed, err)
	}

	var original map[string]json.RawMessage
	_ = json.Unmarshal(raw, &original)
	for _, tc := range []struct {
		name  string
		value json.RawMessage
	}{
		{"enabled", []byte("null")}, {"searchEnabled", []byte("1")}, {"fetchEnabled", []byte("\"true\"")},
		{"providerId", []byte("\"not-a-uuid\"")}, {"providerId", []byte("\"\"")},
		{"version", []byte("\" \"")}, {"version", []byte("null")},
		{"searchSaleMicro", []byte("-1")}, {"searchCostMicro", []byte("0.5")},
		{"fetchSaleMicro", []byte("9007199254740992")}, {"fetchCostMicro", []byte("\"5000\"")},
	} {
		fields := map[string]json.RawMessage{}
		for key, value := range original {
			fields[key] = value
		}
		fields[tc.name] = tc.value
		invalid, _ := json.Marshal(fields)
		if _, err := Parse(invalid); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted %s=%s: %v", tc.name, tc.value, err)
		}
	}
	for _, field := range []string{"version", "enabled", "providerId", "searchEnabled", "fetchEnabled", "searchSaleMicro", "searchCostMicro", "fetchSaleMicro", "fetchCostMicro"} {
		fields := map[string]json.RawMessage{}
		for key, value := range original {
			if key != field {
				fields[key] = value
			}
		}
		invalid, _ := json.Marshal(fields)
		if _, err := Parse(invalid); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted missing %s", field)
		}
	}
	original["apiKey"] = []byte("\"forbidden\"")
	invalid, _ := json.Marshal(original)
	if _, err := Parse(invalid); !errors.Is(err, ErrInvalid) {
		t.Fatal("accepted secret or unknown field")
	}
	if _, err := Parse(append(raw, []byte(" {}")...)); !errors.Is(err, ErrInvalid) {
		t.Fatal("accepted trailing JSON")
	}
}

func TestConfigSaveUsesVersionedCASAndKeepsDisabledTombstone(t *testing.T) {
	ctx := context.Background()
	s := newConfigTestStore(t)
	initial, found, err := Load(ctx, s)
	if err != nil || found || initial.Version != MissingVersion {
		t.Fatalf("missing configuration: %+v %v %v", initial, found, err)
	}
	first, err := Save(ctx, s, MissingVersion, initial)
	if err != nil || first.Version == MissingVersion {
		t.Fatalf("first save: %+v %v", first, err)
	}
	if _, err := Save(ctx, s, MissingVersion, initial); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale initial version: %v", err)
	}
	var winners atomic.Int32
	var group sync.WaitGroup
	for i := 0; i < 24; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := Save(ctx, s, first.Version, first)
			if err == nil {
				winners.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("concurrent save: %v", err)
			}
		}()
	}
	group.Wait()
	if winners.Load() != 1 {
		t.Fatalf("CAS winners=%d", winners.Load())
	}
	current, _, _ := Load(ctx, s)
	cleared, err := Save(ctx, s, current.Version, Defaults())
	if err != nil {
		t.Fatal(err)
	}
	stored, found, err := Load(ctx, s)
	if err != nil || !found || stored.Enabled || stored.ProviderID != "" || stored.Version != cleared.Version {
		t.Fatalf("explicit disabled tombstone lost: %+v %v %v", stored, found, err)
	}
	raw, _ := s.GetConfigValue(ctx, ConfigKey)
	if len(raw) == 0 {
		t.Fatal("clear must not remove persisted configuration")
	}
	if _, err := Resolve(ctx, s, newConfigTestBox(t), stored); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("disabled configuration became available: %v", err)
	}
}

func TestConfigResolveBindsOnlySelectedEncryptedProvider(t *testing.T) {
	ctx := context.Background()
	s := newConfigTestStore(t)
	box := newConfigTestBox(t)
	ciphertext, _ := box.Encrypt("sk-web-tools-private")
	timeout := int64(1234)
	provider, err := s.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
		Name: "Explicit tools", BaseURL: "https://api.tools.example.test/v1/", APIKeyCiphertext: ciphertext, TimeoutMs: &timeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	config := Defaults()
	config.Enabled, config.ProviderID = true, provider.ID
	snapshot, err := Resolve(ctx, s, box, config)
	if err != nil || snapshot.APIKey != "sk-web-tools-private" || snapshot.BaseURL != "https://api.tools.example.test" || snapshot.TimeoutMs != timeout {
		t.Fatalf("selected provider snapshot: %+v %v", snapshot.Config, err)
	}
	serialized, _ := json.Marshal(snapshot)
	if strings.Contains(string(serialized), "sk-web-tools-private") || strings.Contains(string(serialized), ciphertext) {
		t.Fatal("snapshot JSON exposed a key")
	}
	replacement, _ := box.Encrypt("sk-replacement")
	_, _ = s.UpdateUpstreamProvider(ctx, provider.ID, store.UpdateUpstreamProviderPatch{ProvideAPIKey: true, APIKeyCiphertext: replacement})
	next, err := Resolve(ctx, s, box, config)
	if err != nil || next.APIKey != "sk-replacement" || snapshot.APIKey != "sk-web-tools-private" {
		t.Fatal("in-flight snapshot changed with provider key")
	}
	disabled := false
	_, _ = s.UpdateUpstreamProvider(ctx, provider.ID, store.UpdateUpstreamProviderPatch{Enabled: &disabled})
	if _, err := Resolve(ctx, s, box, config); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("disabled provider: %v", err)
	}
	enabled := true
	_, _ = s.UpdateUpstreamProvider(ctx, provider.ID, store.UpdateUpstreamProviderPatch{Enabled: &enabled, ProvideAPIKey: true, APIKeyCiphertext: "invalid-ciphertext"})
	if _, err := Resolve(ctx, s, box, config); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("corrupt credential: %v", err)
	}
	if _, err := Resolve(ctx, s, nil, config); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing platform key: %v", err)
	}
	config.ProviderID = uuid.NewString()
	if _, err := Resolve(ctx, s, box, config); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing selected provider: %v", err)
	}
}

func TestConfigResolveRejectsInvalidCredentialAndServiceRoots(t *testing.T) {
	ctx := context.Background()
	s := newConfigTestStore(t)
	box := newConfigTestBox(t)
	for _, key := range []string{"", " ", "key with spaces", "key\nheader", "密钥"} {
		ciphertext, _ := box.Encrypt(key)
		provider, err := s.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{Name: uuid.NewString(), BaseURL: "https://api.tools.example.test", APIKeyCiphertext: ciphertext})
		if err != nil {
			t.Fatal(err)
		}
		config := Defaults()
		config.Enabled, config.ProviderID = true, provider.ID
		if _, err := Resolve(ctx, s, box, config); !errors.Is(err, ErrUnavailable) {
			t.Errorf("accepted invalid credential: %v", err)
		}
	}
	for _, base := range []string{"http://api.example.test", "https://user:pass@api.example.test", "https://api.example.test?token=private", "https://api.example.test/#fragment", "https:///missing"} {
		if _, err := NormalizeBaseURL(base); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted base URL %q: %v", base, err)
		}
	}
	for raw, expected := range map[string]string{
		"https://api.example.test/v1/":          "https://api.example.test",
		" https://api.example.test/prefix/v1/ ": "https://api.example.test/prefix",
		"https://api.example.test":              "https://api.example.test",
	} {
		normalized, err := NormalizeBaseURL(raw)
		if err != nil || normalized != expected {
			t.Errorf("base URL %q = %q %v", raw, normalized, err)
		}
	}
}
