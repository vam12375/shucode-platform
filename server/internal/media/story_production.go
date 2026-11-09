package media

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

type StoryAssetRole struct {
	AssetID string `json:"assetId"`
	Role    string `json:"role"`
	Facts   string `json:"facts"`
	Retain  string `json:"retain"`
	Allow   string `json:"allow"`
}
type StoryGap struct {
	Description string `json:"description"`
	Suggestion  string `json:"suggestion"`
	Resolution  string `json:"resolution"`
}
type StoryPlanReview struct {
	Status string   `json:"status"`
	Issues []string `json:"issues"`
}
type StoryPlan struct {
	Goal        string           `json:"goal"`
	Progression string           `json:"progression"`
	Ending      string           `json:"ending"`
	AssetRoles  []StoryAssetRole `json:"assetRoles"`
	Gaps        []StoryGap       `json:"gaps"`
	Review      *StoryPlanReview `json:"review,omitempty"`
}
type ShotCreative struct {
	Source     string   `json:"source"`
	AssetIDs   []string `json:"assetIds"`
	Retain     string   `json:"retain"`
	Expand     string   `json:"expand"`
	Beat       string   `json:"beat"`
	Connection string   `json:"connection"`
	InFrame    int      `json:"inFrame"`
	OutFrame   int      `json:"outFrame"`
}

func (v *ShotCreative) UnmarshalJSON(raw []byte) error {
	raw, err := normalizeSecondsRangeJSON(raw)
	if err != nil {
		return err
	}
	type plain ShotCreative
	var x plain
	if _, err := decodeCompositionFields(raw, &x, []string{"source", "assetIds", "retain", "expand", "beat", "connection", "inFrame", "outFrame"}, nil); err != nil {
		return err
	}
	*v = ShotCreative(x)
	return nil
}
func (v *StoryPlan) UnmarshalJSON(raw []byte) error {
	type plain StoryPlan
	var x plain
	if _, err := decodeCompositionFields(raw, &x, []string{"goal", "progression", "ending", "assetRoles", "gaps"}, []string{"review"}); err != nil {
		return err
	}
	*v = StoryPlan(x)
	return nil
}
func (v *StoryAssetRole) UnmarshalJSON(raw []byte) error {
	type plain StoryAssetRole
	var x plain
	if _, err := decodeCompositionFields(raw, &x, []string{"assetId", "role", "facts", "retain", "allow"}, nil); err != nil {
		return err
	}
	*v = StoryAssetRole(x)
	return nil
}
func (v *StoryGap) UnmarshalJSON(raw []byte) error {
	type plain StoryGap
	var x plain
	if _, err := decodeCompositionFields(raw, &x, []string{"description", "suggestion", "resolution"}, nil); err != nil {
		return err
	}
	*v = StoryGap(x)
	return nil
}
func (v *StoryPlanReview) UnmarshalJSON(raw []byte) error {
	type plain StoryPlanReview
	var x plain
	if _, err := decodeCompositionFields(raw, &x, []string{"status", "issues"}, nil); err != nil {
		return err
	}
	*v = StoryPlanReview(x)
	return nil
}

