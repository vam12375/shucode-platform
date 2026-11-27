package apiapp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/media"
)

func TestMediaCatalogRoutePoolRoundTripAndProviderValidation(t *testing.T) {
	f := newTenantFixture(t)
	ctx := context.Background()
	first, err := f.store.CreateUpstreamProvider(ctx, storeProviderInput("first-media-route"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.store.CreateUpstreamProvider(ctx, storeProviderInput("second-media-route"))
	if err != nil {
		t.Fatal(err)
	}
	model := media.Model{
		ID: "public-image", Name: "Public Image", Kind: "image", ProviderID: first.ID,
		Adapter: "ark", UpstreamModel: "shared-upstream-image", Resolutions: []string{"2K"}, PriceMicro: 100_000, PriceVersion: "v1",
		Routes: []media.ModelRoute{{ProviderID: first.ID, Priority: 100, Weight: 2, Enabled: true}, {ProviderID: second.ID, Priority: 200, Weight: 1, Enabled: false}},
	}
	const path = "/console/api/media/models"
	initial := f.do("GET", path, f.rootCookie, "").Header().Get("ETag")
	if initial == "" {
		t.Fatal("媒体目录缺少初始版本")
	}
	save := func(etag string) int {
		t.Helper()
		raw, _ := json.Marshal([]media.Model{model})
		return f.doForge("PUT", path, f.rootCookie, string(raw), map[string]string{
			"Origin": f.deps.Env.PublicAPIOrigin, "Sec-Fetch-Site": "same-origin", "If-Match": etag,
		}).Code
	}
	if code := save(""); code != 428 {
		t.Fatalf("目录写入必须提供版本: %d", code)
	}
	if code := save(initial); code != 200 {
		t.Fatalf("valid pool rejected: %d", code)
	}
	response := f.do("GET", path, f.rootCookie, "")
	current := response.Header().Get("ETag")
	if current == initial || save(initial) != 409 {
		t.Fatal("旧目录覆盖了已保存配置")
	}
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"weight":2`) || !strings.Contains(response.Body.String(), second.ID) {
		t.Fatalf("console lost route config: %d %s", response.Code, response.Body.String())
	}
	stored, err := f.deps.Media.Repo.Models(ctx)
	if err != nil || len(stored) != 1 || len(stored[0].Routes) != 2 || stored[0].Routes[0].ProviderID != first.ID {
		t.Fatalf("route config did not persist: %+v err=%v", stored, err)
	}
	public, _ := json.Marshal(publicMediaModels(stored))
	for _, hidden := range []string{`"routes"`, `"providerId"`, `"upstreamModel"`, first.ID, second.ID, model.UpstreamModel} {
		if strings.Contains(string(public), hidden) {
			t.Fatalf("public media catalog leaked route detail %q: %s", hidden, public)
		}
	}
	model.Routes[1].ProviderID = uuid.NewString()
	if code := save(current); code != 400 {
		t.Fatalf("missing route provider accepted: %d", code)
	}
	stored, _ = f.deps.Media.Repo.Models(ctx)
	if stored[0].Routes[1].ProviderID != second.ID {
		t.Fatal("invalid save replaced the existing catalog")
	}
	model.Routes[1].ProviderID = first.ID
	if code := save(current); code != 400 && code != 422 {
		t.Fatalf("duplicate route provider accepted: %d", code)
	}
}

func TestMediaRoutePoolCannotOverrideSharedCapabilityOrModel(t *testing.T) {
	f := newTenantFixture(t)
	provider, err := f.store.CreateUpstreamProvider(context.Background(), storeProviderInput("media-route"))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"adapter":"toapis"`, `"upstreamModel":"different-model"`, `"priceMicro":1`} {
		body := `[{"id":"image","name":"image","kind":"image","providerId":"` + provider.ID + `","adapter":"ark","upstreamModel":"image","resolutions":["2K"],"priceMicro":100000,"priceVersion":"v1","routes":[{"providerId":"` + provider.ID + `","priority":100,"weight":1,"enabled":true,` + field + `}]}]`
		response := f.do("PUT", "/console/api/media/models", f.rootCookie, body)
		if response.Code != 400 {
			t.Fatalf("route capability override accepted: %d %s", response.Code, response.Body.String())
		}
	}
}
