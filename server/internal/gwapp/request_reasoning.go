package gwapp

import (
	"math"
)

// 只观测实际请求参数；预算和模型名称不能证明某个语义档位，也不证明供应商已执行。
func requestReasoning(spec protocolSpec, body map[string]any) (string, *int64) {
	readEffort := func(value any) string {
		effort, ok := value.(string)
		if !ok || len(effort) == 0 || len(effort) > 64 {
			return ""
		}
		for _, char := range effort {
			if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-') {
				return ""
			}
		}
		return effort
	}
	var effort string
	if spec.routePath == "/v1/responses" {
		reasoning, _ := body["reasoning"].(map[string]any)
		effort = readEffort(reasoning["effort"])
	} else if spec.family == "anthropic" {
		output, _ := body["output_config"].(map[string]any)
		effort = readEffort(output["effort"])
	} else {
		effort = readEffort(body["reasoning_effort"])
	}
	if spec.routePath == "/v1/responses" {
		return effort, nil
	}
	thinking, _ := body["thinking"].(map[string]any)
	mode, _ := thinking["type"].(string)
	if mode != "enabled" && mode != "disabled" && mode != "adaptive" {
		return effort, nil
	}
	if effort == "" {
		effort = mode
	}
	if mode == "disabled" {
		return effort, nil
	}
	value, ok := thinking["budget_tokens"].(float64)
	if !ok || value <= 0 || value > 9007199254740991 || math.Trunc(value) != value {
		return effort, nil
	}
	budget := int64(value)
	return effort, &budget
}

func sourceAnalysisReasoning(spec protocolSpec, body map[string]any) string {
	effort, _ := requestReasoning(spec, body)
	if spec.routePath != "/v1/responses" {
		thinking, _ := body["thinking"].(map[string]any)
		if thinking["type"] == "disabled" || body["enable_thinking"] == false {
			return ""
		}
	}
	return effort
}
