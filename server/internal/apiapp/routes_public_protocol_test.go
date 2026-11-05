package apiapp

import (
	"context"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

type corruptUserConfigProviderKeyStore struct {
	store.PlatformStore
	providerID string
}

func (s corruptUserConfigProviderKeyStore) GetUpstreamProvider(ctx context.Context, id string) (*store.UpstreamProviderRecord, error) {
	provider, err := s.PlatformStore.GetUpstreamProvider(ctx, id)
	if err != nil || provider == nil || id != s.providerID {
		return provider, err
	}
	corrupt := *provider
	corrupt.APIKeyCiphertext = "invalid-ciphertext"
	return &corrupt, nil
}

type legacyMixedUserConfigStore struct {
	store.PlatformStore
	modelID string
	route   store.UpstreamRouteRecord
}

func (s legacyMixedUserConfigStore) ListUpstreamRoutes(
	ctx context.Context,
	modelID string,
	includeDisabled bool,
) ([]store.UpstreamRouteRecord, error) {
	routes, err := s.PlatformStore.ListUpstreamRoutes(ctx, modelID, includeDisabled)
	if err != nil {
		return nil, err
	}
	if s.modelID == modelID && (includeDisabled || s.route.Enabled) {
		routes = append(routes, s.route)
	}
	return routes, nil
}

func legacyMixedRoute(t *testing.T, f *apiFixture, modelID string, providerID string, id string) store.UpstreamRouteRecord {
	t.Helper()
	provider, err := f.store.GetUpstreamProvider(context.Background(), providerID)
	if err != nil || provider == nil {
		t.Fatalf("load legacy mixed provider: provider=%v error=%v", provider, err)
	}
	return store.UpstreamRouteRecord{
		ID: id, ModelID: modelID, ProviderID: providerID, ProviderName: provider.Name,
		ProviderEnabled: provider.Enabled, Enabled: true, Priority: 2, Weight: 1,
	}
}

func TestUserConfigRejectsMixedPoolBeforeFilteringUndecryptableProvider(t *testing.T) {
	f, token, teamID := newUserConfigPolicyFixture(t)
	ctx := context.Background()
	if _, err := f.store.SaveTeamPolicy(ctx, store.TeamPolicyRecord{TeamID: teamID, AllowedModelIDs: []string{"model-a"}}); err != nil {
		t.Fatal(err)
	}
	createProvider := func(name string, protocol store.UpstreamProtocol) *store.UpstreamProviderRecord {
		t.Helper()
		provider, err := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
			Name: name, BaseURL: f.upstream.URL, Protocol: protocol, Enabled: boolPointer(true),
		})
		if err != nil {
			t.Fatal(err)
		}
		return provider
	}
	chatProvider := createProvider("chat-provider", store.ProtocolOpenAIChatCompletions)
	responsesProvider := createProvider("responses-provider", store.ProtocolOpenAIResponses)
	if _, err := f.store.CreateUpstreamRoute(ctx, store.CreateUpstreamRouteInput{
		ModelID: "model-a", ProviderID: chatProvider.ID, Priority: 1, Weight: 1, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	f.deps.Store = corruptUserConfigProviderKeyStore{
		PlatformStore: legacyMixedUserConfigStore{
			PlatformStore: f.store,
			modelID:       "model-a",
			route:         legacyMixedRoute(t, f, "model-a", responsesProvider.ID, "legacy-responses-route"),
		},
		providerID: responsesProvider.ID,
	}

	response := requestUserConfig(f, token, "")
	if response.Code != 200 {
		t.Fatalf("user-config: %d %s", response.Code, response.Body.String())
	}
	personal := dataOf(t, response)["envelope"].(map[string]any)["personalConfig"].(map[string]any)
	provider := personal["providerConfigRules"].(map[string]any)["providerRules"].([]any)[0].(map[string]any)
	modelIDs := provider["config"].(map[string]any)["personalModelIds"].([]any)
	if len(modelIDs) != 0 {
		t.Fatalf("damaged credentials must not hide a mixed protocol pool: %+v", modelIDs)
	}
}
