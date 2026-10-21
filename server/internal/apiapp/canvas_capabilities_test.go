package apiapp

import (
	"github.com/vam12375/shucode-platform/server/internal/media"
	"reflect"
	"testing"
)

func TestPublicCanvasCapabilities(t *testing.T) {
	models := publicMediaModels([]media.Model{{ID: "image", Kind: "image", UpstreamModel: "doubao-seedream-5-0-260128", PriceMicro: 100000}})
	if len(models) != 1 || !reflect.DeepEqual(models[0]["resolutions"], []string{"2K", "3K", "4K"}) || models[0]["maxReferenceImages"] != 8 {
		t.Fatalf("public capabilities: %+v", models)
	}
	for _, key := range []string{"upstreamModel", "costMicro", "providerId", "priceVersion"} {
		if _, exists := models[0][key]; exists {
			t.Fatalf("internal field %s exposed", key)
		}
	}
}
