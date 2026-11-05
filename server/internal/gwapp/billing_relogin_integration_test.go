package gwapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/apiapp"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// 真正经过登录签发、binding API、模型网关准入/实扣与媒体鉴权，夹住跨服务恢复链路。
func TestBillingReloginThroughAPIAndGateway(t *testing.T) {
	f := newGatewayFixture(t, nil)
	ctx := context.Background()
	personal := personalAccountForGateway(t, f, "1")
	box, err := crypto.NewSecretBox(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	api := apiapp.New(&apiapp.Deps{Store: f.store, Env: &apiapp.Env{MemoryDB: true}, Now: time.Now, SecretBox: box, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	request := func(method, path, token, binding, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		if binding != "" {
			req.Header.Set("x-mendao-billing-binding", binding)
		}
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		return rec
	}
	bind := func(token, sessionID string) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"sessionId": sessionID, "workspaceIdentity": "local:relogin-e2e"})
		rec := request("POST", "/api/v1/client/payments/session-bindings", token, "", string(body))
		if rec.Code != 200 {
			t.Fatalf("bind: %d %s", rec.Code, rec.Body.String())
		}
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	sessionID := uuid.NewString()
	before := bind(f.token, sessionID)
	bindingID := before["bindingId"].(string)
	if err := f.store.SetBillingMode(ctx, f.userID, "team"); err != nil {
		t.Fatal(err)
	}
	oldDevice, err := f.store.FindDeviceTokenByHash(ctx, crypto.HashToken(f.token))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.RevokeDeviceToken(ctx, oldDevice.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	flowID, pollToken := uuid.NewString(), uuid.NewString()
	if err := f.store.CreateLoginFlow(ctx, store.LoginFlowRecord{ID: flowID, PollTokenHash: crypto.HashToken(pollToken), Status: store.FlowStatusAuthorized, UserID: f.userID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	rec := request("GET", "/api/v1/platform/auth/poll/"+flowID, pollToken, "", "")
	var login struct {
		Data struct {
			Credentials struct {
				Token string `json:"token"`
			} `json:"credentials"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	newToken := login.Data.Credentials.Token
	if rec.Code != 200 || newToken == "" || newToken == f.token {
		t.Fatalf("login did not issue a new token: %d", rec.Code)
	}
	if rec := gatewayBoundRequest(f, "GET", "/v1/models", newToken, bindingID, "", ""); rec.Code != 403 || !strings.Contains(rec.Body.String(), "当前设备") {
		t.Fatalf("unbound device: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request("GET", "/api/v1/media/overview", newToken, bindingID, ""); rec.Code != 403 || !strings.Contains(rec.Body.String(), "billing_binding_device_mismatch") {
		t.Fatalf("unbound media device: %d %s", rec.Code, rec.Body.String())
	}
	after := bind(newToken, sessionID)
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatalf("relogin changed binding: %s -> %s", b, a)
	}
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"usage":{"prompt_tokens":20,"completion_tokens":100}}`)
	})
	if rec := gatewayBoundRequest(f, "POST", "/v1/chat/completions", newToken, bindingID, "relogin-chat", `{"model":"claude-sonnet-4-5"}`); rec.Code != 200 {
		t.Fatalf("continue old session: %d %s", rec.Code, rec.Body.String())
	}
	charged, err := f.store.GetTeam(ctx, personal.ID)
	if err != nil || charged.UsedCredits != "0.001560" || f.team().UsedCredits != "0.000000" {
		t.Fatalf("wrong wallet charged: %+v %v", charged, err)
	}
	if rec := request("GET", "/api/v1/media/overview", newToken, bindingID, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), personal.ID) {
		t.Fatalf("media restore: %d %s", rec.Code, rec.Body.String())
	}
	if rec := gatewayBoundRequest(f, "GET", "/v1/models", f.token, bindingID, "", ""); rec.Code != 401 {
		t.Fatalf("revoked token: %d", rec.Code)
	}
	next := bind(newToken, uuid.NewString())
	if next["billingAccount"].(map[string]any)["id"] != f.teamID {
		t.Fatalf("new session ignored new default: %+v", next)
	}
}
