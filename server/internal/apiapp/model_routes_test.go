package apiapp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestConsoleModelRoutesValidateMappingAndExposeProviderAvailability(t *testing.T) {
	f := newAPIFixture(t)
	cookie := f.loginAdmin()
	ctx := context.Background()
	provider, err := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
		Name: "route-api-fixture", BaseURL: f.upstream.URL, Protocol: store.ProtocolOpenAIChatCompletions,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.UpsertModel(ctx, store.UpsertModelInput{
		ModelID: "route-api-model", DisplayName: "路由测试", InputPricePerMtok: "1", OutputPricePerMtok: "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/console/api/models/route-api-model/routes"
	for _, mapping := range []string{`123`, `[]`, `true`, `"` + strings.Repeat("x", 201) + `"`} {
		body := fmt.Sprintf(`{"providerId":%q,"upstreamModel":%s}`, provider.ID, mapping)
		if rec := f.do("POST", path, cookie, body); rec.Code != 400 {
			t.Fatalf("invalid mapping %s: %d %s", mapping, rec.Code, rec.Body.String())
		}
	}
	created := f.do("POST", path, cookie, fmt.Sprintf(`{"providerId":%q,"upstreamModel":" upstream-model "}`, provider.ID))
	if created.Code != 200 {
		t.Fatalf("create route: %d %s", created.Code, created.Body.String())
	}
	route := dataOf(t, created)["route"].(map[string]any)
	if route["providerEnabled"] != true || route["upstreamModel"] != "upstream-model" {
		t.Fatalf("route availability/mapping: %+v", route)
	}
	disabled := false
	if _, err := f.store.UpdateUpstreamProvider(ctx, provider.ID, store.UpdateUpstreamProviderPatch{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	routes := dataOf(t, f.do("GET", path, cookie, ""))["routes"].([]any)
	if len(routes) != 1 || routes[0].(map[string]any)["providerEnabled"] != false || routes[0].(map[string]any)["enabled"] != true {
		t.Fatalf("route enabled state must differ from provider availability: %+v", routes)
	}
	if rec := f.do("GET", "/console/api/models/unknown/routes", cookie, ""); rec.Code != 404 {
		t.Fatalf("unknown model: %d %s", rec.Code, rec.Body.String())
	}
}
