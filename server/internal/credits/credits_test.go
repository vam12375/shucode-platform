package credits

import (
	"encoding/json"
	"math"
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

func TestEstimateReservationUsesHighestInputRate(t *testing.T) {
	got := EstimateReservationMicroCredits(100, 20, ModelPriceRates{
		InputPerMtok: 3, OutputPerMtok: 15, CacheReadPerMtok: 0.3, CacheWritePerMtok: 3.75,
	})
	if got != 675 {
		t.Fatalf("预留必须按最高输入单价覆盖缓存写入: got=%d want=675", got)
	}
}

func TestInvalidPricingEstimateFailsClosed(t *testing.T) {
	got := EstimateReservationMicroCredits(100, 20, ModelPriceRates{InputPerMtok: 1, OutputPerMtok: math.NaN()})
	if got != maxInt64Value {
		t.Fatalf("非法单价不得产生零预留: %d", got)
	}
	if _, err := ComputeUsageMicroCredits(UsageTokenCounts{InputTokens: 1}, ModelPriceRates{InputPerMtok: math.Inf(1)}); err == nil {
		t.Fatal("非法单价的实扣计价应报错")
	}
}

func TestCreditParsingPreservesMicroPrecision(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int64
	}{
		{"999999999999.999999", 999999999999999999}, {"0.000001", 1}, {"-0.010000", -10000}, {"1e-6", 1},
	} {
		if got, err := ParseCreditsToMicro(tc.value); err != nil || got != tc.want {
			t.Fatalf("精确金额解析 %s: %d %v", tc.value, got, err)
		}
	}
	for _, bad := range []string{"9223372036854775808", "1e40", "NaN", "1/2"} {
		if _, err := ParseCreditsToMicro(bad); err == nil {
			t.Fatalf("应拒绝溢出/非法金额: %s", bad)
		}
	}
}
