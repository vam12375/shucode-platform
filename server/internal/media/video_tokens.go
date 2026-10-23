package media

import "math"

// Ark Seedance 视频 token 估算：24fps，按输出分辨率像素计。
// 实际结算始终以任务查询的 usage.completion_tokens 为准；这里只用于报价上限。
const videoTokenFPS = 24

func videoTokenSize(resolution, ratio string) (int, int) {
	w, h := 864, 496
	switch resolution {
	case "720p":
		w, h = 1280, 720
	case "1080p":
		w, h = 1920, 1080
	case "4k":
		w, h = 3840, 2160
	}
	if ratio == "9:16" {
		w, h = h, w
	}
	if ratio == "1:1" {
		side := min(w, h)
		w, h = side, side
	}
	return w, h
}

func estimateVideoTokens(resolution, ratio string, inputSeconds, outputSeconds float64) int64 {
	w, h := videoTokenSize(resolution, ratio)
	raw := (inputSeconds + outputSeconds) * float64(w) * float64(h) * videoTokenFPS / 1024
	if math.IsNaN(raw) || math.IsInf(raw, 0) || raw <= 0 {
		return 0
	}
	return int64(math.Ceil(raw))
}

func videoTokenChargeMicro(tokens, rateMicroPerMillion int64) int64 {
	if tokens <= 0 || rateMicroPerMillion <= 0 {
		return 0
	}
	return (tokens*rateMicroPerMillion + 999_999) / 1_000_000
}

// videoReferenceQuoteMicro 使用 15 秒输入上限报价，覆盖 Ark 短输入最低 token 门槛；
// 实际结算会按 completion_tokens 重算，通常低于这个上限。
func videoReferenceQuoteMicro(ref *VideoReferenceCapability, resolution, ratio string, outputSeconds int) int64 {
	if ref == nil {
		return 0
	}
	if ref.TokenPriceMicroPerMillion > 0 {
		inputSeconds := float64(ref.MaxDurationSeconds)
		if inputSeconds <= 0 || inputSeconds > 15 {
			inputSeconds = 15
		}
		tokens := estimateVideoTokens(resolution, ratio, inputSeconds, float64(outputSeconds))
		return videoTokenChargeMicro(tokens, ref.TokenPriceMicroPerMillion)
	}
	return ref.PriceMicro * int64(outputSeconds)
}
