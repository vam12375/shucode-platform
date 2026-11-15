package apiapp

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func installTestConsole(t *testing.T, f *apiFixture) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{"assets/public.js": "public", "assets/session.js": "session", "assets/platform.js": "platform", "assets/setup.js": "setup"}
	if err := os.Mkdir(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("export const fixture = 1;"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(consoleAssetManifest{Version: 1, Files: files})
	for name, data := range map[string][]byte{"asset-access.json": raw, "index.html": []byte("<html>entry</html>"), "assets/old.js": []byte("legacy private code"), "assets/private.js.map": []byte("source code")} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.deps.Env.ConsoleDistDir = dir
	f.engine = New(f.deps)
	return dir
}

func TestConsoleAssetPermissionsAndRevocation(t *testing.T) {
	f := newTenantFixture(t)
	dir := installTestConsole(t, f.apiFixture)
	for _, method := range []string{"GET", "HEAD"} {
		for _, actor := range []struct {
			cookie            string
			session, platform bool
		}{{"", false, false}, {f.memberCookie, true, false}, {f.adminCookie, true, false}, {f.otherCookie, true, false}, {f.rootCookie, true, true}} {
			for name, allowed := range map[string]bool{"public": true, "session": actor.session, "platform": actor.platform, "setup": false} {
				rec := f.do(method, "/console/assets/"+name+".js", actor.cookie, "")
				want := 404
				if allowed {
					want = 200
				}
				if rec.Code != want {
					t.Fatalf("%s %s: got %d want %d", method, name, rec.Code, want)
				}
				if name != "public" && (!strings.Contains(rec.Header().Get("Cache-Control"), "no-store") || strings.Contains(rec.Header().Get("Cache-Control"), "public")) {
					t.Fatal("private asset may be cached", rec.Header())
				}
			}
		}
	}
	for _, path := range []string{"/console/assets/old.js", "/console/assets/private.js.map", "/console/.vite/manifest.json", "/console/asset-access.json", "/console/.env", "/console/assets/", "/console/assets", "/console/../go.mod", "/console/assets/%2e%2e/asset-access.json"} {
		if rec := f.do("GET", path, f.rootCookie, ""); rec.Code != 404 {
			t.Fatalf("unexpected file served: %s %d", path, rec.Code)
		}
	}
	// Even a listed symlink cannot escape the deployment directory.
	outside := filepath.Join(t.TempDir(), "outside.js")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "assets/public.js")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "assets/public.js")); err != nil {
		t.Fatal(err)
	}
	if rec := f.do("GET", "/console/assets/public.js", "", ""); rec.Code != 404 {
		t.Fatal("symlink escaped static root")
	}
	if rec := f.do("POST", "/console/api/auth/logout", f.rootCookie, ""); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	for _, headers := range []map[string]string{{"Range": "bytes=0-5"}, {"If-Modified-Since": time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)}, {"Authorization": "Bearer " + strings.TrimPrefix(f.rootCookie, sessionCookieName+"=")}} {
		if rec := f.doForge("GET", "/console/assets/platform.js", f.rootCookie, "", headers); rec.Code != 404 {
			t.Fatal("revoked access bypassed", rec.Code)
		}
	}
}

