package media

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

type ObservedAsset struct {
	AssetID      string `json:"assetId"`
	Role         string `json:"role"`
	SubjectGroup string `json:"subjectGroup"`
	Description  string `json:"description"`
}
type ObservedTemplate struct {
	TemplateID      string `json:"templateId"`
	TemplateVersion string `json:"templateVersion"`
	Reason          string `json:"reason"`
}
type AssetUnderstanding struct {
	Product            string             `json:"product"`
	Category           string             `json:"category"`
	Summary            string             `json:"summary"`
	Keywords           []string           `json:"keywords"`
	Ambiguity          string             `json:"ambiguity"`
	Assets             []ObservedAsset    `json:"assets"`
	TemplateCandidates []ObservedTemplate `json:"templateCandidates"`
}

// 识图只是冻结的观察数据；平台仍校验素材归属，不能把漏看或多商品混用当成有效输入。
func validateAssetUnderstanding(b Brief) error {
	o := b.AssetUnderstanding
	if o == nil {
		return nil
	}
	if b.Mode != "auto" || b.GenerationPolicy != "whole-first" || b.ReviewPlan {
		return fmt.Errorf("video_understanding_flow_invalid")
	}
	if !boundedNonempty(o.Product, 200) || !boundedNonempty(o.Category, 100) || !boundedNonempty(o.Summary, 2000) || o.Keywords == nil || len(o.Keywords) > 20 || o.TemplateCandidates == nil || len(o.TemplateCandidates) > 5 || utf8.RuneCountInString(o.Ambiguity) > 1000 {
		return fmt.Errorf("video_understanding_invalid_output")
	}
	for _, keyword := range o.Keywords {
		if !boundedNonempty(keyword, 100) {
			return fmt.Errorf("video_understanding_invalid_output")
		}
	}
	for _, candidate := range o.TemplateCandidates {
		if !boundedNonempty(candidate.TemplateID, 100) || !boundedNonempty(candidate.TemplateVersion, 100) || !boundedNonempty(candidate.Reason, 500) {
			return fmt.Errorf("video_understanding_invalid_output")
		}
	}
	if len(o.Assets) != len(b.AssetIDs) || len(o.Assets) < 1 || len(o.Assets) > MaxBriefImages {
		return fmt.Errorf("video_understanding_assets_invalid")
	}
	seen := map[string]bool{}
	groups := map[string]bool{}
	characters := 0
	for _, asset := range o.Assets {
		if seen[asset.AssetID] || !slices.Contains(b.AssetIDs, asset.AssetID) || !boundedNonempty(asset.AssetID, 200) {
			return fmt.Errorf("video_understanding_assets_invalid")
		}
		seen[asset.AssetID] = true
		if !slices.Contains([]string{"product", "character", "scene", "style", "logo", "unused"}, asset.Role) || !boundedNonempty(asset.Description, 1000) || utf8.RuneCountInString(asset.SubjectGroup) > 100 {
			return fmt.Errorf("video_understanding_invalid_output")
		}
		if asset.Role == "product" {
			groups[strings.TrimSpace(asset.SubjectGroup)] = true
		}
		// 角色图锁定各自身份，不应因有多个剧情角色被当作商品歧义拦截。
		if asset.Role == "character" {
			characters++
			if strings.TrimSpace(asset.SubjectGroup) == "" {
				return fmt.Errorf("video_understanding_subject_ambiguous")
			}
		}
	}
	if len(groups) == 0 && characters == 0 {
		return fmt.Errorf("video_understanding_subject_required")
	}
	if len(groups) > 1 || groups[""] || strings.TrimSpace(o.Ambiguity) != "" {
		return fmt.Errorf("video_understanding_subject_ambiguous")
	}
	return nil
}
