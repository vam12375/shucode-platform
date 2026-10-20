package apiapp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestMediaAuthUploadTeamIsolationAndAdminBoundary(t *testing.T) {
	f, token, team := newUserConfigPolicyFixture(t)
	f.deps.Media.Blobs.Root = t.TempDir()
	request := func(method, path string, body *bytes.Buffer, auth bool, contentType string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, body)
		if auth {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		w := httptest.NewRecorder()
		f.engine.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/v1/media/overview", bytes.NewBuffer(nil), false, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request("GET", "/api/v1/platform/account", bytes.NewBuffer(nil), false, ""); w.Code != 401 {
		t.Fatal("account must require the same device session", w.Code)
	}
	if w := request("GET", "/api/v1/platform/account", bytes.NewBuffer(nil), true, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "display_name") || strings.Contains(w.Body.String(), token) {
		t.Fatal("account projection", w.Code, w.Body.String())
	}
	accountWithoutTeam, err := f.store.CreateUser(context.Background(), store.UserInput{
		Email: "no-team@test.local", DisplayName: "未加入团队", PasswordHash: "x", Role: store.RoleUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	noTeamToken := crypto.GenerateDeviceToken()
	noTeamCiphertext, err := f.deps.SecretBox.Encrypt(noTeamToken)
	if err != nil {
		t.Fatal(err)
	}
	noTeamDeviceID := uuid.NewString()
	if err := f.store.CreateDeviceToken(context.Background(), store.DeviceTokenRecord{
		ID: noTeamDeviceID, UserID: accountWithoutTeam.ID, Name: "未加入团队设备",
		TokenHash: crypto.HashToken(noTeamToken), TokenCiphertext: noTeamCiphertext,
		TokenHint: crypto.DeviceTokenHint(noTeamToken), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	noTeamRequest := httptest.NewRequest("GET", "/api/v1/platform/account", nil)
	noTeamRequest.Header.Set("Authorization", "Bearer "+noTeamToken)
	noTeamResponse := httptest.NewRecorder()
	f.engine.ServeHTTP(noTeamResponse, noTeamRequest)
	if noTeamResponse.Code != 200 || !strings.Contains(noTeamResponse.Body.String(), accountWithoutTeam.ID) {
		t.Fatal("account check must not require a team", noTeamResponse.Code, noTeamResponse.Body.String())
	}
	if err := f.store.RevokeDeviceToken(context.Background(), noTeamDeviceID, time.Now()); err != nil {
		t.Fatal(err)
	}
	noTeamRequest = httptest.NewRequest("GET", "/api/v1/platform/account", nil)
	noTeamRequest.Header.Set("Authorization", "Bearer "+noTeamToken)
	noTeamResponse = httptest.NewRecorder()
	f.engine.ServeHTTP(noTeamResponse, noTeamRequest)
	if noTeamResponse.Code != 401 {
		t.Fatal("revoked device must fail account check", noTeamResponse.Code)
	}
	raw, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jX1kAAAAASUVORK5CYII=")
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "product.png")
	part.Write(raw)
	writer.WriteField("product", "fixture")
	writer.Close()
	w := request("POST", "/api/v1/media/assets", body, true, writer.FormDataContentType())
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var envelope struct {
		Data media.Asset `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &envelope)
	if envelope.Data.Mime != "image/png" || envelope.Data.URL == "" {
		t.Fatal(w.Body.String())
	}
	signed := strings.TrimPrefix(envelope.Data.URL, f.deps.Env.PublicAPIOrigin)
	if w = request("GET", signed, bytes.NewBuffer(nil), false, ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w = request("GET", strings.Replace(signed, team, uuid.NewString(), 1), bytes.NewBuffer(nil), false, ""); w.Code != 403 {
		t.Fatal("cross team signature", w.Code)
	}
	if w = request("GET", "/console/api/media/projects", bytes.NewBuffer(nil), true, ""); w.Code == 200 {
		t.Fatal("device cannot reconcile")
	}
	models := []media.Model{{ID: "v", Name: "video", Kind: "video"}, {ID: "other", Name: "other", Kind: "video"}}
	data, _ := json.Marshal(models)
	f.store.SetConfigValue(context.Background(), media.ConfigKey, data)
	f.store.SaveTeamPolicy(context.Background(), store.TeamPolicyRecord{TeamID: team, AllowedModelIDs: []string{"v"}})
	w = request("GET", "/api/v1/media/overview", bytes.NewBuffer(nil), true, "")
	var overview struct {
		Data struct {
			Models []media.Model `json:"models"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &overview)
	if w.Code != 200 || len(overview.Data.Models) != 1 || overview.Data.Models[0].ID != "v" {
		t.Fatal(w.Body.String())
	}
	if w = request("POST", "/api/v1/media/projects/none/cancel", bytes.NewBufferString(`{"version":1} {}`), true, "application/json"); w.Code != 400 {
		t.Fatal("trailing json", w.Code)
	}
}
