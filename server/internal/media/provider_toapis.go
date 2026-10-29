package media

import (
	"context"
	"fmt"
)

func toapisSeedanceModel(upstream string) bool {
	return upstream == "seedance-2" || upstream == "seedance-2-fast" || upstream == "seedance-2-mini" || upstream == "seedance-2.5-ext" || upstream == "seedance-2-5"
}

// toapisVideoModel lists the video models with an explicitly implemented
// ToAPIs adapter.  Keep this separate from toapisSeedanceModel: the latter is
// also used by the Seedance-only duration normalizer and must not make
// unrelated models look like Seedance.
func toapisVideoModel(upstream string) bool {
	return toapisSeedanceModel(upstream) || toapisMiniMaxModel(upstream) || upstream == "kling-4.0"
}

func validateToAPIsModel(m Model) error {
	if m.Kind == "image" {
		return validateToAPIsImageModel(m)
	}
	capability, ok := toapisModelCapabilities(m.UpstreamModel)
	if m.Kind != "video" || !ok || m.GenerateAudio && !capability.nativeAudioToggle {
		return ErrInvalid
	}
	for _, resolution := range m.Resolutions {
		if !toapisResolutionSupported(m.UpstreamModel, resolution) {
			return ErrInvalid
		}
	}
	if m.MinDuration != 0 || m.MaxDuration != 0 {
		if m.MinDuration < capability.minDuration || m.MaxDuration > capability.maxDuration || m.MinDuration > m.MaxDuration {
			return ErrInvalid
		}
	}
	for _, duration := range m.Durations {
		if duration < capability.minDuration || duration > capability.maxDuration {
			return ErrInvalid
		}
	}
	unit := m.EffectiveBillingUnit()
	if unit != BillingUnitSecond && unit != BillingUnitRequest && unit != BillingUnitToken {
		return ErrInvalid
	}
	if unit == BillingUnitRequest && m.PriceMicro <= 0 {
		return ErrInvalid
	}
	if unit == BillingUnitToken && m.TokenPriceMicroPerMillion <= 0 {
		return ErrInvalid
	}
	if m.TokenPriceMicroPerMillion < 0 || m.TokenPriceMicroPerMillion > 1_000_000_000 || m.TokenCostMicroPerMillion < 0 || m.TokenCostMicroPerMillion > 1_000_000_000 {
		return ErrInvalid
	}
	if m.UpstreamModel == "seedance-2.5-ext" && unit != BillingUnitRequest || m.UpstreamModel == "seedance-2-5" && unit != BillingUnitToken {
		return ErrInvalid
	}
	if c := m.VideoReference; c != nil && (!capability.videoReference || c.MaxDurationSeconds > capability.maxDuration || (m.UpstreamModel != "seedance-2-5" && (c.TokenCostMicroPerMillion != 0 || c.TokenPriceMicroPerMillion != 0))) {
		return ErrInvalid
	}
	return nil
}

func (p Provider) submitToAPIs(ctx context.Context, m Model, r CanvasRequest, images []string, video string) (string, error) {
	if validateToAPIsInput(m, r.Resolution, r.AspectRatio, r.Duration, len(images), video != "") != nil {
		return "", ErrInvalid
	}
	if video != "" && !ValidURL(video) {
		return "", ErrInvalid
	}
	ratio := providerVideoRatio(r.AspectRatio)
	if toapisMiniMaxModel(m.UpstreamModel) && len(images) > 0 && video == "" {
		// H3 首尾帧按输入图片自适应；具体画幅会被上游忽略，不能沿用 Seedance 的固定比例参数。
		ratio = "adaptive"
	}
	body := map[string]any{"model": m.UpstreamModel, "prompt": r.Prompt, "resolution": toapisWireResolution(m.UpstreamModel, r.Resolution), "aspect_ratio": ratio, "duration": r.Duration}
	capability, _ := toapisModelCapabilities(m.UpstreamModel)
	if !capability.nativeAudioToggle {
		body["watermark"] = false
	} else {
		body["generate_audio"] = m.GenerateAudio
		if r.GenerateAudio != nil {
			body["generate_audio"] = *r.GenerateAudio
		}
	}
	if len(images) > 0 {
		media := make([]map[string]string, 0, len(images))
		for i, image := range images {
			if !ValidURL(image) {
				return "", ErrInvalid
			}
			role := "first_frame"
			if video != "" {
				role = "reference_image"
			} else if i == 1 {
				role = "last_frame"
			}
			media = append(media, map[string]string{"url": image, "role": role})
		}
		body["image_with_roles"] = media
	}
	if video != "" {
		body["video_with_roles"] = []map[string]string{{"url": video, "role": "reference_video"}}
		if m.UpstreamModel == "kling-4.0" {
			// Kling keeps the source video's soundtrack through its metadata
			// option; it is not the generic generate_audio switch.
			body["metadata"] = map[string]bool{"keep_original_sound": true}
		}
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := p.request(ctx, "POST", "/v1/videos/generations", body, &out); err != nil {
		return "", err
	}
	if publicVideoTaskID(out.ID) == "" {
		return "", fmt.Errorf("provider_task_missing")
	}
	return out.ID, nil
}

func (p Provider) queryToAPIs(ctx context.Context, id string) (ProviderResult, error) {
	var result ProviderResult
	if publicVideoTaskID(id) == "" {
		return result, ErrInvalid
	}
	var out struct {
		ID     string                `json:"id"`
		Status string                `json:"status"`
		Error  providerErrorResponse `json:"error"`
		Result struct {
			Type string `json:"type"`
			Data []struct {
				URL string `json:"url"`
			} `json:"data"`
		} `json:"result"`
		Usage struct {
			CompletionTokens int64 `json:"completion_tokens"`
			TotalTokens      int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := p.request(ctx, "GET", "/v1/videos/generations/"+id, nil, &out); err != nil {
		return result, err
	}
	if out.ID != id {
		return result, fmt.Errorf("provider_invalid_response")
	}
	result.Error = out.Error
	result.Usage.CompletionTokens = out.Usage.CompletionTokens
	result.Usage.TotalTokens = out.Usage.TotalTokens
	switch out.Status {
	case "queued", "failed":
		result.Status = out.Status
	case "in_progress":
		result.Status = "running"
	case "completed":
		if out.Result.Type != "video" || len(out.Result.Data) != 1 || !ValidURL(out.Result.Data[0].URL) {
			return ProviderResult{}, fmt.Errorf("provider_video_missing")
		}
		result.Status = "succeeded"
	default:
		// 供应商未知状态不能碰巧命中 Ark 归一化终态，避免未完成任务被当作成功结算。
		result.Status = "unknown"
	}
	if len(out.Result.Data) > 0 {
		result.Content.VideoURL = out.Result.Data[0].URL
	}
	return result, nil
}
