package media

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

func toapisImageModelSupported(upstream string) bool {
	return upstream == "gpt-image-2" || upstream == "gpt-image-2.5-flare" || upstream == "gpt-image-2.5-sunburst"
}

func toapisImageRatios(upstream string) []string {
	ratios := []string{"1:1", "3:2", "2:3", "4:3", "3:4", "5:4", "4:5", "16:9", "9:16"}
	if upstream == "gpt-image-2" {
		return append(ratios, "2:1", "1:2", "21:9", "9:21")
	}
	return append(ratios, "21:9")
}

func validateToAPIsImageModel(m Model) error {
	if m.Kind != "image" || !toapisImageModelSupported(m.UpstreamModel) || len(m.Resolutions) != 1 || !slices.Contains([]string{"1K", "2K", "4K"}, m.Resolutions[0]) || m.GenerateAudio || m.VideoReference != nil || len(m.Durations) != 0 || m.MinDuration != 0 || m.MaxDuration != 0 {
		return ErrInvalid
	}
	return nil
}

func validateToAPIsImageInput(m Model, r CanvasRequest, images int) error {
	if validateToAPIsImageModel(m) != nil || r.Resolution != m.Resolutions[0] || !slices.Contains(toapisImageRatios(m.UpstreamModel), r.AspectRatio) || r.Duration != 0 || len(r.VideoReferences) > 0 || images < 0 || images > MaxReferenceImages(m) {
		return ErrInvalid
	}
	return nil
}

// 图片可能同步返回 URL，也可能只确认异步任务；两种受理证据不能混入同一个字符串。
type ProviderImageSubmission struct {
	URL    string
	TaskID string
}

type ProviderImageResult struct {
	Status string
	URL    string
	Error  providerErrorResponse
}

func firstFrameResolution(m Model) string {
	if m.Adapter == "toapis" && toapisImageModelSupported(m.UpstreamModel) && len(m.Resolutions) == 1 {
		return m.Resolutions[0]
	}
	return "2K"
}

func (p Provider) SubmitImage(ctx context.Context, m Model, prompt, image string, ratios ...string) (ProviderImageSubmission, error) {
	if p.Adapter != "toapis" {
		url, err := p.Image(ctx, m, prompt, image, ratios...)
		return ProviderImageSubmission{URL: url}, err
	}
	ratio := "1:1"
	if len(ratios) > 0 {
		ratio = ratios[0]
	}
	var images []string
	if image != "" {
		images = []string{image}
	}
	return p.SubmitCanvasImage(ctx, m, CanvasRequest{Prompt: prompt, Resolution: firstFrameResolution(m), AspectRatio: ratio}, images)
}

func (p Provider) SubmitCanvasImage(ctx context.Context, m Model, r CanvasRequest, images []string) (ProviderImageSubmission, error) {
	if p.Adapter != "toapis" {
		url, err := p.CanvasImage(ctx, m, r, images)
		return ProviderImageSubmission{URL: url}, err
	}
	if validateToAPIsImageInput(m, r, len(images)) != nil || strings.TrimSpace(r.Prompt) == "" {
		return ProviderImageSubmission{}, ErrInvalid
	}
	resolution := r.Resolution
	if m.UpstreamModel == "gpt-image-2" {
		resolution = strings.ToLower(resolution)
	}
	// size 在此协议中是比例；复用 Ark 的像素尺寸或提示词后缀会改变请求语义。
	body := map[string]any{"model": m.UpstreamModel, "prompt": r.Prompt, "resolution": resolution, "size": r.AspectRatio, "n": 1}
	if m.UpstreamModel == "gpt-image-2" {
		body["response_format"] = "url"
	} else {
		body["quality"] = "high"
	}
	if len(images) > 0 {
		for _, image := range images {
			if !ValidURL(image) {
				return ProviderImageSubmission{}, ErrInvalid
			}
		}
		body["reference_images"] = images
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := p.request(ctx, "POST", "/v1/images/generations", body, &out); err != nil {
		return ProviderImageSubmission{}, err
	}
	if providerTaskID(out.ID) == "" {
		return ProviderImageSubmission{}, fmt.Errorf("provider_task_missing")
	}
	return ProviderImageSubmission{TaskID: out.ID}, nil
}

func (p Provider) QueryImage(ctx context.Context, id string) (ProviderImageResult, error) {
	if p.Adapter != "toapis" || providerTaskID(id) == "" {
		return ProviderImageResult{}, ErrInvalid
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
	}
	if err := p.request(ctx, "GET", "/v1/images/generations/"+id, nil, &out); err != nil {
		return ProviderImageResult{}, err
	}
	if out.ID != id {
		return ProviderImageResult{}, fmt.Errorf("provider_invalid_response")
	}
	result := ProviderImageResult{Error: out.Error}
	switch out.Status {
	case "pending", "queued":
		result.Status = "queued"
	case "in_progress":
		result.Status = "running"
	case "failed":
		result.Status = "failed"
	case "completed":
		if out.Result.Type != "image" || len(out.Result.Data) != 1 || !ValidURL(out.Result.Data[0].URL) {
			return ProviderImageResult{}, fmt.Errorf("provider_image_missing")
		}
		result.Status, result.URL = "succeeded", out.Result.Data[0].URL
	default:
		result.Status = "unknown"
	}
	return result, nil
}
