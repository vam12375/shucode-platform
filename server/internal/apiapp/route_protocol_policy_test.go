package apiapp

import (
	"context"
	"fmt"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func createProtocolTestProvider(t *testing.T, f *apiFixture, name string, protocol store.UpstreamProtocol) string {
	t.Helper()
	provider, err := f.store.CreateUpstreamProvider(context.Background(), store.CreateUpstreamProviderInput{
		Name: name, BaseURL: f.upstream.URL, Protocol: protocol,
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider.ID
}

func createProtocolTestModel(t *testing.T, f *apiFixture, modelID string) {
	t.Helper()
	_, err := f.store.UpsertModel(context.Background(), store.UpsertModelInput{
		ModelID: modelID, DisplayName: modelID, InputPricePerMtok: "1", OutputPricePerMtok: "1",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func createProtocolTestRoute(t *testing.T, f *apiFixture, cookie, modelID, providerID string, enabled bool) string {
	t.Helper()
	body := fmt.Sprintf(`{"providerId":%q,"enabled":%t}`, providerID, enabled)
	rec := f.do("POST", "/console/api/models/"+modelID+"/routes", cookie, body)
	if rec.Code != 200 {
		t.Fatalf("create route: %d %s", rec.Code, rec.Body.String())
	}
	route := dataOf(t, rec)["route"].(map[string]any)
	return route["id"].(string)
}

func TestConsoleModelRoutesRejectEnabledProtocolMixAndAllowDisabledDraft(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	chatProvider := createProtocolTestProvider(t, f, "route-chat", store.ProtocolOpenAIChatCompletions)
	anthropicProvider := createProtocolTestProvider(t, f, "route-anthropic", store.ProtocolAnthropicMessages)
	modelID := "route-protocol-model"
	createProtocolTestModel(t, f, modelID)
	createProtocolTestRoute(t, f, cookie, modelID, chatProvider, true)

	path := "/console/api/models/" + modelID + "/routes"
	conflict := f.do("POST", path, cookie, fmt.Sprintf(`{"providerId":%q,"enabled":true}`, anthropicProvider))
	if conflict.Code != 409 {
		t.Fatalf("mixed enabled route must be rejected: %d %s", conflict.Code, conflict.Body.String())
	}

	draft := f.do("POST", path, cookie, fmt.Sprintf(`{"providerId":%q,"enabled":false}`, anthropicProvider))
	if draft.Code != 200 {
		t.Fatalf("disabled incompatible draft should be allowed: %d %s", draft.Code, draft.Body.String())
	}
	routeID := dataOf(t, draft)["route"].(map[string]any)["id"].(string)
	enable := f.do("PATCH", "/console/api/routes/"+routeID, cookie, `{"enabled":true}`)
	if enable.Code != 409 {
		t.Fatalf("enabling incompatible route must be rejected: %d %s", enable.Code, enable.Body.String())
	}
}

func TestConsoleProviderProtocolChangeCannotSplitEnabledRoutePool(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	firstProvider := createProtocolTestProvider(t, f, "route-chat-first", store.ProtocolOpenAIChatCompletions)
	secondProvider := createProtocolTestProvider(t, f, "route-chat-second", store.ProtocolOpenAIChatCompletions)
	modelID := "route-provider-protocol-model"
	createProtocolTestModel(t, f, modelID)
	createProtocolTestRoute(t, f, cookie, modelID, firstProvider, true)
	createProtocolTestRoute(t, f, cookie, modelID, secondProvider, true)

	changed := f.do("PATCH", "/console/api/providers/"+secondProvider, cookie, `{"protocol":"anthropic-messages"}`)
	if changed.Code != 409 {
		t.Fatalf("provider protocol change must preserve enabled route protocol: %d %s", changed.Code, changed.Body.String())
	}
	provider, err := f.store.GetUpstreamProvider(context.Background(), secondProvider)
	if err != nil {
		t.Fatal(err)
	}
	if provider == nil || provider.Protocol != store.ProtocolOpenAIChatCompletions {
		t.Fatalf("rejected mutation changed provider protocol: %+v", provider)
	}
}
