package media

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"time"
	"unicode/utf8"
)

//go:embed video_template_catalog.json
var videoTemplateCatalogJSON []byte

const TemplateReviewsKey = "media.video-template-reviews"

type TemplateText struct {
	ZH string `json:"zh"`
	EN string `json:"en"`
}
type TemplateBeat struct {
	Purpose string  `json:"purpose"`
	Framing string  `json:"framing"`
	Action  string  `json:"action"`
	Camera  string  `json:"camera"`
	Weight  float64 `json:"weight"`
	// v2 配方的可选字段：全部 omitempty，旧配方序列化字节与快照 digest 不变，
	// 旧客户端按未知字段忽略。Seconds 是标称整片中的秒数；Tier 决定压缩时的取舍顺序；
	// Group 把相邻的一组镜头绑成一个动作环（如“提起→细看→放回”），精简时整组一同保留或一同删去。
	Seconds int    `json:"seconds,omitempty"`
	Tier    string `json:"tier,omitempty"`
	Act     string `json:"act,omitempty"`
	Group   string `json:"group,omitempty"`
	Sound   string `json:"sound,omitempty"`
}

// 压缩时 core 永不删除；先删 extra 再删 support，这两类插入镜不能改变主线状态。
// 同组镜头作为一个整体取舍：组内首镜的起态与末镜的完成态都等于组前后的静止状态，整组删去不会断链。
const (
	TemplateTierCore    = "core"
	TemplateTierSupport = "support"
	TemplateTierExtra   = "extra"
)

// TemplateStyle 是整片统一的拍摄风格；独立生成的每一段都要复述它，不能只写"沿用全片"。
type TemplateStyle struct {
	Look      string `json:"look"`
	Lens      string `json:"lens"`
	Motion    string `json:"motion"`
	Sound     string `json:"sound"`
	Narration string `json:"narration,omitempty"`
}
type TemplateDurationRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}
type TemplatePreview struct {
	CoverURL string `json:"coverUrl,omitempty"`
	VideoURL string `json:"videoUrl,omitempty"`
}
type VideoTemplate struct {
	Preview           *TemplatePreview      `json:"preview,omitempty"`
	SkillIDs          []string              `json:"skillIds,omitempty"`
	Provenance        *TemplateProvenance   `json:"provenance,omitempty"`
	ID                string                `json:"id"`
	Version           string                `json:"version"`
	Family            string                `json:"family"`
	Name              TemplateText          `json:"name"`
	Description       TemplateText          `json:"description"`
	Industries        []string              `json:"industries"`
	Objectives        []string              `json:"objectives"`
	Keywords          []string              `json:"keywords"`
	RequiredFacts     []string              `json:"requiredFacts"`
	AssetRequirements []string              `json:"assetRequirements"`
	AvoidWhen         []string              `json:"avoidWhen"`
	MinAssets         int                   `json:"minAssets"`
	AspectRatios      []string              `json:"aspectRatios"`
	DurationRange     TemplateDurationRange `json:"durationRange"`
	NominalSeconds    int                   `json:"nominalSeconds,omitempty"`
	Style             *TemplateStyle        `json:"style,omitempty"`
	Beats             []TemplateBeat        `json:"beats"`
	QualityChecks     []string              `json:"qualityChecks"`
	VerifiedModelIDs  []string              `json:"verifiedModelIds"`
}
type TemplateReviewInput struct {
	TemplateID      string `json:"templateId"`
	TemplateVersion string `json:"templateVersion"`
	ProjectTeamID   string `json:"projectTeamId"`
	ProjectID       string `json:"projectId"`
	Score           int    `json:"score"`
	Approved        bool   `json:"approved"`
	Note            string `json:"note"`
}
type TemplateReview struct {
	RecipeDigest string `json:"recipeDigest,omitempty"`
	TemplateReviewInput
	ModelID        string    `json:"modelId"`
	ModelSignature string    `json:"modelSignature"`
	OutputAssetID  string    `json:"outputAssetId"`
	ReviewerID     string    `json:"reviewerId"`
	ReviewedAt     time.Time `json:"reviewedAt"`
}

func VideoTemplateDefinitions() ([]VideoTemplate, error) {
	out := []VideoTemplate{}
	if err := json.Unmarshal(videoTemplateCatalogJSON, &out); err != nil {
		return nil, fmt.Errorf("video_templates_unavailable")
	}
	seen := map[string]bool{}
	for i := range out {
		t := &out[i]
		if !boundedNonempty(t.ID, 100) || !boundedNonempty(t.Version, 100) || seen[t.ID] || len(t.Beats) == 0 || t.MinAssets < 1 || t.DurationRange.Min < 5 || t.DurationRange.Max > MaxBriefSeconds {
			return nil, fmt.Errorf("video_templates_unavailable")
		}
		if len(t.SkillIDs) > 6 {
			return nil, fmt.Errorf("video_templates_unavailable")
		}
		skillSeen := map[string]bool{}
		for _, id := range t.SkillIDs {
			if !boundedNonempty(id, 100) || skillSeen[id] {
				return nil, fmt.Errorf("video_templates_unavailable")
			}
			skillSeen[id] = true
		}
		if !validTemplateTiming(*t) {
			return nil, fmt.Errorf("video_templates_unavailable")
		}
		seen[t.ID] = true
		t.VerifiedModelIDs = []string{}
	}
	return out, nil
}

