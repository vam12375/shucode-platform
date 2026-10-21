package media

import (
	"fmt"
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
	for _, m := range models {
		if strings.TrimSpace(m.ID) == "" || m.Name == "" || seen[m.ID] || m.ProviderID == "" || m.UpstreamModel == "" || m.Adapter != "ark" || m.PriceVersion == "" || m.PriceMicro <= 0 || m.PriceMicro > 100_000_000 || m.CostMicro < 0 || m.CostMicro > 100_000_000 {
			return ErrInvalid
		}
		seen[m.ID] = true
		if m.Kind != "image" && m.Kind != "video" {
			return ErrInvalid
		}
		if m.Kind == "image" && m.GenerateAudio {
			return ErrInvalid
		}
		if m.Kind == "video" && (len(m.Durations) == 0 || len(m.Resolutions) == 0) {
			return ErrInvalid
		}
		for _, d := range m.Durations {
			if d < 2 || d > 30 {
				return ErrInvalid
			}
		}
		for _, r := range m.Resolutions {
			if (m.Kind == "image" && !slices.Contains(imageTiers(m), r)) || (m.Kind == "video" && r != "480p" && r != "720p" && r != "1080p" && r != "4k") {
				return ErrInvalid
			}
		}
	}
	return nil
}
func ValidateBrief(b Brief, models []Model, assets []Asset) error {
	if b.Mode != "auto" && b.Mode != "replicate" {
		return ErrInvalid
	}
	if strings.TrimSpace(b.Product) == "" || utf8.RuneCountInString(b.Product) > 200 || utf8.RuneCountInString(b.SellingPoints) > 4000 || len(b.AssetIDs) == 0 || len(b.AssetIDs) > 20 || len(b.ReferenceURLs) > 10 || b.MaxCreditsMicro <= 0 || b.MaxCreditsMicro > 1_000_000_000 || b.Duration < 5 || b.Duration > 120 {
		return ErrInvalid
	}
	if b.Mode == "replicate" && len(b.ReferenceURLs) == 0 {
		return ErrInvalid
	}
	for _, u := range b.ReferenceURLs {
		if !ValidURL(u) {
			return ErrInvalid
		}
	}
	if !slices.Contains([]string{"9:16", "16:9", "1:1"}, b.AspectRatio) || len(b.Platforms) == 0 {
		return ErrInvalid
	}
	for _, p := range b.Platforms {
		if p != "douyin" && p != "xiaohongshu" {
			return ErrInvalid
		}
	}
	m, e := FindModel(models, b.VideoModel, "video")
	if e != nil {
		return e
	}
	if !slices.Contains(m.Resolutions, b.Resolution) {
		return ErrInvalid
	}
	possible := make([]bool, b.Duration+1)
	possible[0] = true
	for i := 1; i <= b.Duration; i++ {
		for _, d := range m.Durations {
			if d <= i && possible[i-d] {
				possible[i] = true
			}
		}
	}
	if !possible[b.Duration] {
		return fmt.Errorf("duration_not_supported")
	}
	if b.ImageModel != "" {
		if _, e = FindModel(models, b.ImageModel, "image"); e != nil {
			return e
		}
	}
	for _, id := range b.AssetIDs {
		found := false
		for _, a := range assets {
			if a.ID == id && a.Kind == "image" {
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
	if len(plan.Shots) == 0 || len(plan.Shots) > 30 || len(plan.References) == 0 || len(plan.References) > 20 {
		return nil, 0, ErrInvalid
	}
	refs := map[string]bool{}
	for _, r := range plan.References {
		if !ValidURL(r.URL) || r.Title == "" || r.Evidence == "" || r.SelectionReason == "" || r.CapturedAt.IsZero() {
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
	out := []Shot{}
	var total int64
	duration := 0
	for _, p := range plan.Shots {
		if utf8.RuneCountInString(p.Prompt) > 4000 || utf8.RuneCountInString(p.ImagePrompt) > 4000 || utf8.RuneCountInString(p.Subtitle) > 300 || utf8.RuneCountInString(p.Camera) > 500 || utf8.RuneCountInString(p.Framing) > 500 || utf8.RuneCountInString(p.Action) > 1000 || utf8.RuneCountInString(p.Purpose) > 500 {
			return nil, 0, ErrInvalid
		}
		if !refs[p.SourceURL] || p.SourceStart < 0 || p.SourceEnd <= p.SourceStart || !slices.Contains(v.Durations, p.Duration) || !slices.Contains(b.AssetIDs, p.AssetID) || p.Prompt == "" || len(p.Prompt) > 12000 || len(p.Subtitle) > 900 || p.Framing == "" || p.Camera == "" || p.Action == "" || p.Purpose == "" {
			return nil, 0, ErrInvalid
		}
		q := v.PriceMicro * int64(p.Duration)
		if p.ImagePrompt != "" {
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
	if duration != b.Duration || total > b.MaxCreditsMicro {
		return nil, 0, fmt.Errorf("duration_or_budget_exceeded")
	}
	return out, total, nil
}
