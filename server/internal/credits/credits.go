// Package credits:packages/contracts/src/credits.ts 的镜像。
// 内部一律整数 micro-credits(1 credit = 1_000_000 micro);价格按「每 1M token
// 的 credit 数」,计价乘积由 float64 承载后四舍五入——必须与 TS 的 Number 语义
// 逐位一致,否则同一请求在两套后端下结算出的费用会漂移。
package credits

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const MicroCreditsPerCredit = 1_000_000

type ModelPriceRates struct {
	InputPerMtok      float64
	OutputPerMtok     float64
	CacheReadPerMtok  float64
	CacheWritePerMtok float64
}

type UsageTokenCounts struct {
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
}

func ComputeUsageMicroCredits(usage UsageTokenCounts, rates ModelPriceRates) (int64, error) {
	raw := float64(usage.InputTokens)*rates.InputPerMtok +
		float64(usage.OutputTokens)*rates.OutputPerMtok +
		float64(usage.CacheReadTokens)*rates.CacheReadPerMtok +
		float64(usage.CacheCreationTokens)*rates.CacheWritePerMtok
	if math.IsNaN(raw) || math.IsInf(raw, 0) || raw < 0 {
		return 0, fmt.Errorf("非法用量计价输入: %v", raw)
	}
	return round(raw), nil
}

// 请求准入预留:按请求声明的 max_tokens 全额预留输出侧成本。
func EstimateMaxMicroCredits(maxOutputTokens int64, outputPerMtok float64) int64 {
	raw := math.Max(0, math.Floor(float64(maxOutputTokens))) * outputPerMtok
	return round(raw)
}

// EstimateReservationMicroCredits:准入预留同时覆盖输入估算与输出上限，
// 避免只预留输出导致输入成本完全不受准入额度约束。
func EstimateReservationMicroCredits(inputTokens, outputTokens int64, rates ModelPriceRates) int64 {
	raw := math.Max(0, math.Floor(float64(inputTokens)))*rates.InputPerMtok +
		math.Max(0, math.Floor(float64(outputTokens)))*rates.OutputPerMtok
	if math.IsNaN(raw) || math.IsInf(raw, 0) || raw < 0 {
		return 0
	}
	return round(raw)
}

// FormatMicroCredits:micro → 定点 credit 字符串(numeric(18,6) 口径)。
// 与 TS (micro/1e6).toFixed(6) 等价的整型实现(无浮点误差)。
func FormatMicroCredits(micro int64) string {
	negative := micro < 0
	if negative {
		micro = -micro
	}
	whole := micro / MicroCreditsPerCredit
	frac := micro % MicroCreditsPerCredit
	s := fmt.Sprintf("%d.%06d", whole, frac)
	if negative {
		return "-" + s
	}
	return s
}

// ParseCreditsToMicro:numeric(18,6) 字符串 → micro(直接整数换算,不经 float)。
// TS parseInt/round 产物对合法输入一致;离谱输入(如科学计数法)走 float 兜底。
func ParseCreditsToMicro(value string) (int64, error) {
	v := strings.TrimSpace(value)
	if micro, err := strconv.ParseInt(v, 10, 64); err == nil {
		return micro * MicroCreditsPerCredit, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("非法 credit 数值: %s", value)
	}
	return round(f * MicroCreditsPerCredit), nil
}

// round 复刻 Math.round:半数向上(Floor(x+0.5)),与 Go 的 math.Round
// (半数远离零)和 RoundToEven 都不同,负数侧行为也必须一致。
func round(f float64) int64 {
	return int64(math.Floor(f + 0.5))
}
