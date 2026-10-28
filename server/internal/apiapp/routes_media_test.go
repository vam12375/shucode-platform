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
	if got := w.Header().Get("Cross-Origin-Resource-Policy"); got != "cross-origin" {
		t.Fatalf("签名媒体资源应允许 Desktop/Web 跨源预览，CORP=%q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("展示签名媒体不需要开放脚本跨源读取，ACAO=%q", got)
	}
	if w = request("GET", strings.Replace(signed, team, uuid.NewString(), 1), bytes.NewBuffer(nil), false, ""); w.Code != 403 {
		t.Fatal("cross team signature", w.Code)
	}
	if w = request("GET", "/console/api/media/projects", bytes.NewBuffer(nil), true, ""); w.Code == 200 {
		t.Fatal("device cannot reconcile")
	}
	models := []media.Model{
		{ID: "v", Name: "video", Kind: "video", CostMicro: 123_000, PriceMicro: 456_000, PriceVersion: "secret-version", VideoReference: &media.VideoReferenceCapability{MaxDurationSeconds: 20, MaxImages: 3, MaxSegments: 2, PriceMicro: 654_000, CostMicro: 321_000}},
		{ID: "other", Name: "other", Kind: "video", CostMicro: 789_000, PriceMicro: 987_000, PriceVersion: "secret-version"},
	}
	data, _ := json.Marshal(models)
	f.store.SetConfigValue(context.Background(), media.ConfigKey, data)
	if err := f.deps.Media.Repo.UpsertInternalAsset(context.Background(), team, media.Asset{ID: "internal-reference-clip", Name: "reference.mp4", Kind: "video", Mime: "video/mp4", Internal: true, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	f.store.SaveTeamPolicy(context.Background(), store.TeamPolicyRecord{TeamID: team, AllowedModelIDs: []string{"v"}})
	admissionID := uuid.NewString()
	if result, err := f.store.AdmitUsage(context.Background(), store.ReserveQuotaInput{
		ID: admissionID, TeamID: team, RequestID: "media-overview-" + admissionID,
		Micro: 1_250_000, TTL: time.Minute, CreatedAt: time.Now(),
	}); err != nil || result != store.ReserveReserved {
		t.Fatalf("admit usage for overview: result=%s error=%v", result, err)
	}
	w = request("GET", "/api/v1/media/overview", bytes.NewBuffer(nil), true, "")
	for _, secretField := range []string{"costMicro", "priceVersion"} {
		if strings.Contains(w.Body.String(), secretField) {
			t.Fatalf("客户端媒体概览不得包含价格字段 %q: %s", secretField, w.Body.String())
		}
	}
	if !strings.Contains(w.Body.String(), `"priceMicro":456000`) {
		t.Fatalf("媒体概览需要下发用户消耗: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"maxSegments":2`) || !strings.Contains(w.Body.String(), `"videoReference"`) || strings.Contains(w.Body.String(), "internal-reference-clip") {
		t.Fatalf("媒体概览应公开视频参考能力与售价，并隐藏内部切片: %s", w.Body.String())
	}
	var overview struct {
		Data struct {
			Models []media.Model `json:"models"`
			Assets []media.Asset `json:"assets"`
			Team   struct {
				AvailableMicro int64 `json:"availableMicro"`
				ReservedMicro  int64 `json:"reservedMicro"`
			} `json:"team"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &overview)
	if w.Code != 200 || len(overview.Data.Models) != 1 || overview.Data.Models[0].ID != "v" {
		t.Fatal(w.Body.String())
	}
	if overview.Data.Models[0].VideoReference == nil || overview.Data.Models[0].VideoReference.CostMicro != 0 || strings.Contains(w.Body.String(), `"costMicro"`) {
		t.Fatalf("参考能力公开数据泄露成本: %s", w.Body.String())
	}
	for _, asset := range overview.Data.Assets {
		if asset.ID == "internal-reference-clip" || asset.Internal {
			t.Fatalf("内部参考切片出现在 overview: %+v", asset)
		}
	}
	if overview.Data.Team.AvailableMicro != 100_000_000 || overview.Data.Team.ReservedMicro != 0 {
		t.Fatalf("媒体准入不得改变概览余额: %+v", overview.Data.Team)
	}
	if err := f.store.SettleReservation(context.Background(), admissionID, store.UsageRecord{
		ID: admissionID, TeamID: team, RequestID: "media-overview-" + admissionID,
		Model: "media:fixture", CreditsMicro: 100_858_123, Status: store.UsageStatusOk, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	w = request("GET", "/api/v1/media/overview", bytes.NewBuffer(nil), true, "")
	if err := json.Unmarshal(w.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || overview.Data.Team.AvailableMicro != -858_123 || overview.Data.Team.ReservedMicro != 0 {
		t.Fatalf("媒体概览必须保留完整实扣产生的负余额: %+v", overview.Data.Team)
	}
	if w = request("POST", "/api/v1/media/projects/none/cancel", bytes.NewBufferString(`{"version":1} {}`), true, "application/json"); w.Code != 400 {
		t.Fatal("trailing json", w.Code)
	}
}
