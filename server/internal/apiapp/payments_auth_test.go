package apiapp

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestClientPaymentRoutesRequireOnlyBearerDeviceAuthentication(t *testing.T) {
	f := newAPIEmptyFixture(t)
	user, err := f.store.CreateUser(context.Background(), store.UserInput{Email: "payments-auth@test.local", PasswordHash: "x", DisplayName: "Buyer", Role: store.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	token := createDeviceTokenForTest(t, f, user.ID)
	paths := []struct{ method, path string }{
		{"GET", "/api/v1/client/payments/packages"},
		{"GET", "/api/v1/client/payments/account"},
		{"POST", "/api/v1/client/payments/account/mode"},
		{"POST", "/api/v1/client/payments/session-bindings"},
		{"GET", "/api/v1/client/payments/orders"},
		{"POST", "/api/v1/client/payments/orders"},
		{"GET", "/api/v1/client/payments/orders/not-an-order"},
		{"POST", "/api/v1/client/payments/orders/not-an-order/reconcile"},
	}
	for _, path := range paths {
		for _, kind := range []string{"missing", "api-key", "wrong-scheme", "empty-bearer", "bearer-and-api-key", "duplicate"} {
			t.Run(path.method+path.path+"/"+kind, func(t *testing.T) {
				req := httptest.NewRequest(path.method, path.path, strings.NewReader(`{}`))
				req.Header.Set("Content-Type", "application/json")
				switch kind {
				case "api-key":
					req.Header.Set("x-api-key", token)
				case "wrong-scheme":
					req.Header.Set("Authorization", "Basic "+token)
					req.Header.Set("x-api-key", token)
				case "empty-bearer":
					req.Header.Set("Authorization", "Bearer ")
					req.Header.Set("x-api-key", token)
				case "bearer-and-api-key":
					req.Header.Set("Authorization", "Bearer "+token)
					req.Header.Set("x-api-key", token)
				case "duplicate":
					req.Header.Add("Authorization", "Bearer "+token)
					req.Header.Add("Authorization", "Bearer "+token)
				}
				rec := httptest.NewRecorder()
				f.engine.ServeHTTP(rec, req)
				if rec.Code != 401 {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
			})
		}
	}
	if rec := clientPaymentRequest(f, "GET", "/api/v1/client/payments/account", token, ""); rec.Code != 200 {
		t.Fatalf("valid bearer: %d %s", rec.Code, rec.Body.String())
	}
}