// 没有任何 v2 字段的旧配方原样通过；一旦出现任一 v2 字段，整份配方必须自洽，
// 否则客户端按标称秒数压缩时会得到缺字段或总和不符的节拍表。
func validTemplateTiming(t VideoTemplate) bool {
	timed := t.NominalSeconds != 0 || t.Style != nil
	for _, beat := range t.Beats {
		if beat.Seconds != 0 || beat.Tier != "" || beat.Act != "" || beat.Group != "" || beat.Sound != "" {
			timed = true
		}
	}
	if !timed {
		return true
	}
	style := t.Style
	if t.NominalSeconds < t.DurationRange.Min || t.NominalSeconds > t.DurationRange.Max || len(t.Beats) > 30 || style == nil ||
		!boundedNonempty(style.Look, 600) || !boundedNonempty(style.Lens, 600) || !boundedNonempty(style.Motion, 600) || !boundedNonempty(style.Sound, 600) || utf8.RuneCountInString(style.Narration) > 600 {
		return false
	}
	total := 0
	seenGroups, groupSizes := map[string]bool{}, map[string]int{}
	previousGroup, previousTier := "", ""
	for _, beat := range t.Beats {
		switch beat.Tier {
		case TemplateTierCore, TemplateTierSupport, TemplateTierExtra:
		default:
			return false
		}
		if beat.Seconds < 2 || beat.Seconds > 30 || beat.Weight != float64(beat.Seconds) || !boundedNonempty(beat.Act, 30) || !boundedNonempty(beat.Sound, 400) {
			return false
		}
		if beat.Group != "" {
			// 整组一同取舍：组必须相邻、同层级且不含核心镜，否则精简会拆开一个完整的动作环。
			if !boundedNonempty(beat.Group, 30) || beat.Tier == TemplateTierCore ||
				beat.Group != previousGroup && seenGroups[beat.Group] || beat.Group == previousGroup && beat.Tier != previousTier {
				return false
			}
			seenGroups[beat.Group] = true
			groupSizes[beat.Group]++
		}
		previousGroup, previousTier = beat.Group, beat.Tier
		total += beat.Seconds
	}
	for _, size := range groupSizes {
		if size < 2 {
			return false
		}
	}
	return total == t.NominalSeconds
}
func templateModelSignature(m Model) string {
	raw, _ := json.Marshal(m)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (r Repository) TemplateReviews(ctx context.Context) ([]TemplateReview, error) {
	raw, err := r.Store.GetConfigValue(ctx, TemplateReviewsKey)
	if err != nil {
		return nil, err
	}
	out := []TemplateReview{}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("video_templates_unavailable")
		}
	}
	return out, nil
}
func (s *Service) VideoTemplates(ctx context.Context) ([]VideoTemplate, error) {
	models, err := s.Repo.Models(ctx)
	if err != nil {
		return nil, err
	}
	return s.videoTemplatesForModels(ctx, models)
}

func (s *Service) videoTemplatesForModels(ctx context.Context, models []Model) ([]VideoTemplate, error) {
	catalog, err := s.resolveTemplateCatalog(ctx, models)
	if err != nil {
		return nil, err
	}
	out := []VideoTemplate{}
	for _, entry := range catalog {
		if entry.enabled {
			out = append(out, entry.snapshot.Template)
		}
	}
	return out, nil
}

// 管理目录保留停用模板；消费与准入统一读取过滤后的目录。
func (s *Service) AllVideoTemplates(ctx context.Context, models []Model) ([]VideoTemplate, error) {
	catalog, err := s.resolveTemplateCatalog(ctx, models)
	if err != nil {
		return nil, err
	}
	out := make([]VideoTemplate, 0, len(catalog))
	for _, entry := range catalog {
		out = append(out, entry.snapshot.Template)
	}
	return out, nil
}
func findVideoTemplate(templates []VideoTemplate, id, version string) (VideoTemplate, error) {
	for _, t := range templates {
		if t.ID == id && t.Version == version {
			return t, nil
		}
	}
	return VideoTemplate{}, adaptiveResearchError()
}
func (s *Service) validateTemplateBrief(ctx context.Context, b Brief, models []Model) error {
	if !adaptiveResearch(b) || b.TemplateID == "" {
		return nil
	}
	templates, err := s.videoTemplatesForModels(ctx, models)
	if err != nil {
		return err
	}
	_, err = validateTemplateSelection(b, templates)
	return err
}

