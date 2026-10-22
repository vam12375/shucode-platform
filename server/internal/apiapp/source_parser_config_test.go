package apiapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

const sourceParserConfigPath = "/console/api/media/source-parser"

type sourceParserConfigTransport struct {
	calls int
}

func (tr *sourceParserConfigTransport) RoundTrip(*http.Request) (*http.Response, error) {
	tr.calls++
	return nil, errors.New("test_source_parser_network_forbidden")
}

func assertSourceParserState(t *testing.T, rec *httptest.ResponseRecorder, source string, environmentConfigured, canConfigure bool) {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("source parser status: %d %s", rec.Code, rec.Body.String())
	}
	data := dataOf(t, rec)
	for key, want := range map[string]any{
		"configured": source != "none", "provider": "snapany", "source": source,
		"environmentConfigured": environmentConfigured, "canConfigure": canConfigure,
		"maxBytes": float64(31457280), "maxSeconds": float64(90),
	} {
		if data[key] != want {
			t.Fatalf("status %s = %v, want %v", key, data[key], want)
		}
	}
	for _, field := range []string{"apiKey", "keyCiphertext", "keyHint", "keyPreview"} {
		if _, exists := data[field]; exists {
			t.Errorf("status exposes secret field %s", field)
		}
	}
}

func TestSourceParserConfigEncryptedLifecycleAndLiveResolution(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	ctx := context.Background()
	const environmentKey = "test-environment-parser-key"
	const consoleKey = "test-console-parser-key"
	const replacementKey = "test-console-parser-key-replacement"
	f.deps.Env.SnapAnyAPIKey = environmentKey
	transport := &sourceParserConfigTransport{}
	f.deps.Media.SourceParser.Client = &http.Client{Transport: transport}

	assertSourceParserState(t, f.do("GET", sourceParserConfigPath, cookie, ""), "environment", true, true)
	currentParser := f.deps.Media.SourceParser
	if currentParser.ResolveKey == nil {
		t.Fatal("media parser has no dynamic configuration resolver")
	}
	assertResolved := func(want string) {
		t.Helper()
		for name, resolver := range map[string]func(context.Context) (string, error){
			"existing service": currentParser.ResolveKey,
			"new service":      NewMediaService(f.deps).SourceParser.ResolveKey,
		} {
			if resolver == nil {
				t.Fatalf("%s has no resolver", name)
			}
			key, err := resolver(ctx)
			if err != nil || key != want {
				t.Fatalf("%s did not observe current configuration: err=%v", name, err)
			}
		}
	}
	assertResolved(environmentKey)
	var ciphertexts []string
	for _, key := range []string{consoleKey, replacementKey} {
		body, err := json.Marshal(map[string]string{"apiKey": "  " + key + "  "})
		if err != nil {
			t.Fatal(err)
		}
		rec := f.do("PUT", sourceParserConfigPath, cookie, string(body))
		assertSourceParserState(t, rec, "console", true, true)
		raw, err := f.store.GetConfigValue(ctx, sourceParserConfigKey)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), key) {
			t.Fatal("source parser key persisted as plaintext")
		}
		var stored storedSourceParserConfig
		if err := json.Unmarshal(raw, &stored); err != nil || stored.KeyCiphertext == "" {
			t.Fatalf("encrypted configuration missing: %v", err)
		}
		decrypted, err := f.deps.SecretBox.Decrypt(stored.KeyCiphertext)
		if err != nil || decrypted != key {
			t.Fatalf("configuration cannot be decrypted: %v", err)
		}
		ciphertexts = append(ciphertexts, stored.KeyCiphertext)
		for _, response := range []*httptest.ResponseRecorder{rec, f.do("GET", sourceParserConfigPath, cookie, "")} {
			assertSourceParserState(t, response, "console", true, true)
			if strings.Contains(response.Body.String(), key) || strings.Contains(response.Body.String(), stored.KeyCiphertext) {
				t.Fatal("configuration response exposes credential material")
			}
		}
		assertResolved(key)
	}
	assertSourceParserState(t, f.do("DELETE", sourceParserConfigPath, cookie, ""), "environment", true, true)
	assertResolved(environmentKey)
	raw, err := f.store.GetConfigValue(ctx, sourceParserConfigKey)
	if err != nil {
		t.Fatal(err)
	}
	var cleared storedSourceParserConfig
	if !json.Valid(raw) || json.Unmarshal(raw, &cleared) != nil || cleared.KeyCiphertext != "" {
		t.Fatal("clear must persist valid empty JSON configuration")
	}
	f.deps.Env.SnapAnyAPIKey = ""
	assertSourceParserState(t, f.do("GET", sourceParserConfigPath, cookie, ""), "none", false, true)
	assertResolved("")

	logs, err := f.store.ListAuditLogs(ctx, store.AuditLogFilter{})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, entry := range logs {
		if !strings.HasPrefix(entry.Action, "media.source-parser.") {
			continue
		}
		counts[entry.Action]++
		detail := string(entry.Detail)
		for _, secret := range append([]string{environmentKey, consoleKey, replacementKey, "keyCiphertext", "apiKey"}, ciphertexts...) {
			if strings.Contains(detail, secret) {
				t.Fatal("credential material leaked into source parser audit detail")
			}
		}
	}
	if counts["media.source-parser.update"] != 2 || counts["media.source-parser.clear"] != 1 {
		t.Fatalf("source parser audit events missing: %v", counts)
	}
	if transport.calls != 0 {
		t.Fatal("configuration changes must not probe the paid upstream")
	}
}

