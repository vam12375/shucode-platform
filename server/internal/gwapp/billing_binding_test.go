package gwapp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func gatewayBinding(t *testing.T, f *gatewayFixture) *store.BillingSessionBinding {
	t.Helper()
	device, err := f.store.FindDeviceTokenByHash(context.Background(), crypto.HashToken(f.token))
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.store.CreateBillingSessionBinding(context.Background(), store.BillingSessionBindingInput{SessionID: uuid.NewString(), WorkspaceIdentity: "local:billing-test", ActorUserID: f.userID, DeviceTokenID: device.ID})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func gatewayBoundRequest(f *gatewayFixture, method, path, token, bindingID, requestID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-mendao-billing-binding", bindingID)
	if requestID != "" {
		req.Header.Set("x-request-id", requestID)
	}
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	return rec
}

func TestBillingBindingPinsTextAndToolChargesAfterModeSwitch(t *testing.T) {
	f := newGatewayFixture(t, func(env *Env, f *gatewayFixture) {
		env.DeepSeekToolsBaseURL = f.upstream.URL
		env.DeepSeekToolsAPIKey = "tool-secret"
		env.DeepSeekSearchFeeMicro = 5000
	})
	ctx := context.Background()
	personal := personalAccountForGateway(t, f, "1")
	binding := gatewayBinding(t, f)
	if err := f.store.SetBillingMode(ctx, f.userID, "team"); err != nil {
		t.Fatal(err)
	}
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-mendao-billing-binding") != "" {
			t.Error("billing binding leaked upstream")
		}
		if r.URL.Path == deepSeekSearchPath {
			fmt.Fprint(w, `{"results":[{"title":"found"}]}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"usage":{"prompt_tokens":20,"completion_tokens":100}}`)
	})
	if rec := gatewayBoundRequest(f, "POST", "/v1/chat/completions", f.token, binding.ID, "bound-chat", `{"model":"claude-sonnet-4-5"}`); rec.Code != 200 {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
	}
	if rec := gatewayBoundRequest(f, "POST", deepSeekSearchPath, f.token, binding.ID, "bound-search", `{"query":"hello"}`); rec.Code != 200 {
		t.Fatalf("search: %d %s", rec.Code, rec.Body.String())
	}
	charged, err := f.store.GetTeam(ctx, personal.ID)
	if err != nil || charged.UsedCredits != "0.006560" || f.team().UsedCredits != "0.000000" {
		t.Fatalf("charge account: %+v %v team=%+v", charged, err, f.team())
	}
	reservationID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("deepseek-tool:"+personal.ID+":"+f.userID+":bound-search:search")).String()
	reservation, err := f.store.GetQuotaReservation(ctx, reservationID)
	if err != nil || reservation == nil || reservation.BillingBindingID != binding.ID || reservation.TeamID != personal.ID {
		t.Fatalf("tool reservation attribution: %+v %v", reservation, err)
	}
}

func TestBillingBindingRejectsCrossDeviceInvalidAndRevokedTeam(t *testing.T) {
	f := newGatewayFixture(t, nil)
	ctx := context.Background()
	binding := gatewayBinding(t, f)
	device, err := f.store.FindDeviceTokenByHash(ctx, crypto.HashToken(f.token))
	if err != nil {
		t.Fatal(err)
	}
	other := *device
	other.ID = uuid.NewString()
	other.TokenHash = crypto.HashToken("other-device-token")
	if err := f.store.CreateDeviceToken(ctx, other); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ token, binding string }{{f.token, "invalid"}, {"other-device-token", binding.ID}} {
		if rec := gatewayBoundRequest(f, "GET", "/v1/models", tc.token, tc.binding, "", ""); rec.Code != 403 {
			t.Fatalf("invalid binding: %d %s", rec.Code, rec.Body.String())
		}
	}
	if _, err := f.store.UpdateTeam(ctx, f.teamID, store.UpdateTeamPatch{HasDisabled: true, DisabledAt: ptr(time.Now())}); err != nil {
		t.Fatal(err)
	}
	if rec := gatewayBoundRequest(f, "GET", "/v1/models", f.token, binding.ID, "", ""); rec.Code != 403 {
		t.Fatalf("disabled team: %d %s", rec.Code, rec.Body.String())
	}
}

func TestBillingBindingBackgroundRestoresAndReadsOriginalAccount(t *testing.T) {
	f := newGatewayFixture(t, nil)
	ctx := context.Background()
	personal := personalAccountForGateway(t, f, "1")
	binding := gatewayBinding(t, f)
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			fmt.Fprint(w, `{"id":"resp_binding","status":"queued","usage":null}`)
			return
		}
		fmt.Fprint(w, `{"id":"resp_binding","status":"completed","output":[],"usage":{"input_tokens":100,"output_tokens":20}}`)
	})
	if rec := gatewayBoundRequest(f, "POST", "/v1/responses", f.token, binding.ID, "bound-background", `{"model":"claude-sonnet-4-5","background":true}`); rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	job, err := f.store.FindBackgroundGeneration(ctx, personal.ID, "resp_binding")
	if err != nil || job == nil {
		t.Fatalf("persisted job: %+v %v", job, err)
	}
	reservation, err := f.store.GetQuotaReservation(ctx, job.ReservationID)
	if err != nil || reservation.BillingBindingID != binding.ID {
		t.Fatalf("persisted binding: %+v %v", reservation, err)
	}
	if err := f.store.SetBillingMode(ctx, f.userID, "team"); err != nil {
		t.Fatal(err)
	}
	reconcileBackgroundGenerations(ctx, backgroundTestDeps(f))
	charged, err := f.store.GetTeam(ctx, personal.ID)
	if err != nil || charged.UsedCredits != "0.000600" || f.team().UsedCredits != "0.000000" {
		t.Fatalf("restored charge: %+v %v team=%+v", charged, err, f.team())
	}
	if rec := gatewayBoundRequest(f, "GET", "/v1/responses/resp_binding", f.token, binding.ID, "", ""); rec.Code != 200 {
		t.Fatalf("bound result: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("GET", "/v1/responses/resp_binding", f.token, ""); rec.Code != 404 {
		t.Fatalf("current team must not expose personal result: %d %s", rec.Code, rec.Body.String())
	}
}
