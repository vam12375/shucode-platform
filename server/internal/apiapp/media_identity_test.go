package apiapp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func publicMediaAliasForTest(t *testing.T, f *apiFixture, id string) string {
	t.Helper()
	aliases, err := ensureMediaAliases(context.Background(), f.store, nil)
	if err != nil || aliases[id] == "" {
		t.Fatal("missing public alias", id, err)
	}
	return aliases[id]
}

func TestMediaAliasesPersistAcrossConcurrentRegistration(t *testing.T) {
	f := newAPIEmptyFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			aliases, err := ensureMediaAliases(ctx, f.store, []string{"toapis-private-sku", uuid.NewString()})
			if err != nil {
				t.Error(err)
				return
			}
			results <- aliases["toapis-private-sku"]
		}()
	}
	wg.Wait()
	close(results)
	first := ""
	for alias := range results {
		if first == "" {
			first = alias
		}
		if alias != first || !strings.HasPrefix(alias, "mm_") {
			t.Fatal("alias changed", alias, first)
		}
	}
	if first == "" {
		t.Fatal("no alias assigned")
	}
	stored, err := ensureMediaAliases(ctx, f.store, nil)
	if err != nil || len(stored) != 9 || stored["toapis-private-sku"] != first {
		t.Fatal("CAS lost registration", stored, err)
	}
	if err := f.store.SetConfigValue(ctx, mediaAliasesKey, []byte(`{"version":7}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureMediaAliases(ctx, f.store, []string{"toapis-private-sku"}); err == nil {
		t.Fatal("corrupt alias registry reopened")
	}
}

func TestMediaPublicIDRoundTripAndLegacyIdempotency(t *testing.T) {
	f, token, teamID := setupAgentRouteFixture(t)
	ctx := context.Background()
	models, err := f.deps.Media.Repo.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const internalID = "toapis-private-image"
	models[0].ID, models[0].DisplayName = internalID, "用户已有显示名"
	config, _ := json.Marshal(models)
	if err := f.store.SetConfigValue(ctx, media.ConfigKey, config); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SaveTeamPolicy(ctx, store.TeamPolicyRecord{TeamID: teamID, AllowedModelIDs: []string{internalID}}); err != nil {
		t.Fatal(err)
	}
	request := agentRouteRequestBody()
	request["modelId"] = internalID
	quote := agentRouteRequest(t, f, token, "POST", "/api/v1/media/agent/generations/quote", request)
	data := dataOf(t, quote)
	alias, _ := data["modelId"].(string)
	if !strings.HasPrefix(alias, "mm_") || data["modelName"] != "用户已有显示名" || strings.Contains(quote.Body.String(), internalID) {
		t.Fatal("quote identity/name", quote.Body.String())
	}
	request["modelId"] = alias
	generate := map[string]any{"request": request, "confirmationToken": data["confirmationToken"], "expectedQuoteMicro": data["quoteMicro"]}
	if response := agentRouteRequest(t, f, token, "POST", "/api/v1/media/agent/generations", generate); response.Code != 200 {
		t.Fatal("public ID submit", response.Code, response.Body.String())
	}
	// The same old request ID must still match the exact internal request frozen at admission.
	request["modelId"] = internalID
	if response := agentRouteRequest(t, f, token, "POST", "/api/v1/media/agent/generations", generate); response.Code != 200 {
		t.Fatal("legacy retry diverged", response.Code, response.Body.String())
	}
	projects, err := f.deps.Media.Repo.All(ctx)
	if err != nil || len(projects) != 1 || projects[0].Agent.ModelID != internalID {
		t.Fatal("persistent owner was rewritten", projects, err)
	}
	request["requestId"], request["modelId"] = "forbidden-alias", alias
	if _, err := f.store.SaveTeamPolicy(ctx, store.TeamPolicyRecord{TeamID: teamID, AllowedModelIDs: []string{"another-model"}}); err != nil {
		t.Fatal(err)
	}
	if response := agentRouteRequest(t, f, token, "POST", "/api/v1/media/agent/generations/quote", request); response.Code == 200 {
		t.Fatal("public ID bypassed model policy")
	}
}

func TestHistoricalMediaProjectionKeepsAliasesWithoutCurrentCatalog(t *testing.T) {
	f, token, teamID := setupAgentRouteFixture(t)
	ctx := context.Background()
	const internalID = "toapis-deleted-video"
	id := uuid.NewString()
	now := time.Now().UTC()
	p := &media.Project{ID: id, TeamID: teamID, Version: 1, Status: "failed", CreatedAt: now, UpdatedAt: now,
		Brief:  media.Brief{VideoModel: internalID, Product: "保留显示名和创作内容", Mode: "auto"},
		Canvas: &media.CanvasRequest{Kind: "video", ModelID: internalID, Prompt: "用户自己的 toapis 文本不做替换"},
		Models: []media.Model{{ID: internalID}}, Error: "composition_failed: /private/secret/tool ToAPIs",
		Shots: []media.Shot{{Status: "failed", VideoTaskID: "toapis-task-private", ProviderError: &media.ProviderError{Code: "ToAPIs.PrivateError"}, Error: "provider_rejected"}},
	}
	if err := f.deps.Media.Repo.Save(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	response := agentRouteRequest(t, f, token, http.MethodGet, "/api/v1/media/projects/"+id, nil)
	data := dataOf(t, response)
	alias := data["brief"].(map[string]any)["videoModel"].(string)
	if !strings.HasPrefix(alias, "mm_") || data["canvas"].(map[string]any)["modelId"] != alias {
		t.Fatal("historical identity missing", data)
	}
	for _, secret := range []string{internalID, "toapis-task-private", "ToAPIs.PrivateError", "/private/secret"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatal("historical response leaked", secret, response.Body.String())
		}
	}
	if !strings.Contains(response.Body.String(), "用户自己的 toapis 文本不做替换") || data["error"] != "composition_failed" {
		t.Fatal("content or safe error changed", data)
	}
	before := response.Body.String()
	f.engine = New(f.deps)
	after := agentRouteRequest(t, f, token, http.MethodGet, "/api/v1/media/projects/"+id, nil)
	if before != after.Body.String() {
		t.Fatal("alias changed after app restart")
	}
	persisted, err := f.deps.Media.Repo.Get(ctx, teamID, id)
	if err != nil || persisted.Brief.VideoModel != internalID || persisted.Shots[0].VideoTaskID != "toapis-task-private" {
		t.Fatal("projection changed stored history", err)
	}
}
