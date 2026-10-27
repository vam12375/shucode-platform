package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/upstreamurl"
)

// Adapter 来自制作单冻结模型；密钥只在服务端解密。
type Provider struct {
	BaseURL, Key string
	Adapter      string
	Client       *http.Client
}

func (p Provider) request(ctx context.Context, method, path string, body any, out any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	endpoint := strings.TrimRight(p.BaseURL, "/") + path
	if p.Adapter == "toapis" {
		// 控制台会规范化掉 /v1；复用上游 URL 拼接，兼容服务根与代理版本前缀。
		endpoint = upstreamurl.Join(p.BaseURL, path)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error providerErrorResponse `json:"error"`
		}
		_ = json.Unmarshal(raw, &failure)
		failureResult := &providerHTTPError{status: resp.StatusCode, detail: sanitizeProviderError(failure.Error.Code)}
		if resp.StatusCode == http.StatusTooManyRequests {
			failureResult.retryAfterAt = providerRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		}
		return failureResult
	}
	if err = json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("provider_invalid_response: %w", err)
	}
	return nil
}
func (p Provider) Image(ctx context.Context, m Model, prompt, image string, aspectRatios ...string) (string, error) {
	if p.Adapter != "" && p.Adapter != "ark" {
		return "", ErrInvalid
	}
	var out struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	// 原请求显式开启了供应商的可见 AI 水印；首帧图片也需关闭，避免标记进入后续视频参考。
	size := "2K"
	if len(aspectRatios) > 0 && aspectRatios[0] != "" {
		// 原首帧忽略 Brief 比例；复用画布图片参数归一，避免新增视频比例与首帧形状脱节。
		size, prompt = canvasImageParameters(m, CanvasRequest{Prompt: prompt, Resolution: "2K", AspectRatio: aspectRatios[0]})
	}
	body := map[string]any{"model": m.UpstreamModel, "prompt": prompt, "response_format": "url", "size": size, "watermark": false}
	// 无参考图时按脚本生成首帧；空 image 并非供应商合法的文生图参数。
	if image != "" {
		body["image"] = image
	}
	e := p.request(ctx, "POST", "/images/generations", body, &out)
	if e != nil {
		return "", e
	}
	if len(out.Data) != 1 || !ValidURL(out.Data[0].URL) {
		return "", fmt.Errorf("provider_image_missing")
	}
	return out.Data[0].URL, nil
}
func videoSubmitBody(m Model, b Brief, s ShotPlan, image string, referenceVideo ...string) map[string]any {
	video := ""
	if len(referenceVideo) > 0 && referenceVideo[0] != "" {
		video = referenceVideo[0]
	}
	return videoSubmitBodyWithImages(m, b, s, []string{image}, video)
}

func videoContent(prompt string, images []string, referenceVideo string, referenceAudio ...string) []any {
	content := []any{map[string]any{"type": "text", "text": prompt}}
	for i, image := range images {
		role := "first_frame"
		if referenceVideo != "" {
			role = "reference_image"
		} else if i == 1 {
			role = "last_frame"
		}
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{"url": image}, "role": role})
	}
	if referenceVideo != "" {
		content = append(content, map[string]any{"type": "video_url", "video_url": map[string]string{"url": referenceVideo}, "role": "reference_video"})
	}
	// 参考音频与原片同源，用于保住卡点；缺失时只是降级，不改变其他 content 项。
	if len(referenceAudio) > 0 && referenceAudio[0] != "" {
		content = append(content, map[string]any{"type": "audio_url", "audio_url": map[string]string{"url": referenceAudio[0]}, "role": "reference_audio"})
	}
	return content
}

func videoSubmitBodyWithImages(m Model, b Brief, s ShotPlan, images []string, referenceVideo string, referenceAudio ...string) map[string]any {
	// 原 true 会启用供应商可见水印；制作视频统一显式关闭，保留原首帧和视频参考语义。
	body := map[string]any{"model": m.UpstreamModel, "content": videoContent(s.Prompt, images, referenceVideo, referenceAudio...), "resolution": b.Resolution, "ratio": providerVideoRatio(b.AspectRatio), "duration": s.Duration, "watermark": false}
	// 只在模型显式开启时下发；Seedance 1.0 等不支持该参数的模型保持原请求不变。
	if m.GenerateAudio {
		// 新后期拥有唯一声音主轨；支持原生音频的模型必须显式关掉，省略会沿用上游默认。
		body["generate_audio"] = b.Finishing == nil
	}
	return body
}

func (p Provider) Submit(ctx context.Context, m Model, b Brief, s ShotPlan, image string) (string, error) {
	return p.SubmitWithReference(ctx, m, b, s, image, "")
}

func (p Provider) SubmitWithReference(ctx context.Context, m Model, b Brief, s ShotPlan, image, referenceVideo string) (string, error) {
	return p.SubmitWithReferenceImages(ctx, m, b, s, []string{image}, referenceVideo)
}

