package gwapp

import (
	"math"

	"github.com/vam12375/shucode-platform/server/internal/credits"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// 未在请求中显式携带的输入、后台生成和供应商托管工具没有可验证的费用上界，不能透传。
func boundedGeneration(body map[string]any) bool {
	for _, key := range []string{"previous_response_id", "conversation", "best_of", "prompt", "mcp_servers", "container", "context_management"} {
		if value, exists := body[key]; exists && value != nil {
			return false
		}
	}
	if value, exists := body["background"]; exists && value != false && value != nil {
		return false
	}
	if value, exists := body["modalities"]; exists {
		values, ok := value.([]any)
		if !ok {
			return false
		}
		for _, v := range values {
			if v != "text" {
				return false
			}
		}
	}
	var walk func(any) bool
	walk = func(value any) bool {
		switch v := value.(type) {
		case []any:
			for _, item := range v {
				if !walk(item) {
					return false
				}
			}
		case map[string]any:
			if typ, ok := v["type"].(string); ok {
				switch typ {
				case "image", "image_url", "input_image", "input_audio", "audio", "video", "input_video", "document", "file", "input_file", "file_reference", "item_reference":
					return false
				}
			}
			for key, item := range v {
				switch key {
				case "image_url", "file_id", "file_data", "input_audio", "audio_url", "video_url":
					return false
				}
				if !walk(item) {
					return false
				}
			}
		}
		return true
	}
	if !walk(body) {
		return false
	}
	if tools, exists := body["tools"]; exists && tools != nil {
		values, ok := tools.([]any)
		if !ok {
			return false
		}
		for _, value := range values {
			tool, ok := value.(map[string]any)
			if !ok {
				return false
			}
			// Anthropic 用户函数工具无 type 或 custom；OpenAI 使用 function。
			typ, _ := tool["type"].(string)
			if typ != "" && typ != "function" && typ != "custom" {
				return false
			}
		}
	}
	return true
}

func validPriceRates(rates credits.ModelPriceRates) bool {
	for _, v := range []float64{rates.InputPerMtok, rates.OutputPerMtok, rates.CacheReadPerMtok, rates.CacheWritePerMtok} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return false
		}
	}
	return true
}

func affordableOutputLimit(team *store.TeamRecord, promptTokens, count, requested int64, rates credits.ModelPriceRates) int64 {
	if team.Unlimited {
		return requested
	}
	quota, e1 := credits.ParseCreditsToMicro(team.QuotaCredits)
	used, e2 := credits.ParseCreditsToMicro(team.UsedCredits)
	reserved, e3 := credits.ParseCreditsToMicro(team.ReservedCredits)
	if e1 != nil || e2 != nil || e3 != nil {
		return requested
	} // 最终原子准入仍会拒绝无效账本。
	available := quota - used - reserved
	if available <= 0 {
		return requested
	}
	estimate := func(limit int64) int64 {
		return credits.EstimateReservationMicroCredits(promptTokens, limit*count, rates)
	}
	if estimate(requested) <= available || estimate(1) > available {
		return requested
	}
	lo, hi := int64(1), requested
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		if estimate(mid) <= available {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}
