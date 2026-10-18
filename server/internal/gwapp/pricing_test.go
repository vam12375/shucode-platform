package gwapp

import (
	"math"
	"testing"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestPriceRatesPeakAndOffPeak(t *testing.T) {
	shanghai := time.FixedZone("Asia/Shanghai", 8*3600)
	model := &store.PlatformModelRecord{
		InputPricePerMtok: "2", OutputPricePerMtok: "8",
		CacheReadPricePerMtok: "0.04", CacheWritePricePerMtok: "0",
		OffPeakMultiplier: "0.5",
	}
	peak := priceRates(model, time.Date(2026, 10, 19, 10, 0, 0, 0, shanghai), nil)
	if peak.InputPerMtok != 2 || peak.OutputPerMtok != 8 || math.Abs(peak.CacheReadPerMtok-0.04) > 1e-9 {
		t.Fatalf("高峰价不符: %+v", peak)
	}
	offPeak := priceRates(model, time.Date(2026, 10, 19, 20, 0, 0, 0, shanghai), nil)
	if offPeak.InputPerMtok != 1 || offPeak.OutputPerMtok != 4 || math.Abs(offPeak.CacheReadPerMtok-0.02) > 1e-9 {
		t.Fatalf("空闲价应减半: %+v", offPeak)
	}
	// 节假日按空闲价。
	holidays := map[string]bool{"2026-10-19": true}
	holiday := priceRates(model, time.Date(2026, 10, 19, 10, 0, 0, 0, shanghai), holidays)
	if holiday.OutputPerMtok != 4 {
		t.Fatalf("节假日应按空闲价: %+v", holiday)
	}
	// 倍率为 1 的模型不受时段影响。
	model.OffPeakMultiplier = "1"
	unchanged := priceRates(model, time.Date(2026, 10, 19, 20, 0, 0, 0, shanghai), nil)
	if unchanged.InputPerMtok != 2 || unchanged.OutputPerMtok != 8 {
		t.Fatalf("倍率 1 不应打折: %+v", unchanged)
	}
}
