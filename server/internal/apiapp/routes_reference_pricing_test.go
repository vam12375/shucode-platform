package apiapp

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/media"
)

func TestReferencePricingRootUpdatesAndPublicProjection(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	const path = "/console/api/media/reference-pricing"
	if data := dataOf(t, f.do("GET", path, cookie, "")); data["snapAnyImportMicro"] != float64(500000) || data["analysisMicro"] != float64(1000000) {
		t.Fatalf("invalid default prices: %v", data)
	}
	for _, body := range []string{`{}`, `{"snapAnyImportMicro":-1,"analysisMicro":0}`, `{"snapAnyImportMicro":0.5,"analysisMicro":0}`, `{"snapAnyImportMicro":0,"analysisMicro":1,"extra":1}`, `{"snapAnyImportMicro":9007199254740992,"analysisMicro":0}`} {
		if rec := f.do("PUT", path, cookie, body); rec.Code != 400 {
			t.Fatalf("invalid price accepted: %d", rec.Code)
		}
	}
	data := dataOf(t, f.do("PUT", path, cookie, `{"snapAnyImportMicro":0,"analysisMicro":2500000}`))
	if data["snapAnyImportMicro"] != float64(0) || data["analysisMicro"] != float64(2500000) {
		t.Fatalf("prices not updated: %v", data)
	}
	pricing, err := f.deps.Media.Repo.ReferencePricing(context.Background())
	if err != nil || pricing.AnalysisMicro != 2500000 || pricing.SnapAnyImportMicro != 0 {
		t.Fatalf("media not reading shared prices: %+v %v", pricing, err)
	}
	if rec := f.do("PUT", path, "", `{"snapAnyImportMicro":0,"analysisMicro":0}`); rec.Code != 401 {
		t.Fatal("anonymous can set prices")
	}
	if rec := f.do("GET", path, "", ""); rec.Code != 401 {
		t.Fatal("anonymous can read root settings")
	}
	if err := f.store.SetConfigValue(context.Background(), media.ReferencePricingKey, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if rec := f.do("GET", path, cookie, ""); rec.Code != 503 {
		t.Fatal("corrupt pricing silently defaulted")
	}
}

func TestReferencePricingRejectsNonRootConsoleUsers(t *testing.T) {
	f := newTenantFixture(t)
	for _, cookie := range []string{f.adminCookie, f.memberCookie, f.otherCookie, f.unassignedCookie} {
		for _, method := range []string{"GET", "PUT"} {
			if rec := f.do(method, "/console/api/media/reference-pricing", cookie, `{"snapAnyImportMicro":0,"analysisMicro":0}`); rec.Code != 403 {
				t.Fatalf("%s non-root pricing access: %d", method, rec.Code)
			}
		}
	}
}

func TestReferencePricingProjectsCurrentPricesWithoutGrantingDeviceManagement(t *testing.T) {
	f, token, _ := newUserConfigPolicyFixture(t)
	ctx := context.Background()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.engine.ServeHTTP(w, r)
		return w
	}
	for _, method := range []string{"GET", "PUT"} {
		if rec := request(method, "/console/api/media/reference-pricing", `{"snapAnyImportMicro":0,"analysisMicro":0}`); rec.Code != 401 {
			t.Fatalf("device gained pricing management access: %s %d", method, rec.Code)
		}
	}
	for _, raw := range []string{`{"snapAnyImportMicro":750000,"analysisMicro":2000000}`, `{"snapAnyImportMicro":0,"analysisMicro":0}`} {
		if err := f.store.SetConfigValue(ctx, media.ReferencePricingKey, []byte(raw)); err != nil {
			t.Fatal(err)
		}
		want, _ := media.ParseReferencePricing([]byte(raw))
		data := dataOf(t, request("GET", "/api/v1/media/overview", ""))
		prices := data["referencePricing"].(map[string]any)
		if prices["snapAnyImportMicro"] != float64(want.SnapAnyImportMicro) || prices["analysisMicro"] != float64(want.AnalysisMicro) {
			t.Fatalf("overview returned stale or default prices: %v", prices)
		}
	}
	if err := f.store.SetConfigValue(ctx, media.ReferencePricingKey, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if rec := request("GET", "/api/v1/media/overview", ""); rec.Code != 503 {
		t.Fatalf("overview silently used corrupt prices: %d", rec.Code)
	}
}
