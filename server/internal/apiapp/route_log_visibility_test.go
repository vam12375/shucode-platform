package apiapp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestRoutedUsageKeepsProcurementRootOnlyAcrossConsoleSurfaces(t *testing.T) {
	f := newTenantFixture(t)
	ctx := context.Background()
	const publicName = "DeepSeek V4.1 Flash"
	if _, err := f.store.UpsertModel(ctx, store.UpsertModelInput{
		ModelID: "public-flash", DisplayName: publicName,
		InputPricePerMtok: "1", OutputPricePerMtok: "1",
	}); err != nil {
		t.Fatal(err)
	}
	mediaModels := []media.Model{{
		ID: "private-media-sku", Name: "Private supplier image", DisplayName: "公开图片模型",
		Kind: "image", Adapter: "ark", UpstreamModel: "private-image-upstream", ProviderID: uuid.NewString(),
		Routes: []media.ModelRoute{{ProviderID: uuid.NewString(), Priority: 100, Weight: 1, Enabled: true}},
	}}
	raw, _ := json.Marshal(mediaModels)
	if err := f.store.SetConfigValue(ctx, media.ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	providers := []string{uuid.NewString(), uuid.NewString()}
	cost := int64(12345)
	for i, model := range []string{"public-flash", "public-flash", "media:private-media-sku"} {
		if err := f.store.InsertUsage(ctx, store.UsageRecord{
			ID: uuid.NewString(), RequestID: uuid.NewString(), TeamID: f.team.ID, UserID: f.member.ID,
			Model: model, UpstreamModel: "private-route-" + providers[i%2], ProviderID: providers[i%2],
			InputTokens: 10, OutputTokens: 20, CreditsMicro: 20000, Status: store.UsageStatusOk, CreatedAt: time.Now(),
			PriceSnapshot: &store.UsagePricingSnapshot{
				SchemaVersion: 1, CapturedAt: time.Now(), Cost: store.UsageCostSnapshot{
					Status: "estimated", ModelID: "private-cost-model", PricingVersion: "private-price-version", EstimatedMicro: &cost,
					Rates: &store.UsagePriceSnapshot{Currency: "CNY"},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	root := f.do("GET", "/console/api/usage?teamId="+f.team.ID, f.rootCookie, "")
	if root.Code != 200 {
		t.Fatal(root.Code, root.Body.String())
	}
	for _, want := range []string{"private-route-", "private-cost-model", "private-price-version", providers[0], providers[1]} {
		if !strings.Contains(root.Body.String(), want) {
			t.Fatalf("root lost route/cost evidence %q", want)
		}
	}
	for _, cookie := range []string{f.adminCookie, f.memberCookie} {
		for _, path := range []string{"/console/api/usage", "/console/api/usage/export", "/console/api/teams/" + f.team.ID} {
			rec := f.do("GET", path, cookie, "")
			if rec.Code != 200 {
				t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
			}
			for _, secret := range []string{"private-", "priceSnapshot", "providerId", "upstreamModel", "routes", providers[0], providers[1]} {
				if strings.Contains(rec.Body.String(), secret) {
					t.Fatalf("non-root %s leaked %q: %s", path, secret, rec.Body.String())
				}
			}
			if !strings.Contains(rec.Body.String(), publicName) || !strings.Contains(rec.Body.String(), "公开图片模型") {
				t.Fatalf("non-root %s lost canonical model names: %s", path, rec.Body.String())
			}
		}
		usage := dataOf(t, f.do("GET", "/console/api/usage", cookie, ""))
		textModels := map[string]bool{}
		for _, raw := range usage["records"].([]any) {
			row := raw.(map[string]any)
			if row["modelDisplayName"] == publicName {
				textModels[row["model"].(string)] = true
			}
		}
		if len(textModels) != 1 || usage["costSummary"] != nil {
			t.Fatalf("one public text model must not split by supplier: %+v", usage)
		}
		for _, path := range []string{"/console/api/providers", "/console/api/models/public-flash/routes", "/console/api/media/models", "/console/api/media/projects", "/console/api/ops/overview"} {
			if rec := f.do("GET", path, cookie, ""); rec.Code != 403 {
				t.Fatalf("non-root procurement endpoint %s returned %d", path, rec.Code)
			}
		}
	}
}
