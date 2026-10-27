package media

import "slices"

type CanvasCapabilities struct {
	Modes              []string `json:"modes"`
	MaxImages          int      `json:"maxImages"`
	MaxVideos          int      `json:"maxVideos"`
	MaxAudios          int      `json:"maxAudios"`
	MaxTotalReferences int      `json:"maxTotalReferences"`
	MaxVideoSeconds    int      `json:"maxVideoSeconds"`
	MaxAudioSeconds    int      `json:"maxAudioSeconds"`
	GenerateAudio      bool     `json:"generateAudio"`
	ReturnLastFrame    bool     `json:"returnLastFrame"`
}

// 能力来自明确适配的上游型号；旧目录的两图首尾帧能力不能冒充全能参考。
func CanvasCapabilitiesFor(m Model) *CanvasCapabilities {
	adapter := m.Adapter
	if adapter == "" {
		// 历史目录未写adapter时provider一直默认Ark，能力投影必须与真实执行协议一致。
		adapter = "ark"
	}
	if m.Kind != "video" || !(adapter == "ark" && seedance20Model(m.UpstreamModel) || adapter == "toapis" && toapisSeedanceModel(m.UpstreamModel)) {
		return nil
	}
	c := &CanvasCapabilities{Modes: []string{"frames", "multimodal"}, MaxImages: 9, MaxAudios: 3, MaxTotalReferences: 12, MaxAudioSeconds: 15, GenerateAudio: m.GenerateAudio, ReturnLastFrame: adapter == "ark"}
	// 含视频报价必须有已配置的真实费率，不能只因供应商支持就向UI公开可计费入口。
	if m.VideoReference != nil && (m.VideoReference.PriceMicro > 0 || m.VideoReference.TokenPriceMicroPerMillion > 0) {
		c.MaxVideos = 3
		c.MaxVideoSeconds = min(15, m.VideoReference.MaxDurationSeconds)
	}
	return c
}

func validateCanvasOptions(m Model, r CanvasRequest) error {
	if r.ReferenceMode == "" && r.GenerateAudio == nil && r.ReturnLastFrame == nil && len(r.AudioAssetIDs) == 0 {
		return nil
	}
	c := CanvasCapabilitiesFor(m)
	if r.Kind == "video" && (r.Duration != 0 && (r.Duration < 4 || r.Duration > 15)) {
		return ErrInvalid
	}
	if c == nil || r.ReferenceMode != "" && !slices.Contains(c.Modes, r.ReferenceMode) {
		return ErrInvalid
	}
	if r.GenerateAudio != nil && !c.GenerateAudio || r.ReturnLastFrame != nil && !c.ReturnLastFrame {
		return ErrInvalid
	}
	if r.ReferenceMode != "multimodal" && len(r.AudioAssetIDs) > 0 {
		return ErrInvalid
	}
	if r.ReferenceMode == "multimodal" && (len(r.AssetIDs) > c.MaxImages || len(r.VideoReferences) > c.MaxVideos || len(r.AudioAssetIDs) > c.MaxAudios || len(r.AssetIDs)+len(r.VideoReferences)+len(r.AudioAssetIDs) > c.MaxTotalReferences || len(r.AudioAssetIDs) > 0 && len(r.AssetIDs)+len(r.VideoReferences) == 0) {
		return ErrInvalid
	}
	return nil
}
