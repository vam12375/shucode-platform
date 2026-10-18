package credits

import (
	"encoding/json"
	"os"
	"testing"
)

type fixturesFile struct {
	Credits struct {
		Format []struct {
			Micro int64  `json:"micro"`
			S     string `json:"s"`
		} `json:"format"`
		Parse []struct {
			V     string          `json:"v"`
			Micro json.RawMessage `json:"micro"`
		} `json:"parse"`
		Usage []struct {
			Usage UsageTokenCounts `json:"usage"`
			Rates ModelPriceRates  `json:"rates"`
			Micro int64            `json:"micro"`
		} `json:"usage"`
		Estimate []struct {
			MaxTokens int64   `json:"maxTokens"`
			Rate      float64 `json:"rate"`
			Micro     int64   `json:"micro"`
		} `json:"estimate"`
	} `json:"credits"`
}

func loadFixtures(t *testing.T) fixturesFile {
	t.Helper()
	raw, err := os.ReadFile("../contracts/testdata/fixtures.json")
	if err != nil {
		t.Fatalf("读取契约 fixtures: %v", err)
	}
	var f fixturesFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("解析 fixtures: %v", err)
	}
	return f
}

func TestFormatFixtures(t *testing.T) {
	for _, tc := range loadFixtures(t).Credits.Format {
		if got := FormatMicroCredits(tc.Micro); got != tc.S {
			t.Fatalf("格式化不符 micro=%d: got %s want %s", tc.Micro, got, tc.S)
		}
	}
}

func TestParseFixtures(t *testing.T) {
	for _, tc := range loadFixtures(t).Credits.Parse {
		var want int64
		if err := json.Unmarshal(tc.Micro, &want); err != nil {
			t.Fatalf("micro 解析: %v", err)
		}
		got, err := ParseCreditsToMicro(tc.V)
		if err != nil {
			t.Fatalf("解析 %q: %v", tc.V, err)
		}
		if got != want {
			t.Fatalf("解析不符 %q: got %d want %d", tc.V, got, want)
		}
	}
	if _, err := ParseCreditsToMicro("not-a-number"); err == nil {
		t.Fatal("非法数值应报错")
	}
}

func TestUsageFixtures(t *testing.T) {
	for i, tc := range loadFixtures(t).Credits.Usage {
		got, err := ComputeUsageMicroCredits(tc.Usage, tc.Rates)
		if err != nil {
			t.Fatalf("样本 %d: %v", i, err)
		}
		if got != tc.Micro {
			t.Fatalf("样本 %d 计价不符: got %d want %d", i, got, tc.Micro)
		}
	}
}

func TestEstimateFixtures(t *testing.T) {
	for _, tc := range loadFixtures(t).Credits.Estimate {
		if got := EstimateMaxMicroCredits(tc.MaxTokens, tc.Rate); got != tc.Micro {
			t.Fatalf("预留不符 max=%d rate=%v: got %d want %d", tc.MaxTokens, tc.Rate, got, tc.Micro)
		}
	}
}
