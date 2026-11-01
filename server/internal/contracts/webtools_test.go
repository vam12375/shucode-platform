package contracts

import (
	"encoding/json"
	"os"
	"testing"
)

func TestWebToolsCapabilityFixtures(t *testing.T) {
	raw, err := os.ReadFile("testdata/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Cases []struct {
			Input json.RawMessage `json:"input"`
			OK    bool            `json:"ok"`
		} `json:"webToolsCapabilities"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures.Cases) == 0 {
		t.Fatal("missing web tools fixtures")
	}
	for i, tc := range fixtures.Cases {
		_, err := ParseWebToolsCapabilities(tc.Input)
		if (err == nil) != tc.OK {
			t.Fatalf("fixture %d: accepted=%v want %v", i, err == nil, tc.OK)
		}
	}
}
