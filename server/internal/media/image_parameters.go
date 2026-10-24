package media

import (
	"slices"
	"strings"
)

var imageRatios = []string{"auto", "1:1", "4:3", "3:4", "16:9", "9:16", "3:2", "2:3", "21:9"}
var videoRatios = []string{"16:9", "9:16", "1:1"}

// 目录与准入共用比例能力，避免客户端展示一个服务端实际拒绝的比例。
func CanvasAspectRatios(kind string) []string {
	if kind == "image" {
		return slices.Clone(imageRatios)
	}
	if kind == "video" {
		return slices.Clone(videoRatios)
	}
	return []string{}
}

// 图片能力在平台归一化，公开目录、准入与 adapter 共用，避免客户端选择上游拒绝的档位。
func imageTiers(m Model) []string {
	switch {
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