func validateTemplateSelection(b Brief, templates []VideoTemplate) (VideoTemplate, error) {
	t, err := findVideoTemplate(templates, b.TemplateID, b.TemplateVersion)
	if err != nil {
		return VideoTemplate{}, err
	}
	assetCount := len(videoProductAssetIDs(b))
	if b.AssetUnderstanding != nil {
		assetCount = 0
		for _, asset := range b.AssetUnderstanding.Assets {
			// 片尾绑定优先于识图角色；不参与生成的图片不能补足模板主体数，与客户端一致。
			if b.Finishing != nil && b.Finishing.Brand != nil && asset.AssetID == b.Finishing.Brand.LogoAssetID {
				continue
			}
			// 品牌/IP 可用角色造型图锁身份；Logo 和场景图不能补足商品证据。
			if asset.Role == "product" || (t.Family == "brand" && asset.Role == "character") {
				assetCount++
			}
		}
	}
	if assetCount < t.MinAssets || !slices.Contains(t.AspectRatios, b.AspectRatio) || b.Duration < t.DurationRange.Min || b.Duration > t.DurationRange.Max {
		return VideoTemplate{}, adaptiveResearchError()
	}
	// 图片优先流程已明确点击直接成片；未验证配方只作原创结构，不增加人工审阅门槛。
	imageFirstRecipe := b.Mode == "auto" && b.ImageAnalysisFirst && b.GenerationPolicy == "whole-first"
	if !slices.Contains(t.VerifiedModelIDs, b.VideoModel) && !b.ReviewPlan && !imageFirstRecipe {
		return VideoTemplate{}, adaptiveResearchError()
	}
	return t, nil
}
func (s *Service) validateAdaptivePlan(_ context.Context, b Brief, p Plan, _ []Model, _ ...*TemplateSnapshot) error {
	if err := validateStoryPlan(b, p); err != nil {
		return err
	}
	if err := validateSelectedImageUsage(b, p); err != nil {
		return err
	}
	// 模板已在制作单创建/选择时冻结；目录、验证与研究说明不重复否决已完成镜头。
	return validateResearchPlan(b, p)
}
func (s *Service) ReviewVideoTemplate(ctx context.Context, reviewer string, input TemplateReviewInput) (TemplateReview, error) {
	if reviewer == "" || input.Score < 1 || input.Score > 100 || input.Approved && input.Score < 85 || !boundedNonempty(input.Note, 2000) {
		return TemplateReview{}, ErrInvalid
	}
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		return TemplateReview{}, err
	}
	definition, err := findVideoTemplate(templates, input.TemplateID, input.TemplateVersion)
	if err != nil {
		return TemplateReview{}, err
	}
	p, err := s.Repo.Get(ctx, input.ProjectTeamID, input.ProjectID)
	if err != nil {
		return TemplateReview{}, err
	}
	if p == nil || p.Status != "completed" || p.CancelRequested || p.OutputAssetID == "" || p.Canvas != nil || p.Agent != nil || p.Plan == nil || p.Plan.Research == nil || p.Plan.Research.TemplateID != input.TemplateID || p.Plan.Research.TemplateVersion != input.TemplateVersion {
		return TemplateReview{}, adaptiveResearchError()
	}
	found := false
	assets, err := s.Repo.Assets(ctx, p.TeamID)
	if err != nil {
		return TemplateReview{}, err
	}
	for _, asset := range assets {
		if asset.ID == p.OutputAssetID && asset.Kind == "video" && asset.Size > 0 {
			found = true
		}
	}
	info, fileErr := os.Stat(s.Blobs.Path(p.TeamID, p.OutputAssetID))
	if !found || fileErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return TemplateReview{}, adaptiveResearchError()
	}
	model, err := FindModel(p.Models, p.Brief.VideoModel, "video")
	if err != nil {
		return TemplateReview{}, err
	}
	if len(definition.SkillIDs) > 0 && !frozenTemplateMatches(p.Brief, p.TemplateSnapshot) {
		return TemplateReview{}, adaptiveResearchError()
	}
	review := TemplateReview{TemplateReviewInput: input, ModelID: model.ID, ModelSignature: templateModelSignature(model), OutputAssetID: p.OutputAssetID, ReviewerID: reviewer, ReviewedAt: time.Now().UTC()}
	if p.TemplateSnapshot != nil {
		review.RecipeDigest = p.TemplateSnapshot.Digest
	}
	err = updateJSON(ctx, s.Repo.Store, TemplateReviewsKey, []TemplateReview{}, func(rows *[]TemplateReview) error {
		if len(*rows) >= 10000 {
			return fmt.Errorf("template_review_limit")
		}
		*rows = append(*rows, review)
		return nil
	})
	return review, err
}
