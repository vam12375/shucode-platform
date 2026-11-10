package media

import "slices"

type toapisModelCapability struct {
	resolutions       []string
	minDuration       int
	maxDuration       int
	videoReference    bool
	nativeAudioToggle bool
	referenceMaxBytes int64
	maxImages         int
	maxVideos         int
}

func toapisMiniMaxModel(upstream string) bool {
	return upstream == "MiniMax-H3" || upstream == "MiniMax-H3-Max" || upstream == "MiniMax-H3-Max-Turbo"
}

func toapisWanModel(upstream string) bool {
	return upstream == "wan3.0-video" || upstream == "wan3.0-video-prime"
}

func toapisVideoOnlyReference(m Model) bool {
	return m.Adapter == "toapis" && (m.UpstreamModel == "kling-4.0" || toapisWanModel(m.UpstreamModel))
}

func toapisModelCapabilities(upstream string) (toapisModelCapability, bool) {
	switch upstream {
	case "wan3.0-video", "wan3.0-video-prime":
		return toapisModelCapability{resolutions: []string{"480p", "720p", "1080p"}, minDuration: 2, maxDuration: 30, videoReference: true, nativeAudioToggle: true, referenceMaxBytes: 100 << 20, maxImages: 10, maxVideos: 5}, true
	case "seedance-2":
		return toapisModelCapability{resolutions: []string{"480p", "720p", "1080p", "4k"}, minDuration: 4, maxDuration: 15, videoReference: true, nativeAudioToggle: true, referenceMaxBytes: 100 << 20, maxImages: 2, maxVideos: 3}, true
	case "seedance-2-fast", "seedance-2-mini":
		return toapisModelCapability{resolutions: []string{"480p", "720p"}, minDuration: 4, maxDuration: 15, videoReference: true, nativeAudioToggle: true, referenceMaxBytes: 100 << 20, maxImages: 2, maxVideos: 3}, true
	case "seedance-2.5-ext":
		// Seedance 2.5 家族开放连续 4–30 秒；视频参考片段仍按平台既有 15 秒上限。
		return toapisModelCapability{resolutions: []string{"720p"}, minDuration: 4, maxDuration: 30, videoReference: true, nativeAudioToggle: true, referenceMaxBytes: 100 << 20, maxImages: 2, maxVideos: 3}, true
	case "seedance-2-5":
		return toapisModelCapability{resolutions: []string{"480p", "720p", "1080p"}, minDuration: 4, maxDuration: 30, videoReference: true, nativeAudioToggle: true, referenceMaxBytes: 100 << 20, maxImages: 2, maxVideos: 3}, true
	case "kling-4.0":
		// ToAPIs exposes Kling 4.0 as a generic video task.  It accepts
		// 720p/1080p output, continuous 3–30 second durations and an optional
		// reference video.  The model has no request-level audio toggle, so
		// callers must omit generate_audio (see provider_toapis.go).
		return toapisModelCapability{resolutions: []string{"720p", "1080p"}, minDuration: 3, maxDuration: 30, videoReference: true, nativeAudioToggle: false, referenceMaxBytes: 100 << 20, maxImages: 10, maxVideos: 5}, true
	case "MiniMax-H3":
		return toapisModelCapability{resolutions: []string{"768p", "2K"}, minDuration: 4, maxDuration: 15, videoReference: true, nativeAudioToggle: false, referenceMaxBytes: 50_000_000, maxImages: 2, maxVideos: 3}, true
	case "MiniMax-H3-Max":
		return toapisModelCapability{resolutions: []string{"480p", "768p"}, minDuration: 4, maxDuration: 15, videoReference: true, nativeAudioToggle: false, referenceMaxBytes: 50_000_000, maxImages: 2, maxVideos: 3}, true
	case "MiniMax-H3-Max-Turbo":
		return toapisModelCapability{resolutions: []string{"480p", "768p"}, minDuration: 4, maxDuration: 15, videoReference: false, nativeAudioToggle: false, referenceMaxBytes: 0, maxImages: 2, maxVideos: 0}, true
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
	if !ok || validateToAPIsModel(m) != nil || duration < capability.minDuration || duration > capability.maxDuration || !m.DurationSupported(duration) || !toapisResolutionSupported(m.UpstreamModel, resolution) || !slices.Contains(m.Resolutions, resolution) || !slices.Contains(ModelAspectRatios(m), ratio) {
		return ErrInvalid
	}
	if video {
		if !capability.videoReference || m.VideoReference == nil || images > m.VideoReference.MaxImages || images > capability.maxImages || images == 0 && !toapisVideoOnlyReference(m) {
			return ErrInvalid
		}
	} else if images > capability.maxImages || toapisMiniMaxModel(m.UpstreamModel) && images == 0 && ratio == "auto" {
		return ErrInvalid
	}
	if toapisWanModel(m.UpstreamModel) && !video && images > 2 {
		return ErrInvalid
	}
	return nil
}
