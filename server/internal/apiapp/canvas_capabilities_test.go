package apiapp

import (
	"github.com/vam12375/shucode-platform/server/internal/media"
	"reflect"
	"strings"
	"testing"
)

func TestPublicCanvasCapabilities(t *testing.T) {
	models := publicMediaModels([]media.Model{{ID: "image", Kind: "image", UpstreamModel: "doubao-seedream-5-0-260128", PriceMicro: 100000}})
	if len(models) != 1 || !reflect.DeepEqual(models[0]["resolutions"], []string{"2K", "3K", "4K"}) || models[0]["maxReferenceImages"] != 8 {
		t.Fatalf("public capabilities: %+v", models)
	}
	if !reflect.DeepEqual(models[0]["aspectRatios"], media.CanvasAspectRatios("image")) {
		t.Fatalf("image ratios differ from admission capabilities")
	}
	for _, key := range []string{"upstreamModel", "costMicro", "providerId", "priceVersion"} {
		if _, exists := models[0][key]; exists {
			t.Fatalf("internal field %s exposed", key)
		}
	}
}

func TestPublicMediaModelGroupsKeepPricedSKUs(t *testing.T) {
	base := media.Model{ID: "video-480", Name: "Video Model 480p", Kind: "video", ProviderID: "provider-a", Adapter: "ark", UpstreamModel: "upstream-model", Resolutions: []string{"480p"}, MinDuration: 4, MaxDuration: 15, PriceMicro: 100_000}
	second := base
	second.ID, second.Name, second.Resolutions, second.PriceMicro = "video-720", "Video Model 720p", []string{"720p"}, 200_000
	otherProvider, otherAdapter, otherUpstream, otherKind := base, base, base, base
	otherProvider.ProviderID = "provider-b"
	otherAdapter.Adapter = "future"
	otherUpstream.UpstreamModel = "another-model"
	otherKind.Kind = "image"
	models := publicMediaModels([]media.Model{base, second, otherProvider, otherAdapter, otherUpstream, otherKind})
	group, ok := models[0]["groupId"].(string)
	if !ok || group == "" || len(group) > 200 || strings.Contains(group, base.ProviderID) || strings.Contains(group, base.UpstreamModel) {
		t.Fatalf("group ID must be bounded and opaque")
	}
	if models[1]["groupId"] != group || models[0]["groupName"] != "Video Model" || models[1]["groupName"] != "Video Model" {
		t.Fatalf("resolution SKUs do not share their group")
	}
	for _, model := range models[2:] {
		if model["groupId"] == group {
			t.Fatalf("unrelated model joined the group")
		}
	}
	if models[0]["id"] != base.ID || models[1]["id"] != second.ID || models[0]["priceMicro"] != base.PriceMicro || models[1]["priceMicro"] != second.PriceMicro {
		t.Fatalf("grouping changed concrete pricing SKU")
	}
	if !reflect.DeepEqual(models[0]["aspectRatios"], []string{"16:9", "9:16", "1:1"}) || models[0]["minDuration"] != 4 || models[0]["maxDuration"] != 15 {
		t.Fatalf("video capabilities differ from admission capabilities")
	}
	base.UpstreamModel = "  upstream-model  "
	if publicMediaModels([]media.Model{base})[0]["groupId"] != group || publicMediaModels([]media.Model{second, base})[0]["groupId"] != group {
		t.Fatalf("group identity changes with whitespace or catalog order")
	}
}
