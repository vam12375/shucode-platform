package apiapp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func setupAgentRouteFixture(t *testing.T) (*apiFixture, string, string) {
	t.Helper()
	f, token, teamID := newUserConfigPolicyFixture(t)
	model := media.Model{
		ID: "agent-image", Name: "Agent Image", DisplayName: "Agent Image", Kind: "image",
		Adapter: "ark", ProviderID: uuid.NewString(), UpstreamModel: "image",
		Resolutions: []string{"2K"}, PriceMicro: 100000, PriceVersion: "v1",
	}
	raw, err := json.Marshal([]media.Model{model})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetConfigValue(context.Background(), media.ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SaveTeamPolicy(context.Background(), store.TeamPolicyRecord{TeamID: teamID, AllowedModelIDs: []string{model.ID}}); err != nil {
		t.Fatal(err)
	}
	return f, token, teamID
}

func agentRouteRequest(t *testing.T, f *apiFixture, token, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	return rec
}

func agentRouteRequestBody() map[string]any {
	return map[string]any{
		"requestId": "agent-route-1", "intent": "generate", "kind": "image", "prompt": "portrait",
		"assetIds": []string{}, "modelId": "agent-image", "resolution": "2K", "aspectRatio": "1:1",
		"duration": 0, "count": 1,
	}
}

func createAgentRouteDevice(t *testing.T, f *apiFixture, userID string) string {
	t.Helper()
	token := crypto.GenerateDeviceToken()
	ciphertext, err := f.deps.SecretBox.Encrypt(token)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateDeviceToken(context.Background(), store.DeviceTokenRecord{
		ID: uuid.NewString(), UserID: userID, Name: "Agent generation test device",
		TokenHash: crypto.HashToken(token), TokenCiphertext: ciphertext, TokenHint: crypto.DeviceTokenHint(token), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return token
}

func TestMediaAgentGenerationRouteStrictConfirmationAndIdempotency(t *testing.T) {
	f, token, teamID := setupAgentRouteFixture(t)
	quote := agentRouteRequest(t, f, token, http.MethodPost, "/api/v1/media/agent/generations/quote", agentRouteRequestBody())
	if quote.Code != http.StatusOK {
		t.Fatalf("quote: %d %s", quote.Code, quote.Body.String())
	}
	var envelope struct {
		Data struct {
			Token string `json:"confirmationToken"`
			Quote int64  `json:"quoteMicro"`
		} `json:"data"`
	}
	if err := json.Unmarshal(quote.Body.Bytes(), &envelope); err != nil || envelope.Data.Token == "" || envelope.Data.Quote != 100000 {
		t.Fatalf("quote response: %s", quote.Body.String())
	}
	device, err := f.store.FindDeviceTokenByHash(context.Background(), crypto.HashToken(token))
	if err != nil || device == nil {
		t.Fatalf("authenticated device lookup: %v", err)
	}

	unknown := agentRouteRequestBody()
	unknown["expectedQuoteMicro"] = envelope.Data.Quote
	if response := agentRouteRequest(t, f, token, http.MethodPost, "/api/v1/media/agent/generations/quote", unknown); response.Code != http.StatusBadRequest {
		t.Fatalf("internal quote field must be rejected: %d %s", response.Code, response.Body.String())
	}

	request := agentRouteRequestBody()
	generate := map[string]any{"request": request, "confirmationToken": envelope.Data.Token, "expectedQuoteMicro": envelope.Data.Quote}
	created := agentRouteRequest(t, f, token, http.MethodPost, "/api/v1/media/agent/generations", generate)
	if created.Code != http.StatusOK {
		t.Fatalf("generate: %d %s", created.Code, created.Body.String())
	}
	again := agentRouteRequest(t, f, token, http.MethodPost, "/api/v1/media/agent/generations", generate)
	if again.Code != http.StatusOK {
		t.Fatalf("idempotent generate: %d %s", again.Code, again.Body.String())
	}

	changed := agentRouteRequestBody()
	changed["prompt"] = "different"
	changedGenerate := map[string]any{"request": changed, "confirmationToken": envelope.Data.Token, "expectedQuoteMicro": envelope.Data.Quote}
	if response := agentRouteRequest(t, f, token, http.MethodPost, "/api/v1/media/agent/generations", changedGenerate); response.Code != http.StatusConflict {
		t.Fatalf("changed request must reject token: %d %s", response.Code, response.Body.String())
	}

	if response := agentRouteRequest(t, f, token, http.MethodPost, "/api/v1/media/agent/generations", map[string]any{
		"request": agentRouteRequestBody(), "confirmationToken": envelope.Data.Token, "expectedQuoteMicro": envelope.Data.Quote + 1,
	}); response.Code != http.StatusConflict {
		t.Fatalf("changed quote must reject token: %d %s", response.Code, response.Body.String())
	}
	otherDevice := createAgentRouteDevice(t, f, device.UserID)
	if response := agentRouteRequest(t, f, otherDevice, http.MethodPost, "/api/v1/media/agent/generations", generate); response.Code != http.StatusConflict {
		t.Fatalf("device-bound token must reject another device: %d %s", response.Code, response.Body.String())
	}

	ctx := context.Background()
	project, err := f.deps.Media.GetAgentGeneration(ctx, teamID, device.UserID, "agent-route-1")
	if err != nil || project == nil {
		t.Fatalf("get created project: %v", err)
	}
	outputID := uuid.NewString()
	if err := f.deps.Media.Repo.AddAsset(ctx, teamID, media.Asset{ID: outputID, Name: "generated.png", Kind: "image", Mime: "image/png"}); err != nil {
		t.Fatal(err)
	}
	project, err = f.deps.Media.Mutate(ctx, teamID, project.ID, project.Version, func(current *media.Project) error {
		current.Status = "completed"
		current.Shots[0].Status = "succeeded"
		current.Shots[0].OutputAssetID = outputID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	read := func(authToken string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/media/agent/generations/agent-route-1", nil)
		req.Header.Set("Authorization", "Bearer "+authToken)
		rec := httptest.NewRecorder()
		f.engine.ServeHTTP(rec, req)
		return rec
	}
	var state struct {
		Data struct {
			Status       string `json:"status"`
			Stage        string `json:"stage"`
			CloudArchive string `json:"cloudArchive"`
		} `json:"data"`
	}
	completedPending := read(token)
	if err := json.Unmarshal(completedPending.Body.Bytes(), &state); err != nil || completedPending.Code != http.StatusOK || state.Data.Status != "completed" || state.Data.Stage != "saving" || state.Data.CloudArchive != "pending" {
		t.Fatalf("generation and archive must have independent state: %d %s %v", completedPending.Code, completedPending.Body.String(), err)
	}
	project, err = f.deps.Media.Mutate(ctx, teamID, project.ID, project.Version, func(current *media.Project) error {
		current.TeamAssetArchives = []media.CanvasTeamAssetArchive{{SourceAssetID: outputID, TeamAssetID: uuid.NewString(), Status: "saved"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	completedSaved := read(token)
	if err := json.Unmarshal(completedSaved.Body.Bytes(), &state); err != nil || completedSaved.Code != http.StatusOK || state.Data.Status != "completed" || state.Data.Stage != "completed" || state.Data.CloudArchive != "saved" {
		t.Fatalf("saved archive must not change generation status: %d %s %v", completedSaved.Code, completedSaved.Body.String(), err)
	}
	if response := agentRouteRequest(t, f, token, http.MethodGet, "/api/v1/media/projects/"+project.ID, nil); response.Code != http.StatusNotFound {
		t.Fatalf("Agent project must stay outside generic project reads: %d %s", response.Code, response.Body.String())
	}
	if response := agentRouteRequest(t, f, token, http.MethodPost, "/api/v1/media/projects/"+project.ID+"/cancel", map[string]any{"version": project.Version}); response.Code != http.StatusNotFound {
		t.Fatalf("Agent project must stay outside generic project actions: %d %s", response.Code, response.Body.String())
	}
	overview := agentRouteRequest(t, f, token, http.MethodGet, "/api/v1/media/overview", nil)
	if overview.Code != http.StatusOK || bytes.Contains(overview.Body.Bytes(), []byte(project.ID)) {
		t.Fatalf("Agent project must stay outside overview project lists: %d %s", overview.Code, overview.Body.String())
	}
	otherUser, err := f.store.CreateUser(ctx, store.UserInput{Email: "agent-other-user@test.local", DisplayName: "Other", PasswordHash: "x", Role: store.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddMember(ctx, teamID, otherUser.ID, store.MemberRoleMember); err != nil {
		t.Fatal(err)
	}
	otherUserToken := createAgentRouteDevice(t, f, otherUser.ID)
	if response := read(otherUserToken); response.Code != http.StatusNotFound {
		t.Fatalf("Agent generation GET must isolate users: %d %s", response.Code, response.Body.String())
	}

	f.deps.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	if response := agentRouteRequest(t, f, token, http.MethodPost, "/api/v1/media/agent/generations", generate); response.Code != http.StatusConflict {
		t.Fatalf("expired token must reject: %d %s", response.Code, response.Body.String())
	}
}

func TestMediaAgentGenerationCancelRouteReturnsPlatformState(t *testing.T) {
	f, token, _ := setupAgentRouteFixture(t)
	quote := agentRouteRequest(t, f, token, http.MethodPost, "/api/v1/media/agent/generations/quote", agentRouteRequestBody())
	if quote.Code != http.StatusOK {
		t.Fatalf("quote: %d %s", quote.Code, quote.Body.String())
	}
	var envelope struct {
		Data struct {
			Token string `json:"confirmationToken"`
			Quote int64  `json:"quoteMicro"`
		} `json:"data"`
	}
	if err := json.Unmarshal(quote.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	generate := map[string]any{"request": agentRouteRequestBody(), "confirmationToken": envelope.Data.Token, "expectedQuoteMicro": envelope.Data.Quote}
	if created := agentRouteRequest(t, f, token, http.MethodPost, "/api/v1/media/agent/generations", generate); created.Code != http.StatusOK {
		t.Fatalf("generate: %d %s", created.Code, created.Body.String())
	}
	cancel := agentRouteRequest(t, f, token, http.MethodPost, "/api/v1/media/agent/generations/agent-route-1/cancel", map[string]any{})
	if cancel.Code != http.StatusOK || !strings.Contains(cancel.Body.String(), `"cancelRequested":true`) {
		t.Fatalf("cancel should return accepted platform state: %d %s", cancel.Code, cancel.Body.String())
	}
	missing := agentRouteRequest(t, f, token, http.MethodPost, "/api/v1/media/agent/generations/missing/cancel", map[string]any{})
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown generation cancellation: %d %s", missing.Code, missing.Body.String())
	}
}

func TestMediaAgentGenerationRouteRejectsSourceArtifactUris(t *testing.T) {
	f, token, _ := setupAgentRouteFixture(t)
	body := agentRouteRequestBody()
	body["sourceArtifactUris"] = []string{"artifact://unsupported"}
	if response := agentRouteRequest(t, f, token, http.MethodPost, "/api/v1/media/agent/generations/quote", body); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("source artifact must be rejected: %d %s", response.Code, response.Body.String())
	}
}