func TestConsoleAssetsRequireManifestAndMFA(t *testing.T) {
	f := newRootMFAFixture(t)
	dir := installTestConsole(t, f.f)
	f.f.deps.Env.RootMFARequired = true
	if rec := f.f.do("GET", "/console/assets/platform.js", f.cookie, ""); rec.Code != 404 {
		t.Fatal("MFA gate bypassed")
	}
	if rec := f.f.do("GET", "/console/assets/session.js", f.cookie, ""); rec.Code != 200 {
		t.Fatal("MFA enrollment unavailable")
	}
	for _, raw := range []string{"", `{"version":1,"files":{"assets/public.js":"public","assets/old.js":"unknown"}}`} {
		if err := os.WriteFile(filepath.Join(dir, "asset-access.json"), []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
		f.f.engine = New(f.f.deps)
		if rec := f.f.do("GET", "/console/", "", ""); rec.Code != 404 {
			t.Fatal("invalid manifest opened deployment")
		}
	}
}

func TestSetupGrantAndClosedDeploymentDoNotDiscloseStatus(t *testing.T) {
	f := newAPIEmptyFixture(t)
	f.deps.Env.MemoryDB = false
	f.deps.Env.BootstrapSecret = "fixture-bootstrap-credential"
	installTestConsole(t, f)
	for _, path := range []string{"/console/api/setup/status", "/console/assets/setup.js"} {
		if rec := f.do("GET", path, "", ""); rec.Code != 404 {
			t.Fatal("anonymous setup disclosure", path)
		}
	}
	grant := f.doForge("POST", "/console/api/setup/access", "", "", map[string]string{"x-bootstrap-secret": f.deps.Env.BootstrapSecret})
	if grant.Code != 200 {
		t.Fatal(grant.Code, grant.Body.String())
	}
	cookies := grant.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/console" {
		t.Fatal("unsafe setup cookie")
	}
	cookie := cookies[0].Name + "=" + cookies[0].Value
	if strings.Contains(cookie, f.deps.Env.BootstrapSecret) {
		t.Fatal("plaintext bootstrap secret")
	}
	if rec := f.do("GET", "/console/assets/setup.js", cookie, ""); rec.Code != 200 {
		t.Fatal("grant failed", rec.Code)
	}
	f.deps.Env.BootstrapSecret = "rotated-fixture"
	if rec := f.do("GET", "/console/assets/setup.js", cookie, ""); rec.Code != 404 {
		t.Fatal("rotated grant survived")
	}
	f.deps.Env.BootstrapSecret = "fixture-bootstrap-credential"
	now := f.deps.Now()
	f.deps.Now = func() time.Time { return now.Add(setupAccessTTL + time.Second) }
	if rec := f.do("GET", "/console/assets/setup.js", cookie, ""); rec.Code != 404 {
		t.Fatal("expired grant survived")
	}
	f.deps.Now = func() time.Time { return now }
	created := f.do("POST", "/console/api/setup/root", cookie, `{"email":"operator@example.test","displayName":"Operator","password":"test-password-1234"}`)
	if created.Code != 200 {
		t.Fatal(created.Code, created.Body.String())
	}
	for _, path := range []string{"/console/api/setup/status", "/console/assets/setup.js"} {
		if rec := f.do("GET", path, cookie, ""); rec.Code != 404 {
			t.Fatal("closed setup reopened", path)
		}
	}
}

func TestInternalAccountNotExposedToTeamMembers(t *testing.T) {
	f := newTenantFixture(t)
	ctx := context.Background()
	if err := f.store.AddMember(ctx, f.team.ID, f.root.ID, store.MemberRoleMember); err != nil {
		t.Fatal(err)
	}
	if rec := f.do("PATCH", "/console/api/teams/"+f.team.ID, f.rootCookie, `{"name":"Updated team"}`); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if err := f.store.InsertUsage(ctx, store.UsageRecord{ID: "private-actor-usage", RequestID: "private-actor-request", UserID: f.root.ID, TeamID: f.team.ID, Model: "fixture", DeviceTokenID: "private-system-device", CreditsMicro: 1000, Status: store.UsageStatusOk, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateQuotaRequest(ctx, store.QuotaRequestRecord{ID: "private-actor-quota", TeamID: f.team.ID, RequestedBy: f.root.ID, AmountCredits: "1", Reason: "fixture", Status: "pending", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range []string{f.adminCookie, f.memberCookie} {
		paths := []string{"/console/api/auth/me", "/console/api/users", "/console/api/teams/" + f.team.ID}
		if cookie == f.adminCookie {
			paths = append(paths, "/console/api/audit", "/console/api/quota-requests", "/console/api/usage", "/console/api/usage/export")
		}
		for _, path := range paths {
			rec := f.do("GET", path, cookie, "")
			if rec.Code != 200 {
				t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
			}
			for _, secret := range []string{f.root.ID, f.root.Email, "private-system-device", `"root"`, "platformAccess", "超级管理员"} {
				if strings.Contains(rec.Body.String(), secret) {
					t.Fatalf("%s leaked %q: %s", path, secret, rec.Body.String())
				}
			}
		}
	}
	me := dataOf(t, f.do("GET", "/console/api/auth/me", f.rootCookie, ""))["user"].(map[string]any)
	if me["role"] != "admin" || me["platformAccess"] != true {
		t.Fatal("capability projection incorrect", me)
	}
}