func (p Provider) SubmitWithReferenceImages(ctx context.Context, m Model, b Brief, s ShotPlan, images []string, referenceVideo string, referenceAudio ...string) (string, error) {
	if len(images) == 0 {
		return "", fmt.Errorf("first_frame_required")
	}
	for _, image := range images {
		if image == "" {
			return "", fmt.Errorf("first_frame_required")
		}
	}
	if referenceVideo != "" && (m.VideoReference == nil || len(images) > m.VideoReference.MaxImages) {
		return "", fmt.Errorf("video_reference_unsupported")
	}
	if p.Adapter == "toapis" {
		// ToAPIs 的多模态协议没有参考音频槽位，目录校验已拒绝该能力声明。
		if b.Finishing != nil {
			m.GenerateAudio = false
		}
		return p.submitToAPIs(ctx, m, CanvasRequest{Prompt: s.Prompt, Resolution: b.Resolution, AspectRatio: b.AspectRatio, Duration: s.Duration}, images, referenceVideo)
	}
	if p.Adapter != "" && p.Adapter != "ark" {
		return "", ErrInvalid
	}
	var out struct {
		ID string `json:"id"`
	}
	err := p.request(ctx, "POST", "/contents/generations/tasks", videoSubmitBodyWithImages(m, b, s, images, referenceVideo, referenceAudio...), &out)
	if err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("provider_task_missing")
	}
	return out.ID, nil
}

type ProviderResult struct {
	Status  string                `json:"status"`
	Error   providerErrorResponse `json:"error"`
	Content struct {
		VideoURL     string `json:"video_url"`
		LastFrameURL string `json:"last_frame_url"`
	} `json:"content"`
	Usage struct {
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
	} `json:"usage"`
}

func (p Provider) Query(ctx context.Context, id string) (ProviderResult, error) {
	var out ProviderResult
	if p.Adapter == "toapis" {
		return p.queryToAPIs(ctx, id)
	}
	if p.Adapter != "" && p.Adapter != "ark" {
		return out, ErrInvalid
	}
	if strings.ContainsAny(id, "/?#") {
		return out, ErrInvalid
	}
	e := p.request(ctx, "GET", "/contents/generations/tasks/"+id, nil, &out)
	return out, e
}

func (p Provider) CanvasImage(ctx context.Context, m Model, r CanvasRequest, images []string) (string, error) {
	if p.Adapter != "" && p.Adapter != "ark" {
		return "", ErrInvalid
	}
	var out struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	size, prompt := canvasImageParameters(m, r)
	// 原请求的 true 会把“AI生成”直接烘焙进图片；显式关闭，不能仅省略并依赖上游默认值。
	body := map[string]any{"model": m.UpstreamModel, "prompt": prompt, "size": size, "response_format": "url", "watermark": false}
	if len(images) == 1 {
		body["image"] = images[0]
	} else if len(images) > 1 {
		body["image"] = images
	}
	if e := p.request(ctx, "POST", "/images/generations", body, &out); e != nil {
		return "", e
	}
	if len(out.Data) != 1 || !ValidURL(out.Data[0].URL) {
		return "", fmt.Errorf("provider_image_missing")
	}
	return out.Data[0].URL, nil
}
func (p Provider) CanvasVideo(ctx context.Context, m Model, r CanvasRequest, images []string, referenceVideos ...string) (string, error) {
	if err := validateCanvasOptions(m, r); err != nil {
		return "", err
	}
	video := ""
	if len(referenceVideos) > 0 && referenceVideos[0] != "" {
		if m.VideoReference == nil || len(images) == 0 || len(images) > m.VideoReference.MaxImages {
			return "", fmt.Errorf("video_reference_unsupported")
		}
		video = referenceVideos[0]
	}
	if p.Adapter == "toapis" {
		return p.submitToAPIs(ctx, m, r, images, video)
	}
	if p.Adapter != "" && p.Adapter != "ark" {
		return "", ErrInvalid
	}
	// 原 true 会把供应商水印写入视频；画布各生成模式也需显式关闭，不能依赖默认值。
	body := map[string]any{"model": m.UpstreamModel, "content": videoContent(r.Prompt, images, video), "resolution": r.Resolution, "ratio": providerVideoRatio(r.AspectRatio), "duration": r.Duration, "watermark": false}
	if m.GenerateAudio {
		body["generate_audio"] = true
		if r.GenerateAudio != nil {
			body["generate_audio"] = *r.GenerateAudio
		}
	}
	if r.ReturnLastFrame != nil {
		body["return_last_frame"] = *r.ReturnLastFrame
	}
	var out struct {
		ID string `json:"id"`
	}
	if e := p.request(ctx, "POST", "/contents/generations/tasks", body, &out); e != nil {
		return "", e
	}
	if out.ID == "" {
		return "", fmt.Errorf("provider_task_missing")
	}
	return out.ID, nil
}
