package media

import "slices"

type toapisModelCapability struct {
	resolutions       []string
	minDuration       int
	videoReference    bool
	nativeAudioToggle bool
	referenceMaxBytes int64
}

func toapisMiniMaxModel(upstream string) bool {
	return upstream == "MiniMax-H3" || upstream == "MiniMax-H3-Max" || upstream == "MiniMax-H3-Max-Turbo"
}

func toapisModelCapabilities(upstream string) (toapisModelCapability, bool) {
	switch upstream {
	case "seedance-2":
		return toapisModelCapability{[]string{"480p", "720p", "1080p", "4k"}, 4, true, true, 100 << 20}, true
	case "seedance-2-fast", "seedance-2-mini":
		return toapisModelCapability{[]string{"480p", "720p"}, 4, true, true, 100 << 20}, true
	case "MiniMax-H3":
		return toapisModelCapability{[]string{"768p", "2K"}, 4, true, false, 50_000_000}, true
	case "MiniMax-H3-Max":
		return toapisModelCapability{[]string{"480p", "768p"}, 5, true, false, 50_000_000}, true
	case "MiniMax-H3-Max-Turbo":
		return toapisModelCapability{[]string{"480p", "768p"}, 5, false, false, 0}, true
	default:
		return toapisModelCapability{}, false
	}
}

func toapisResolutionSupported(upstream, resolution string) bool {
	capability, ok := toapisModelCapabilities(upstream)
	return ok && slices.Contains(capability.resolutions, resolution)
}

func toapisWireResolution(upstream, resolution string) string {
	if upstream == "MiniMax-H3-Max" || upstream == "MiniMax-H3-Max-Turbo" {
		switch resolution {
		case "480p":
			return "480P"
		case "768p":
			return "768P"
		}
	}
	return resolution
}

func validateToAPIsInput(m Model, resolution, ratio string, duration, images int, video bool) error {
	capability, ok := toapisModelCapabilities(m.UpstreamModel)
	if !ok || validateToAPIsModel(m) != nil || duration < capability.minDuration || duration > 15 || !m.DurationSupported(duration) || !toapisResolutionSupported(m.UpstreamModel, resolution) || !slices.Contains(m.Resolutions, resolution) || !slices.Contains(ModelAspectRatios(m), ratio) {
		return ErrInvalid
	}
	if video {
		if !capability.videoReference || m.VideoReference == nil || images < 1 || images > m.VideoReference.MaxImages || images > 8 {
			return ErrInvalid
		}
	} else if images > 2 || toapisMiniMaxModel(m.UpstreamModel) && images == 0 && ratio == "auto" {
		return ErrInvalid
	}
	return nil
}
