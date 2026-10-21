package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Ark 使用供应商配置中的完整 API 前缀（例如 .../api/v3），密钥只在服务端解密。
type Provider struct {
	BaseURL, Key string
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
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.BaseURL, "/")+path, bytes.NewReader(data))
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
		return fmt.Errorf("provider_http_%d", resp.StatusCode)
	}
	if err = json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("provider_invalid_response: %w", err)
	}
	return nil
}
func (p Provider) Image(ctx context.Context, m Model, prompt, image string) (string, error) {
	var out struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	e := p.request(ctx, "POST", "/images/generations", map[string]any{"model": m.UpstreamModel, "prompt": prompt, "image": image, "response_format": "url", "size": "2K", "watermark": true}, &out)
	if e != nil {
		return "", e
	}
	if len(out.Data) != 1 || !ValidURL(out.Data[0].URL) {
		return "", fmt.Errorf("provider_image_missing")
	}
	return out.Data[0].URL, nil
}
func videoSubmitBody(m Model, b Brief, s ShotPlan, image string) map[string]any {
	body := map[string]any{"model": m.UpstreamModel, "content": []any{map[string]any{"type": "text", "text": s.Prompt}, map[string]any{"type": "image_url", "image_url": map[string]string{"url": image}, "role": "first_frame"}}, "resolution": b.Resolution, "ratio": b.AspectRatio, "duration": s.Duration, "watermark": true}
	// 只在模型显式开启时下发；Seedance 1.0 等不支持该参数的模型保持原请求不变。
	if m.GenerateAudio {
		body["generate_audio"] = true
	}
	return body
}

func (p Provider) Submit(ctx context.Context, m Model, b Brief, s ShotPlan, image string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	e := p.request(ctx, "POST", "/contents/generations/tasks", videoSubmitBody(m, b, s, image), &out)
	if e != nil {
		return "", e
	}
	if out.ID == "" {
		return "", fmt.Errorf("provider_task_missing")
	}
	return out.ID, nil
}

type ProviderResult struct {
	Status  string `json:"status"`
	Content struct {
		VideoURL string `json:"video_url"`
	} `json:"content"`
}

func (p Provider) Query(ctx context.Context, id string) (ProviderResult, error) {
	var out ProviderResult
	if strings.ContainsAny(id, "/?#") {
		return out, ErrInvalid
	}
	e := p.request(ctx, "GET", "/contents/generations/tasks/"+id, nil, &out)
	return out, e
}

func (p Provider) CanvasImage(ctx context.Context, m Model, r CanvasRequest, images []string) (string, error) {
	var out struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	size, prompt := canvasImageParameters(m, r)
	body := map[string]any{"model": m.UpstreamModel, "prompt": prompt, "size": size, "response_format": "url", "watermark": true}
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
func (p Provider) CanvasVideo(ctx context.Context, m Model, r CanvasRequest, images []string) (string, error) {
	content := []any{map[string]any{"type": "text", "text": r.Prompt}}
	for i, image := range images {
		role := "first_frame"
		if i == 1 {
			role = "last_frame"
		}
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{"url": image}, "role": role})
	}
	body := map[string]any{"model": m.UpstreamModel, "content": content, "resolution": r.Resolution, "ratio": r.AspectRatio, "duration": r.Duration, "watermark": true}
	if m.GenerateAudio {
		body["generate_audio"] = true
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
