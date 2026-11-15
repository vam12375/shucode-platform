package apiapp

import "testing"

func TestConsoleVersionVisibility(t *testing.T) {
	f := newTenantFixture(t)
	f.deps.Env.AppVersion = "v0.102.5"
	f.deps.Env.AppSubject = "fix(console): 修复控制台版本显示"
	for _, tc := range []struct {
		name, cookie string
		root         bool
	}{
		{"root", f.rootCookie, true},
		{"team admin", f.adminCookie, false},
		{"member", f.memberCookie, false},
		{"global admin", f.otherCookie, false},
		{"unassigned", f.unassignedCookie, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do("GET", "/console/api/version", tc.cookie, "")
			data := dataOf(t, rec)
			if data["tag"] != f.deps.Env.AppVersion || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("version/cache: %s %v", rec.Body.String(), rec.Header())
			}
			if tc.root {
				if len(data) != 2 || data["subject"] != f.deps.Env.AppSubject {
					t.Fatalf("root title missing: %v", data)
				}
			} else if len(data) != 1 {
				t.Fatalf("title exposed to %s: %v", tc.name, data)
			}
		})
	}
	if rec := f.do("GET", "/console/api/version", "", ""); rec.Code != 401 {
		t.Fatalf("anonymous version: %d %s", rec.Code, rec.Body.String())
	}
	// 本地未注入构建信息时保持空版本，不能再把业务配置序号当成软件版本。
	f.deps.Env.AppVersion, f.deps.Env.AppSubject = "", ""
	if data := dataOf(t, f.do("GET", "/console/api/version", f.memberCookie, "")); len(data) != 1 || data["tag"] != "" {
		t.Fatalf("unversioned build: %v", data)
	}
}
