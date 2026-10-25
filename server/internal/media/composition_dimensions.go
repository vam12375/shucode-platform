package media

import (
	"context"
	"fmt"
	"math"
	"slices"
)

func (s *Service) compositionOutputSize(ctx context.Context, p *Project) (int, int, error) {
	m, err := FindModel(p.Models, p.Brief.VideoModel, "video")
	if err != nil {
		return 0, 0, err
	}
	if m.Adapter != "toapis" || !toapisMiniMaxModel(m.UpstreamModel) {
		w, h := outputSize(p.Brief.Resolution, p.Brief.AspectRatio)
		if w <= 0 || h <= 0 {
			return 0, 0, fmt.Errorf("unsupported_composition_aspect_ratio")
		}
		return w, h, nil
	}
	if !slices.Contains(fixedVideoRatios, p.Brief.AspectRatio) || len(p.Shots) == 0 || p.Shots[0].OutputAssetID == "" {
		return 0, 0, fmt.Errorf("invalid_composition_output")
	}
	probe := s.ReferenceProbe
	if probe == nil {
		probe = ProbeReference
	}
	metadata, err := probe(ctx, s.Blobs.Path(p.TeamID, p.Shots[0].OutputAssetID))
	if err != nil || metadata.Width < 2 || metadata.Height < 2 || metadata.Width > 16384 || metadata.Height > 16384 {
		return 0, 0, fmt.Errorf("invalid_composition_output")
	}
	// H3 未提供官方像素矩阵，首尾帧还会按图片自适应；合成依据真实片段短边与交付比例，不能套用 Ark token 尺寸。
	short := min(metadata.Width, metadata.Height) / 2 * 2
	var ratioW, ratioH float64
	_, _ = fmt.Sscanf(p.Brief.AspectRatio, "%f:%f", &ratioW, &ratioH)
	w, h := short, short
	if ratioW >= ratioH {
		w = int(math.Round(float64(short)*ratioW/ratioH/2)) * 2
	} else {
		h = int(math.Round(float64(short)*ratioH/ratioW/2)) * 2
	}
	if w < 2 || h < 2 || w > 16384 || h > 16384 {
		return 0, 0, fmt.Errorf("invalid_composition_output")
	}
	return w, h, nil
}
