package media

import (
	"fmt"
	"math"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"
)

func ValidURL(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}
func FindModel(models []Model, id, kind string) (Model, error) {
	for _, m := range models {
		if m.ID == id && m.Kind == kind {
			return m, nil
		}
	}
	return Model{}, fmt.Errorf("model_not_configured")
}
func ValidateModels(models []Model) error {
	seen := map[string]bool{}
	displayNames := map[string]string{}
	for _, m := range models {
		if len(m.Routes) > 32 {
			return ErrInvalid
		}
		routeProviders := map[string]bool{}
		for _, route := range m.Routes {
			if strings.TrimSpace(route.ProviderID) == "" || route.ProviderID != strings.TrimSpace(route.ProviderID) || routeProviders[route.ProviderID] || route.Priority < 0 || route.Priority > 1_000_000 || route.Weight < 1 || route.Weight > 10_000 {
				return ErrInvalid
			}
			routeProviders[route.ProviderID] = true
		}
		if m.DisplayName != "" && (strings.TrimSpace(m.DisplayName) != m.DisplayName || len([]rune(m.DisplayName)) > 120) {
			return ErrInvalid
		}
		group := strings.Join([]string{m.Kind, m.ProviderID, m.Adapter, m.UpstreamModel}, "\x00")
		if previous, exists := displayNames[group]; exists && previous != m.DisplayName {
			return ErrInvalid
		}
		displayNames[group] = m.DisplayName
		unit := m.EffectiveBillingUnit()
		priceMissing := m.PriceMicro <= 0 && !(m.Kind == "video" && unit == BillingUnitToken && m.TokenPriceMicroPerMillion > 0)
		if strings.TrimSpace(m.ID) == "" || m.Name == "" || seen[m.ID] || m.ProviderID == "" || m.UpstreamModel == "" || (m.Adapter != "ark" && m.Adapter != "toapis") || m.PriceVersion == "" || m.PriceMicro < 0 || priceMissing || m.PriceMicro > 100_000_000 || m.CostMicro < 0 || m.CostMicro > 100_000_000 {
			return ErrInvalid
		}
		if m.Kind == "image" && unit != "image" || m.Kind == "video" && unit != BillingUnitSecond && unit != BillingUnitRequest && unit != BillingUnitToken {
			return ErrInvalid
		}
		if m.TokenPriceMicroPerMillion < 0 || m.TokenPriceMicroPerMillion > 1_000_000_000 || m.TokenCostMicroPerMillion < 0 || m.TokenCostMicroPerMillion > 1_000_000_000 {
			return ErrInvalid
		}
		if unit != BillingUnitToken && (m.TokenPriceMicroPerMillion != 0 || m.TokenCostMicroPerMillion != 0) {
			return ErrInvalid
		}
		if unit == BillingUnitRequest && m.PriceMicro <= 0 || unit == BillingUnitToken && m.TokenPriceMicroPerMillion <= 0 {
			return ErrInvalid
		}
		if m.Adapter == "toapis" && validateToAPIsModel(m) != nil {
			return ErrInvalid
		}
		seen[m.ID] = true
		if m.Kind != "image" && m.Kind != "video" {
			return ErrInvalid
		}
		if m.VideoReference != nil {
			c := m.VideoReference
			maxReferenceImages := 8
			if m.Adapter == "toapis" && m.UpstreamModel == "kling-4.0" {
				maxReferenceImages = 10
			}
			if m.Kind != "video" || c.MaxDurationSeconds < 1 || c.MaxDurationSeconds > MaxReferenceSeconds || c.MaxImages < 1 || c.MaxImages > maxReferenceImages || c.MaxSegments < 0 || c.MaxSegments > 30 || c.PriceMicro < 0 || c.PriceMicro > 100_000_000 || c.CostMicro < 0 || c.CostMicro > 100_000_000 || c.TokenPriceMicroPerMillion < 0 || c.TokenPriceMicroPerMillion > 1_000_000_000 || c.TokenCostMicroPerMillion < 0 || c.TokenCostMicroPerMillion > 1_000_000_000 || (c.PriceMicro == 0 && c.TokenPriceMicroPerMillion == 0) {
				return ErrInvalid
			}
			// MaxAudioSeconds 为 0 表示不发参考音频；非零时不小于 2 秒且不超过片段上限，
			// 否则会出现「视频合法但音频必然超限」的区间。
			if c.MaxAudioSeconds < 0 || c.MaxAudioSeconds > c.MaxDurationSeconds || c.MaxAudioSeconds == 1 {
				return ErrInvalid
			}
			// ToAPIs 的多模态协议没有参考音频槽位，声明了能力却静默丢弃会造成误导。
			if m.Adapter == "toapis" && c.MaxAudioSeconds > 0 {
				return ErrInvalid
			}
		}
		if m.Kind == "image" && m.GenerateAudio {
			return ErrInvalid
		}
		if m.Kind == "video" {
			if len(m.Resolutions) == 0 {
				return ErrInvalid
			}
			if m.MinDuration > 0 || m.MaxDuration > 0 {
				if m.MinDuration < 2 || m.MaxDuration > 30 || m.MinDuration > m.MaxDuration {
					return ErrInvalid
				}
			} else if len(m.Durations) == 0 {
				return ErrInvalid
			}
		}
		for _, d := range m.Durations {
			if d < 2 || d > 30 {
				return ErrInvalid
			}
		}
		for _, r := range m.Resolutions {
			if (m.Kind == "image" && !slices.Contains(imageTiers(m), r)) || (m.Kind == "video" && m.Adapter != "toapis" && r != "480p" && r != "720p" && r != "1080p" && r != "4k") {
				return ErrInvalid
			}
		}
	}
	return nil
}
func ValidateBrief(b Brief, models []Model, assets []Asset) error {
	replica := b.ReferenceAssetID != ""
	if err := validateFinishingOptions(b); err != nil {
		return err
	}
	if (b.Mode != "auto" && b.Mode != "replicate") || (b.GenerationMode != "" && b.GenerationMode != "storyboard" && b.GenerationMode != "video_reference") {
		return ErrInvalid
	}
	if err := validateBriefReferenceMode(b); err != nil {
		return err
	}
	if strings.TrimSpace(b.Product) == "" || utf8.RuneCountInString(b.Product) > 200 || utf8.RuneCountInString(b.SellingPoints) > 4000 || (!replica && len(b.AssetIDs) == 0) || len(b.AssetIDs) > 20 || len(b.ReferenceURLs) > 10 || b.MaxCreditsMicro <= 0 || b.MaxCreditsMicro > 1_000_000_000 || (!replica && b.Duration < 5) || b.Duration < 1 || b.Duration > 120 || utf8.RuneCountInString(b.ReplacementInstructions) > 4000 {
		return ErrInvalid
	}
	if replica {
		if b.Mode != "replicate" || !b.ReviewPlan || !finitePositive(b.ReferenceDurationSeconds) || b.ReferenceDurationSeconds > MaxReferenceSeconds || (len(b.AssetIDs) == 0 && b.ImageModel == "" && b.ReferenceMode != "multimodal") {
			return ErrInvalid
		}
		found := false
		for _, a := range assets {
			if a.ID == b.ReferenceAssetID && a.Kind == "video" && math.Abs(a.DurationSeconds-b.ReferenceDurationSeconds) < .001 {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("reference_asset_not_found")
		}
	}
	if !replica && b.Mode == "replicate" && len(b.ReferenceURLs) == 0 {
		return ErrInvalid
	}
	for _, u := range b.ReferenceURLs {
		if !ValidURL(u) {
			return ErrInvalid
		}
	}
	if !replica && len(b.Platforms) == 0 {
		return ErrInvalid
	}
	// 创作意图来自客户端可选字段：长度与枚举必须与共享 schema 一致，
	// 否则客户端认为合法的输入会被这里静默拒绝。variantCount 只在平台声明 planVariants 后使用。
	if utf8.RuneCountInString(b.Audience) > 200 || utf8.RuneCountInString(b.CallToAction) > 200 || utf8.RuneCountInString(b.AvoidClaims) > 2000 || utf8.RuneCountInString(b.Script) > 4000 || b.VariantCount < 0 || b.VariantCount > 3 {
		return ErrInvalid
	}
	if b.Tone != "" && !slices.Contains([]string{"professional", "lifestyle", "energetic", "warm", "luxury", "playful"}, b.Tone) {
		return ErrInvalid
	}
	if b.HookStyle != "" && !slices.Contains([]string{"question", "pain_point", "result_first", "contrast", "story"}, b.HookStyle) {
		return ErrInvalid
	}
	if b.NarrationLanguage != "" && !slices.Contains([]string{"zh-CN", "en-US", "ja-JP", "ko-KR"}, b.NarrationLanguage) {
		return ErrInvalid
	}
	if len(b.Platforms) > 3 {
		return ErrInvalid
	}
	platforms := make(map[string]bool, len(b.Platforms))
	for _, p := range b.Platforms {
		if p != "douyin" && p != "xiaohongshu" && p != "bilibili" {
			return ErrInvalid
		}
		if platforms[p] {
			return ErrInvalid
		}
		platforms[p] = true
	}
	m, e := FindModel(models, b.VideoModel, "video")
	if e != nil {
		return e
	}
	if m.Adapter == "toapis" && validateToAPIsModel(m) != nil {
		return ErrInvalid
	}
	// 多镜头合成必须有固定画幅；auto 仅允许画布直接输出，不得落入默认竖屏分支。
	if b.AspectRatio == "auto" || !slices.Contains(ModelAspectRatios(m), b.AspectRatio) {
		return ErrInvalid
	}
	if b.GenerationMode == "video_reference" {
		if !replica || !b.ReviewPlan {
			return ErrInvalid
		}
		if m.VideoReference == nil {
			return fmt.Errorf("video_reference_unsupported")
		}
	}
	if err := validateBriefReferenceImages(b, m); err != nil {
		return err
	}
	if !slices.Contains(m.Resolutions, b.Resolution) {
		return ErrInvalid
	}
	possible := make([]bool, b.Duration+1)
	possible[0] = true
	allowed := m.AllowedDurations()
	for i := 1; i <= b.Duration; i++ {
		for _, d := range allowed {
			if d <= i && possible[i-d] {
				possible[i] = true
			}
		}
	}
	if !replica && !possible[b.Duration] {
		return fmt.Errorf("duration_not_supported")
	}
	if b.ImageModel != "" {
		image, imageErr := FindModel(models, b.ImageModel, "image")
		if imageErr != nil {
			return imageErr
		}
		if !slices.Contains(ModelAspectRatios(image), b.AspectRatio) || image.Adapter == "toapis" && validateToAPIsImageModel(image) != nil {
			return ErrInvalid
		}
	}
	for _, id := range b.AssetIDs {
		found := false
		for _, a := range assets {
			if a.ID == id && a.Kind == "image" && !a.Internal {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("asset_not_found")
		}
	}
	return nil
}
func BuildShots(b Brief, plan Plan, models []Model) ([]Shot, int64, error) {
	if err := validateBriefReferenceMode(b); err != nil {
		return nil, 0, err
	}
	if err := validateFinishingOptions(b); err != nil {
		return nil, 0, err
	}
	// 审阅草稿允许用户修正文案；结构与报价仍校验，生成准入再严格检查完整独白。
	if !b.ReviewPlan {
		if err := validateFinishingPlan(b, plan); err != nil {
			return nil, 0, err
		}
	}
	replica := b.ReferenceAssetID != ""
	wholeReference := replica && b.GenerationMode == "video_reference"
	if len(plan.Shots) == 0 || len(plan.Shots) > 30 || len(plan.References) == 0 || len(plan.References) > 20 {
		return nil, 0, ErrInvalid
	}
	refs := map[string]bool{}
	for _, r := range plan.References {
		validSource := ValidURL(r.URL) || (replica && r.URL == "" && len(b.ReferenceURLs) == 0)
		if !validSource || r.Title == "" || r.Evidence == "" || r.SelectionReason == "" || r.CapturedAt.IsZero() {
			return nil, 0, ErrInvalid
		}
		if utf8.RuneCountInString(r.Title) > 300 || utf8.RuneCountInString(r.Evidence) > 3000 || utf8.RuneCountInString(r.SelectionReason) > 1000 || (r.Likes != nil && *r.Likes < 0) {
			return nil, 0, ErrInvalid
		}
		refs[r.URL] = true
	}
	v, e := FindModel(models, b.VideoModel, "video")
	if e != nil {
		return nil, 0, e
	}
	if err := validateBriefReferenceImages(b, v); err != nil {
		return nil, 0, err
	}
	globalReferences := b.ReferenceMode == "multimodal"
	out := []Shot{}
	var total int64
	duration := 0
	sourceEnd := float64(0)
	for _, p := range plan.Shots {
		if utf8.RuneCountInString(p.Prompt) > 4000 || utf8.RuneCountInString(p.ImagePrompt) > 4000 || utf8.RuneCountInString(p.Subtitle) > 300 || utf8.RuneCountInString(p.Narration) > 300 || utf8.RuneCountInString(p.Camera) > 500 || utf8.RuneCountInString(p.Framing) > 500 || utf8.RuneCountInString(p.Action) > 1000 || utf8.RuneCountInString(p.Purpose) > 500 {
			return nil, 0, ErrInvalid
		}
		validShotSource := refs[p.SourceURL] || (replica && p.SourceURL == "" && len(b.ReferenceURLs) == 0 && refs[""])
		if !validShotSource || math.IsNaN(p.SourceStart) || math.IsInf(p.SourceStart, 0) || !finitePositive(p.SourceEnd) || p.SourceStart < 0 || p.SourceEnd <= p.SourceStart || (!wholeReference && !v.DurationSupported(p.Duration)) || (wholeReference && (p.Duration < 2 || p.Duration > 30)) || ((!replica && !globalReferences || p.AssetID != "") && !slices.Contains(b.AssetIDs, p.AssetID)) || p.Prompt == "" || len(p.Prompt) > 12000 || len(p.Subtitle) > 900 || p.Framing == "" || p.Camera == "" || p.Action == "" || p.Purpose == "" {
			return nil, 0, ErrInvalid
		}
		if replica {
			if math.Abs(p.SourceStart-sourceEnd) > .001 || p.SourceEnd > b.ReferenceDurationSeconds+.001 || p.SourceEnd-p.SourceStart < 1.0/30 || (!wholeReference && p.SourceEnd-p.SourceStart > float64(p.Duration)+.001) || (!wholeReference && !globalReferences && p.AssetID == "" && (b.ImageModel == "" || p.ImagePrompt == "")) || (p.Transition != "" && p.Transition != "cut" && p.Transition != "fade") || utf8.RuneCountInString(p.TransitionDescription) > 1000 || utf8.RuneCountInString(p.Subject) > 1000 || utf8.RuneCountInString(p.Scene) > 1000 || utf8.RuneCountInString(p.Sound) > 1000 {
				return nil, 0, ErrInvalid
			}
			if p.SourceURL != "" && len(b.ReferenceURLs) > 0 && !slices.Contains(b.ReferenceURLs, p.SourceURL) {
				return nil, 0, ErrInvalid
			}
			sourceEnd = p.SourceEnd
		}
		if wholeReference {
			// 亚秒镜头属于审阅事实，供应商的输入限制只约束下方派生的完整原片。
			continue
		}
		if globalReferences {
			// 制作单的全局图片是参考集合；历史镜头文字或绑定不能重新开启首帧阶段。
			p.AssetID, p.ImagePrompt = "", ""
		}
		var q int64
		if b.GenerationMode == "video_reference" {
			if v.VideoReference == nil {
				return nil, 0, fmt.Errorf("video_reference_unsupported")
			}
			if p.SourceEnd-p.SourceStart < float64(MinReferenceClipSeconds)-.001 || p.SourceEnd-p.SourceStart > float64(v.VideoReference.MaxDurationSeconds)+.001 {
				return nil, 0, fmt.Errorf("video_reference_duration_unsupported")
			}
			q = mediaVideoReferenceQuoteMicro(v, v.VideoReference, b.Resolution, b.AspectRatio, p.Duration)
		} else {
			q = mediaVideoQuoteMicro(v, b.Resolution, b.AspectRatio, p.Duration)
		}
		if p.ImagePrompt != "" && b.ImageModel != "" {
			im, e := FindModel(models, b.ImageModel, "image")
			if e != nil {
				return nil, 0, e
			}
			q += im.PriceMicro
		}
		total += q
		duration += p.Duration
		out = append(out, Shot{Plan: p, Status: "pending", Attempt: 1, QuoteMicro: q})
	}
	if wholeReference {
		if math.Abs(sourceEnd-b.ReferenceDurationSeconds) > .001 {
			return nil, 0, fmt.Errorf("video_reference_plan_invalid")
		}
		return buildWholeReferenceExecution(b, plan, v, models)
	}
	if (!replica && duration != b.Duration) || (replica && math.Abs(sourceEnd-b.ReferenceDurationSeconds) > .001) || total > b.MaxCreditsMicro {
		return nil, 0, fmt.Errorf("duration_or_budget_exceeded")
	}
	return out, total, nil
}

func finitePositive(v float64) bool { return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }
