package media

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

var imageRatios = []string{"auto", "1:1", "4:3", "3:4", "16:9", "9:16", "3:2", "2:3", "21:9"}
var fixedVideoRatios = []string{"16:9", "4:3", "1:1", "3:4", "9:16", "21:9"}

// 官方 ratio 能力依赖真实模型，原 kind 三项常量遗漏了 Seedance 的固定比例与 adaptive。
// 目录、控制台和准入共用此入口；未知型号保持历史集合，不能凭名称相似推测开放新任务能力。
func ModelAspectRatios(m Model) []string {
	if m.Kind == "image" {
		if m.Adapter == "toapis" && toapisImageModelSupported(m.UpstreamModel) {
			return toapisImageRatios(m.UpstreamModel)
		}
		return slices.Clone(imageRatios)
	}
	if m.Kind == "video" {
		upstream := strings.TrimSpace(m.UpstreamModel)
		if m.Adapter == "toapis" && (toapisSeedanceModel(upstream) || toapisMiniMaxModel(upstream)) {
			return append([]string{"auto"}, fixedVideoRatios...)
		}
		if m.Adapter == "ark" {
			if seedance20Model(upstream) {
				return append([]string{"auto"}, fixedVideoRatios...)
			}
			if datedModelFamily(upstream, []string{"doubao-seedance-1-0-pro-fast-", "doubao-seedance-1-0-pro-"}) {
				return slices.Clone(fixedVideoRatios)
			}
		}
		return []string{"16:9", "9:16", "1:1"}
	}
	return []string{}
}

func seedance20Model(upstream string) bool {
	return datedModelFamily(upstream, []string{"doubao-seedance-2-0-mini-", "doubao-seedance-2-0-fast-", "doubao-seedance-2-0-"})
}

func datedModelFamily(upstream string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if suffix, ok := strings.CutPrefix(upstream, prefix); ok && len(suffix) == 6 {
			valid := true
			for _, digit := range suffix {
				valid = valid && digit >= '0' && digit <= '9'
			}
			if valid {
				return true
			}
		}
	}
	return false
}

func providerVideoRatio(ratio string) string {
	if ratio == "auto" {
		return "adaptive"
	}
	return ratio
}

func nearestFixedVideoRatio(width, height int) string {
	if width <= 0 || height <= 0 {
		return "9:16"
	}
	best, distance := "9:16", math.Inf(1)
	for _, ratio := range fixedVideoRatios {
		var w, h float64
		_, _ = fmt.Sscanf(ratio, "%f:%f", &w, &h)
		next := math.Abs(math.Log((float64(width) / float64(height)) / (w / h)))
		if next < distance {
			best, distance = ratio, next
		}
	}
	return best
}

// 图片能力在平台归一化，公开目录、准入与 adapter 共用，避免客户端选择上游拒绝的档位。
func imageTiers(m Model) []string {
	switch {
	case m.Adapter == "toapis" && toapisImageModelSupported(m.UpstreamModel):
		return []string{"1K", "2K", "4K"}
	case strings.Contains(m.UpstreamModel, "seedream-5-0-pro"):
		return []string{"1K", "2K"}
	case strings.Contains(m.UpstreamModel, "seedream-5-0"):
		return []string{"2K", "3K", "4K"}
	case strings.Contains(m.UpstreamModel, "seedream-4-5"):
		return []string{"2K", "4K"}
	case strings.Contains(m.UpstreamModel, "seedream-4-0"):
		return []string{"1K", "2K", "4K"}
	case strings.Contains(m.UpstreamModel, "seedream-3-0"):
		return []string{"1K"}
	default:
		return []string{"1K", "2K", "3K", "4K"}
	}
}

func ImageResolutions(m Model) []string {
	if m.Adapter == "toapis" && validateToAPIsImageModel(m) != nil {
		return []string{}
	}
	tiers := imageTiers(m)
	if len(m.Resolutions) > 0 {
		out := []string{}
		for _, r := range m.Resolutions {
			if slices.Contains(tiers, r) && !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
		return out
	}
	if !strings.Contains(m.UpstreamModel, "seedream-") {
		return []string{"2K"}
	}
	return tiers
}

func MaxReferenceImages(m Model) int {
	if m.Kind == "video" {
		return 2
	}
	if strings.Contains(m.UpstreamModel, "seedream-3-0") {
		return 0
	}
	return 8
}

func canvasImageParameters(m Model, r CanvasRequest) (string, string) {
	if strings.Contains(m.UpstreamModel, "seedream-3-0") {
		sizes := map[string]string{"auto": "1024x1024", "1:1": "1024x1024", "4:3": "1152x864", "3:4": "864x1152", "16:9": "1280x720", "9:16": "720x1280", "3:2": "1248x832", "2:3": "832x1248", "21:9": "1512x648"}
		return sizes[r.AspectRatio], r.Prompt
	}
	prompt := r.Prompt
	if r.AspectRatio != "auto" {
		prompt += "（画面比例 " + r.AspectRatio + "）"
	}
	return r.Resolution, prompt
}
