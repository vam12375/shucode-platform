package media

import (
	"context"
	"fmt"
	"os"
)

const MaxCanvasVideoBytes int64 = 50 << 20
const MaxCanvasAudioBytes int64 = 15 << 20

func (s *Service) validateCanvasMultimodal(ctx context.Context, team string, m Model, r CanvasRequest) ([]string, error) {
	if err := validateCanvasOptions(m, r); err != nil {
		return nil, err
	}
	c := CanvasCapabilitiesFor(m)
	if c == nil {
		return nil, ErrInvalid
	}
	assets, err := s.Repo.Assets(ctx, team)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	find := func(id, kind string) (Asset, error) {
		if seen[id] {
			return Asset{}, ErrInvalid
		}
		seen[id] = true
		for _, a := range assets {
			if a.ID == id && a.Kind == kind && !a.Internal {
				return a, nil
			}
		}
		return Asset{}, fmt.Errorf("canvas_reference_asset_invalid")
	}
	for _, id := range r.AssetIDs {
		a, e := find(id, "image")
		if e != nil {
			return nil, e
		}
		if a.Size <= 0 || a.Size > MaxUpload {
			return nil, fmt.Errorf("canvas_reference_size_limit")
		}
		if a.Mime != "image/png" && a.Mime != "image/jpeg" && a.Mime != "image/webp" {
			return nil, fmt.Errorf("canvas_reference_format_invalid")
		}
		if info, e := os.Stat(s.Blobs.Path(team, a.ID)); e != nil || info.Size() != a.Size {
			return nil, fmt.Errorf("canvas_reference_asset_invalid")
		}
	}
	videos := make([]string, 0, len(r.VideoReferences))
	videoSeconds := 0.0
	for _, ref := range r.VideoReferences {
		a, e := find(ref.AssetID, "video")
		if e != nil {
			return nil, e
		}
		if ref.StartSeconds != nil || ref.EndSeconds != nil {
			if ref.StartSeconds == nil || ref.EndSeconds == nil || ref.ReferenceClipAssetID == "" {
				return nil, fmt.Errorf("video_reference_clip_required")
			}
			source, e := s.resolveVideoReference(ctx, team, ref.AssetID, ref.StartSeconds, ref.EndSeconds, c.MaxVideoSeconds)
			if e != nil {
				return nil, e
			}
			a, e = s.validateUploadedReferenceClip(ctx, team, source, ref.ReferenceClipAssetID)
			if e != nil {
				return nil, e
			}
		}
		if a.Size <= 0 || a.Size > MaxCanvasVideoBytes || !finitePositive(a.DurationSeconds) || a.DurationSeconds < 2 {
			return nil, fmt.Errorf("canvas_reference_duration_limit")
		}
		if a.Mime != "video/mp4" && a.Mime != "video/quicktime" {
			return nil, fmt.Errorf("canvas_reference_format_invalid")
		}
		if info, e := os.Stat(s.Blobs.Path(team, a.ID)); e != nil || info.Size() != a.Size {
			return nil, fmt.Errorf("canvas_reference_asset_invalid")
		}
		videoSeconds += a.DurationSeconds
		videos = append(videos, a.ID)
	}
	audioSeconds := 0.0
	for _, id := range r.AudioAssetIDs {
		a, e := find(id, "audio")
		if e != nil {
			return nil, e
		}
		if a.Size <= 0 || a.Size > MaxCanvasAudioBytes || !finitePositive(a.DurationSeconds) || a.DurationSeconds < 2 {
			return nil, fmt.Errorf("canvas_reference_duration_limit")
		}
		if a.Mime != "audio/wav" && a.Mime != "audio/mpeg" {
			return nil, fmt.Errorf("canvas_reference_format_invalid")
		}
		if info, e := os.Stat(s.Blobs.Path(team, a.ID)); e != nil || info.Size() != a.Size {
			return nil, fmt.Errorf("canvas_reference_asset_invalid")
		}
		audioSeconds += a.DurationSeconds
	}
	if videoSeconds > float64(c.MaxVideoSeconds) || audioSeconds > float64(c.MaxAudioSeconds) {
		return nil, fmt.Errorf("canvas_reference_duration_limit")
	}
	return videos, nil
}

func referenceContent(prompt string, images, videos, audios []string) []any {
	out := []any{map[string]any{"type": "text", "text": prompt}}
	for _, group := range []struct {
		kind, role string
		urls       []string
	}{{"image", "reference_image", images}, {"video", "reference_video", videos}, {"audio", "reference_audio", audios}} {
		for _, url := range group.urls {
			out = append(out, map[string]any{"type": group.kind + "_url", group.kind + "_url": map[string]string{"url": url}, "role": group.role})
		}
	}
	return out
}

func (p Provider) CanvasMultimodalVideo(ctx context.Context, m Model, r CanvasRequest, images, videos, audios []string) (string, error) {
	if err := validateCanvasOptions(m, r); err != nil {
		return "", err
	}
	c := CanvasCapabilitiesFor(m)
	if c == nil || len(images) > c.MaxImages || len(videos) > c.MaxVideos || len(audios) > c.MaxAudios || len(images)+len(videos)+len(audios) > c.MaxTotalReferences || len(audios) > 0 && len(images)+len(videos) == 0 {
		return "", ErrInvalid
	}
	for _, urls := range [][]string{images, videos, audios} {
		for _, url := range urls {
			if !ValidURL(url) {
				return "", ErrInvalid
			}
		}
	}
	body := map[string]any{"model": m.UpstreamModel, "duration": r.Duration, "resolution": r.Resolution, "watermark": false}
	path := "/contents/generations/tasks"
	if p.Adapter == "toapis" {
		path = "/v1/videos/generations"
		body["prompt"] = r.Prompt
		body["aspect_ratio"] = providerVideoRatio(r.AspectRatio)
		for _, group := range []struct {
			kind string
			urls []string
		}{{"image", images}, {"video", videos}, {"audio", audios}} {
			if len(group.urls) > 0 {
				items := []map[string]string{}
				for _, url := range group.urls {
					items = append(items, map[string]string{"url": url, "role": "reference_" + group.kind})
				}
				body[group.kind+"_with_roles"] = items
			}
		}
		if m.UpstreamModel == "kling-4.0" && len(videos) > 0 {
			body["metadata"] = map[string]bool{"keep_original_sound": true}
		}
	} else if p.Adapter == "ark" || p.Adapter == "" {
		body["content"] = referenceContent(r.Prompt, images, videos, audios)
		body["ratio"] = providerVideoRatio(r.AspectRatio)
	} else {
		return "", ErrInvalid
	}
	// 只有明确支持请求级音频开关的型号才发送 generate_audio；
	// H3/Kling 等型号没有该参数，发送 false 也可能被上游判为非法字段。
	capability, _ := toapisModelCapabilities(m.UpstreamModel)
	if p.Adapter != "toapis" || capability.nativeAudioToggle {
		body["generate_audio"] = m.GenerateAudio
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
	if err := p.request(ctx, "POST", path, body, &out); err != nil {
		return "", err
	}
	if out.ID == "" || p.Adapter == "toapis" && publicVideoTaskID(out.ID) == "" {
		return "", fmt.Errorf("provider_task_missing")
	}
	return out.ID, nil
}
