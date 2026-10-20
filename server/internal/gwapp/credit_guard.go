package gwapp

import (
	"math"
	"strings"

	"github.com/vam12375/shucode-platform/server/internal/credits"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// 隐藏输入、远端媒体和托管工具仍可用，但必须先冻结团队全部可用余额。
// 仅检查真实消息输入，不能把工具 JSON Schema 中同名属性误认为媒体引用。
func requiresFullCreditPrecharge(body map[string]any) bool {
	for _, key := range []string{"previous_response_id", "conversation", "prompt", "mcp_servers", "container", "context_management", "best_of"} {
		if value, exists := body[key]; exists && value != nil && value != "" {
			return true
		}
	}
	if value, exists := body["background"]; exists && value != nil && value != false {
		return true
	}
	if modalities, ok := body["modalities"].([]any); ok {
		for _, modality := range modalities {
			if modality != "text" {
				return true
			}
		}
	}
	for _, key := range []string{"messages", "input"} {
		if containsMediaReference(body[key]) {
			return true
		}
	}
	if tools, ok := body["tools"].([]any); ok {
		for _, value := range tools {
			tool, ok := value.(map[string]any)
			if !ok {
				return true
			}
			typ, _ := tool["type"].(string)
			if typ != "" && !strings.EqualFold(typ, "function") && !strings.EqualFold(typ, "custom") {
				return true
			}
		}
	}
	return false
}

func containsMediaReference(value any) bool {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if containsMediaReference(item) {
				return true
			}
		}
	case map[string]any:
		if typ, ok := typed["type"].(string); ok {
			switch strings.ToLower(typ) {
			case "image", "image_url", "input_image", "input_audio", "audio", "video", "input_video", "document", "file", "input_file", "file_reference", "item_reference":
				return true
			}
		}
		for key, item := range typed {
			switch key {
			case "image_url", "file_id", "file_data", "input_audio", "audio_url", "video_url":
				return true
			}
			if containsMediaReference(item) {
				return true
			}
		}
	}
	return false
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
