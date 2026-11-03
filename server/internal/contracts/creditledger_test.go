package contracts

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestCreditLedgerPageFixtures(t *testing.T) {
	raw, err := os.ReadFile("testdata/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Cases []struct {
			Name   string          `json:"name"`
			Input  json.RawMessage `json:"input"`
			OK     bool            `json:"ok"`
			Output json.RawMessage `json:"output"`
		} `json:"creditLedgerPages"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures.Cases) == 0 {
		t.Fatal("missing credit ledger fixtures")
	}
	for _, tc := range fixtures.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			got, err := ParseCreditLedgerPage(tc.Input)
			if (err == nil) != tc.OK {
				t.Fatalf("accepted=%v want %v: %v", err == nil, tc.OK, err)
			}
			if !tc.OK {
				return
			}
			gotRaw, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var gotValue, wantValue any
			if err := json.Unmarshal(gotRaw, &gotValue); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(tc.Output, &wantValue); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotValue, wantValue) {
				t.Fatalf("normalized output mismatch:\n got %s\nwant %s", gotRaw, tc.Output)
			}
		})
	}
}

func TestCreditLedgerRejectsMalformedJSON(t *testing.T) {
	for _, raw := range []string{"null", "[]", "{}", "true", "{", `{"range":"all","entries":[],"nextCursor":null,"summary":null} {}`} {
		if _, err := ParseCreditLedgerPage([]byte(raw)); err == nil {
			t.Fatalf("accepted malformed page: %s", raw)
		}
	}
}
