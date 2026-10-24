package media

import "math"

// Ark Seedance 视频 token 估算：24fps，按输出分辨率像素计。
// 实际结算始终以任务查询的 usage.completion_tokens 为准；这里只用于报价上限。
const videoTokenFPS = 24

// Ark 官方 Seedance 2.0 输出像素矩阵。旧实现把正方形压成短边、其他比例按16:9估算，造成报价失真。
// 固定比例合成和参考报价共用此表；auto 用同档最大面积作上限，实际结算仍只认 usage。
var videoSizes = map[string]map[string][2]int{
	"480p":  {"16:9": {864, 496}, "4:3": {752, 560}, "1:1": {640, 640}, "3:4": {560, 752}, "9:16": {496, 864}, "21:9": {992, 432}},
	"720p":  {"16:9": {1280, 720}, "4:3": {1112, 834}, "1:1": {960, 960}, "3:4": {834, 1112}, "9:16": {720, 1280}, "21:9": {1470, 630}},
	"1080p": {"16:9": {1920, 1080}, "4:3": {1664, 1248}, "1:1": {1440, 1440}, "3:4": {1248, 1664}, "9:16": {1080, 1920}, "21:9": {2206, 946}},
	"4k":    {"16:9": {3840, 2160}, "4:3": {3326, 2494}, "1:1": {2880, 2880}, "3:4": {2494, 3326}, "9:16": {2160, 3840}, "21:9": {4398, 1886}},
}

func videoTokenSize(resolution, ratio string) (int, int) {
	sizes, ok := videoSizes[resolution]
	if !ok {
		return 0, 0
	}
	if ratio == "auto" {
		best := [2]int{}
		for _, candidate := range sizes {
			if candidate[0]*candidate[1] > best[0]*best[1] {
				best = candidate
			}
		}
		return best[0], best[1]
	}
	size := sizes[ratio]
	return size[0], size[1]
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