func TestSourceParserConfigRejectsInvalidWritesWithoutReplacingKey(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	ctx := context.Background()
	const key = "test-parser-key-to-retain"
	assertSourceParserState(t, f.do("PUT", sourceParserConfigPath, cookie, `{"apiKey":"`+key+`"}`), "console", false, true)
	before, err := f.store.GetConfigValue(ctx, sourceParserConfigKey)
	if err != nil {
		t.Fatal(err)
	}
	invalidBodies := map[string]string{
		"malformed": "{", "array": `[]`, "missing": `{}`, "null": `{"apiKey":null}`,
		"non-string": `{"apiKey":17}`, "empty": `{"apiKey":""}`, "whitespace": `{"apiKey":"   "}`,
		"internal space": `{"apiKey":"abc def"}`, "control": `{"apiKey":"abc\ndef"}`,
		"non-ASCII": `{"apiKey":"密钥"}`, "too long": `{"apiKey":"` + strings.Repeat("a", 4097) + `"}`,
		"unknown field":  `{"apiKey":"new-key","unexpected":true}`,
		"oversized body": `{"apiKey":"` + strings.Repeat("a", 32<<10) + `"}`,
	}
	for name, body := range invalidBodies {
		t.Run(name, func(t *testing.T) {
			rec := f.do("PUT", sourceParserConfigPath, cookie, body)
			if rec.Code != 400 {
				t.Fatalf("invalid update status: %d", rec.Code)
			}
			after, err := f.store.GetConfigValue(ctx, sourceParserConfigKey)
			if err != nil || string(after) != string(before) {
				t.Fatalf("invalid update replaced configuration: %v", err)
			}
		})
	}
	// 密钥不可加密时拒绝写入，避免明文落库或破坏既有配置；清除仍应可用。
	f.deps.SecretBox = nil
	rec := f.do("PUT", sourceParserConfigPath, cookie, `{"apiKey":"new-test-parser-key"}`)
	if rec.Code < 500 {
		t.Fatalf("save without encryption must fail: %d", rec.Code)
	}
	after, err := f.store.GetConfigValue(ctx, sourceParserConfigKey)
	if err != nil || string(after) != string(before) {
		t.Fatalf("unavailable encryption replaced configuration: %v", err)
	}
	assertSourceParserState(t, f.do("DELETE", sourceParserConfigPath, cookie, ""), "none", false, false)
}

func TestSourceParserConfigCorruptionDoesNotFallBackToEnvironment(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	ctx := context.Background()
	f.deps.Env.SnapAnyAPIKey = "test-environment-key-not-to-reuse"
	invalidPlaintext, err := f.deps.SecretBox.Encrypt("test key with spaces")
	if err != nil {
		t.Fatal(err)
	}
	invalidEncryptedConfig, err := json.Marshal(storedSourceParserConfig{KeyCiphertext: invalidPlaintext})
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{
		"malformed JSON":     []byte(`{`),
		"missing ciphertext": []byte(`{}`),
		"null ciphertext":    []byte(`{"keyCiphertext":null}`),
		"invalid ciphertext": []byte(`{"keyCiphertext":"test-invalid-ciphertext"}`),
		"invalid plaintext":  invalidEncryptedConfig,
	} {
		t.Run(name, func(t *testing.T) {
			if err := f.store.SetConfigValue(ctx, sourceParserConfigKey, raw); err != nil {
				t.Fatal(err)
			}
			key, err := resolveSourceParserKey(ctx, f.deps)
			if err == nil || key != "" || err.Error() != "source_parser_configuration_unavailable" {
				t.Fatalf("corrupt config must fail without fallback: key returned=%t err=%v", key != "", err)
			}
			rec := f.do("GET", sourceParserConfigPath, cookie, "")
			if rec.Code != 503 || strings.Contains(rec.Body.String(), f.deps.Env.SnapAnyAPIKey) || strings.Contains(rec.Body.String(), "test-invalid-ciphertext") {
				t.Fatalf("corrupt configuration response: %d", rec.Code)
			}
		})
	}
	assertSourceParserState(t, f.do("DELETE", sourceParserConfigPath, cookie, ""), "environment", true, true)
}

func TestSourceParserConfigRequiresRootForAllMethods(t *testing.T) {
	f := newTenantFixture(t)
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		body := ""
		if method == "PUT" {
			body = `{"apiKey":"test-forbidden-key"}`
		}
		for name, cookie := range map[string]string{
			"team admin": f.adminCookie, "member": f.memberCookie, "global admin": f.unassignedCookie,
		} {
			t.Run(method+" "+name, func(t *testing.T) {
				if rec := f.do(method, sourceParserConfigPath, cookie, body); rec.Code != 403 {
					t.Fatalf("non-root configuration access: %d", rec.Code)
				}
			})
		}
		if rec := f.do(method, sourceParserConfigPath, "", body); rec.Code != 401 {
			t.Fatalf("anonymous %s access: %d", method, rec.Code)
		}
	}
	deviceFixture, token, _ := newUserConfigPolicyFixture(t)
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		request := httptest.NewRequest(method, sourceParserConfigPath, strings.NewReader(`{"apiKey":"test-device-key"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		deviceFixture.engine.ServeHTTP(rec, request)
		if rec.Code != 401 {
			t.Fatalf("device %s configuration access: %d", method, rec.Code)
		}
	}
	raw, err := f.store.GetConfigValue(context.Background(), sourceParserConfigKey)
	if err != nil || len(raw) != 0 {
		t.Fatalf("unauthorized requests altered configuration: %v", err)
	}
}
