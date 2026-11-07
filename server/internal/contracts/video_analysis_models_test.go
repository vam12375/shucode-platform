package contracts

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestVideoAnalysisModelCatalogWire(t *testing.T) {
	raw, err := os.ReadFile("testdata/video-analysis-models.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []json.RawMessage
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		var catalog VideoAnalysisModelCatalog
		if err := json.Unmarshal(fixture, &catalog); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(catalog)
		if err != nil {
			t.Fatal(err)
		}
		var before, after any
		if err := json.Unmarshal(fixture, &before); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &after); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("catalog wire drift: got %s want %s", encoded, fixture)
		}
	}
}
