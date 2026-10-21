package gwapp

import (
	"math"

	"github.com/vam12375/shucode-platform/server/internal/credits"
)

func validPriceRates(rates credits.ModelPriceRates) bool {
	for _, v := range []float64{rates.InputPerMtok, rates.OutputPerMtok, rates.CacheReadPerMtok, rates.CacheWritePerMtok} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return false
		}
	}
	return true
}
