package contracts

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestMediaAssetNameMatchesClientUnicodeRules(t *testing.T) {
	for _, tc := range []struct {
		name, input, normalized string
		valid                   bool
	}{
		{"trim-bom", "\ufeff产品图\ufeff", "产品图", true},
		{"preserve-nel", "\u0085产品图\u0085", "\u0085产品图\u0085", true},
		{"only-bom", "\ufeff", "", false},
		{"unicode-limit", strings.Repeat("😀", 200), strings.Repeat("😀", 200), true},
		{"unicode-over-limit", strings.Repeat("😀", 201), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, valid := NormalizeMediaAssetName(tc.input)
			if valid != tc.valid || got != tc.normalized {
				t.Fatalf("name contract mismatch: valid=%v normalized=%q", valid, got)
			}
		})
	}
}

func TestMediaLibraryFrozenContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/media-library.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Names []struct {
			Input      string
			Valid      bool
			Normalized string
		}
		Library    json.RawMessage
		Management json.RawMessage
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, v := range fixture.Names {
		normalized, valid := NormalizeMediaAssetName(v.Input)
		if valid != v.Valid || valid && normalized != v.Normalized {
			t.Fatalf("name contract mismatch: %q", v.Input)
		}
	}
	for _, tc := range []struct {
		raw   json.RawMessage
		value any
	}{{fixture.Library, &MediaLibrary{}}, {fixture.Management, &MediaAssetManagement{}}} {
		if err = json.Unmarshal(tc.raw, tc.value); err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(tc.value)
		if err != nil {
			t.Fatal(err)
		}
		var expected, actual any
		json.Unmarshal(tc.raw, &expected)
		json.Unmarshal(out, &actual)
		if !reflect.DeepEqual(expected, actual) {
			t.Fatalf("projection mismatch: %s", out)
		}
	}
}