func storyPlanning(b Brief) bool { return b.CreativeExpansion != "" }
func storyError() error          { return fmt.Errorf("video_story_invalid") }
func validateStoryBrief(b Brief) error {
	if b.GenerationPolicy != "" && (b.GenerationPolicy != "whole-first" || !storyPlanning(b) || b.Mode != "auto" || b.Finishing == nil || !adaptiveResearch(b) || b.ReferenceAssetID != "" || b.SourceOnly) {
		return fmt.Errorf("video_generation_policy_invalid")
	}
	if !storyPlanning(b) {
		return nil
	}
	if !slices.Contains([]string{"related", "strict"}, b.CreativeExpansion) || b.Mode != "auto" || b.ReferenceAssetID != "" || b.SourceOnly || b.Finishing == nil || !adaptiveResearch(b) {
		return storyError()
	}
	return nil
}
func storyText(value string, max int, required bool) bool {
	return utf8.RuneCountInString(value) <= max && (!required || strings.TrimSpace(value) != "")
}
func validateStoryPlan(b Brief, p Plan) error {
	if !storyPlanning(b) {
		if p.Story != nil {
			return storyError()
		}
		for _, shot := range p.Shots {
			if shot.Creative != nil {
				return storyError()
			}
		}
		return nil
	}
	if err := validateStoryBrief(b); err != nil {
		return err
	}
	st := p.Story
	if st == nil || !storyText(st.Goal, 1000, true) || !storyText(st.Progression, 2000, true) || !storyText(st.Ending, 1000, true) || len(st.AssetRoles) != len(b.AssetIDs) || st.Gaps == nil || len(st.Gaps) > 5 {
		return storyError()
	}
	seen := map[string]bool{}
	for _, role := range st.AssetRoles {
		if !slices.Contains(b.AssetIDs, role.AssetID) || seen[role.AssetID] || !slices.Contains([]string{"product", "scene", "style", "unused"}, role.Role) || !storyText(role.Facts, 1000, true) || !storyText(role.Retain, 1000, false) || !storyText(role.Allow, 1000, false) {
			return storyError()
		}
		seen[role.AssetID] = true
	}
	for _, gap := range st.Gaps {
		if !storyText(gap.Description, 500, true) || !storyText(gap.Suggestion, 500, true) || !slices.Contains([]string{"generate", "upload", "omit"}, gap.Resolution) {
			return storyError()
		}
	}
	if st.Review != nil {
		if !slices.Contains([]string{"passed", "warning"}, st.Review.Status) || st.Review.Issues == nil || len(st.Review.Issues) > 20 {
			return storyError()
		}
		for _, issue := range st.Review.Issues {
			if !storyText(issue, 500, true) {
				return storyError()
			}
		}
	}
	frames := 0
	for _, shot := range p.Shots {
		c := shot.Creative
		if c == nil || !slices.Contains([]string{"asset", "extended", "generated"}, c.Source) || c.AssetIDs == nil || len(c.AssetIDs) > 20 || !storyText(c.Retain, 1000, false) || !storyText(c.Expand, 1000, false) || !storyText(c.Beat, 500, true) || !storyText(c.Connection, 500, true) || c.InFrame < 0 || c.OutFrame > shot.Duration*30 || c.OutFrame-c.InFrame < 15 {
			return storyError()
		}
		if c.Source == "generated" {
			if len(c.AssetIDs) > 0 || shot.AssetID != "" {
				return storyError()
			}
		} else {
			if len(c.AssetIDs) == 0 {
				return storyError()
			}
			refs := map[string]bool{}
			for _, id := range c.AssetIDs {
				if !slices.Contains(videoProductAssetIDs(b), id) || refs[id] {
					return storyError()
				}
				refs[id] = true
			}
			if shot.AssetID != "" && !refs[shot.AssetID] {
				return storyError()
			}
		}
		if b.CreativeExpansion == "strict" && (c.Source != "asset" || strings.TrimSpace(c.Expand) != "") {
			return storyError()
		}
		frames += c.OutFrame - c.InFrame
	}
	if frames != b.Duration*30 {
		return storyError()
	}
	return nil
}
func storyNeedsReview(b Brief, p Plan) bool {
	if !storyPlanning(b) {
		return false
	}
	// 关闭生成前独立复核，缺少复核结果不能阻塞生成；素材和模型仍经原准入校验。
	if b.ImageAnalysisFirst && !b.ReviewPlan {
		return false
	}
	if p.Story == nil {
		return false // validateStoryPlan 负责拒绝缺失的故事。
	}
	for _, g := range p.Story.Gaps {
		if g.Resolution == "upload" {
			return true
		}
	}
	return false
}

// 根因：全局图片污染每镜参考，导致模型只能重复摆拍；新计划只使用冻结的镜头子集。
func storyShotBrief(b Brief, p ShotPlan) Brief {
	if storyPlanning(b) && p.Creative != nil {
		b.AssetIDs = slices.Clone(p.Creative.AssetIDs)
	}
	return b
}
func storyDeliveryFrames(p ShotPlan) int {
	if p.Creative != nil {
		return p.Creative.OutFrame - p.Creative.InFrame
	}
	return p.Duration * 30
}

func storyShotDeliveryFrames(shot Shot) int { return storyDeliveryFrames(shot.Plan) }
