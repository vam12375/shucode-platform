// Package credits:packages/contracts/src/credits.ts 的镜像。
// 内部一律整数 micro-credits(1 credit = 1_000_000 micro);价格按「每 1M token
// 的 credit 数」,计价乘积由 float64 承载后四舍五入——必须与 TS 的 Number 语义
// 逐位一致,否则同一请求在两套后端下结算出的费用会漂移。
package credits

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

const MicroCreditsPerCredit = 1_000_000
const maxInt64Value = int64(1<<63 - 1)
const minInt64Value = int64(-1 << 63)

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
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CacheReadTokens < 0 || usage.CacheCreationTokens < 0 {
		return 0, fmt.Errorf("用量 token 数不能为负数")
	}
	if !validRates(rates) {
		return 0, fmt.Errorf("模型单价无效")
	}
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
	if math.IsNaN(outputPerMtok) || math.IsInf(outputPerMtok, 0) || outputPerMtok < 0 {
		return maxInt64Value
	}
	raw := math.Max(0, math.Floor(float64(maxOutputTokens))) * outputPerMtok
	return round(raw)
}

// EstimateReservationMicroCredits:准入预留同时覆盖输入估算与输出上限，
// 避免只预留输出导致输入成本完全不受准入额度约束。
func EstimateReservationMicroCredits(inputTokens, outputTokens int64, rates ModelPriceRates) int64 {
	if !validRates(rates) {
		return maxInt64Value
	}
	inputRate := math.Max(rates.InputPerMtok, math.Max(rates.CacheReadPerMtok, rates.CacheWritePerMtok))
	raw := math.Max(0, math.Floor(float64(inputTokens)))*inputRate +
		math.Max(0, math.Floor(float64(outputTokens)))*rates.OutputPerMtok
	if math.IsNaN(raw) || math.IsInf(raw, 0) || raw < 0 {
		return maxInt64Value
	}
	return round(raw)
}

func validRates(rates ModelPriceRates) bool {
	for _, rate := range []float64{rates.InputPerMtok, rates.OutputPerMtok, rates.CacheReadPerMtok, rates.CacheWritePerMtok} {
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
			return false
		}
	}
	return true
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

// ParseCreditsToMicro 使用十进制定点运算，避免浮点在大额余额上丢失 micro 精度。
func ParseCreditsToMicro(value string) (int64, error) {
	v := strings.TrimSpace(value)
	invalid := func() (int64, error) { return 0, fmt.Errorf("非法或溢出 credit 数值") }
	if len(v) == 0 || len(v) > 128 {
		return invalid()
	}
	for _, c := range v {
		if !(c >= '0' && c <= '9') && !strings.ContainsRune("+-.eE", c) {
			return invalid()
		}
	}
	if i := strings.IndexAny(v, "eE"); i >= 0 {
		exponent, err := strconv.Atoi(v[i+1:])
		if err != nil || exponent < -100 || exponent > 100 {
			return invalid()
		}
	}
	amount, ok := new(big.Rat).SetString(v)
	if !ok {
		return invalid()
	}
	amount.Mul(amount, big.NewRat(MicroCreditsPerCredit, 1))
	// 与 Math.round 一致：floor(x + 0.5)，负数也不能使用截断或远离零舍入。
	amount.Add(amount, big.NewRat(1, 2))
	q, r := new(big.Int), new(big.Int)
	q.QuoRem(amount.Num(), amount.Denom(), r)
	if r.Sign() < 0 {
		q.Sub(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return invalid()
	}
	return q.Int64(), nil
}

// round 复刻 Math.round:半数向上(Floor(x+0.5)),与 Go 的 math.Round
// (半数远离零)和 RoundToEven 都不同,负数侧行为也必须一致。
func round(f float64) int64 {
	if f >= float64(maxInt64Value) {
		return maxInt64Value
	}
	if f <= float64(minInt64Value) {
		return minInt64Value
	}
	return int64(math.Floor(f + 0.5))
}
