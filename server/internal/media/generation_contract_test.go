package media

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestGenerationContractMatchesHostFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/video-generation-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Plan          Plan   `json:"plan"`
		CandidateHash string `json:"candidateHash"`
		Durations     []struct {
			Total    int   `json:"total"`
			Model    Model `json:"model"`
			Expected []int `json:"expected"`
		} `json:"durations"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	hash, err := candidatePlanHash(fixture.Plan)
	if err != nil || hash != fixture.CandidateHash {
		t.Fatalf("cross-language hash mismatch: %s %v", hash, err)
	}
	for _, v := range fixture.Durations {
		if got := planGenerationDurations(v.Total, v.Model); !reflect.DeepEqual(got, v.Expected) {
			t.Fatalf("duration mismatch: %v want %v", got, v.Expected)
		}
	}
}
