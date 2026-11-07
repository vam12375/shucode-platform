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
		seen[t.ID] = true
		t.VerifiedModelIDs = []string{}
	}
	return out, nil
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
			if asset.Role == "product" {
				assetCount++
			}
		}
	}
	if assetCount < t.MinAssets || !slices.Contains(t.AspectRatios, b.AspectRatio) || b.Duration < t.DurationRange.Min || b.Duration > t.DurationRange.Max {
		return VideoTemplate{}, adaptiveResearchError()
	}
	if !slices.Contains(t.VerifiedModelIDs, b.VideoModel) && !b.ReviewPlan {
		return VideoTemplate{}, adaptiveResearchError()
	}
	return t, nil
}
func (s *Service) validateAdaptivePlan(ctx context.Context, b Brief, p Plan, models []Model, snapshots ...*TemplateSnapshot) error {
	if err := validateStoryPlan(b, p); err != nil {
		return err
	}
	if err := validateResearchPlan(b, p); err != nil {
		return err
	}
	if !adaptiveResearch(b) || p.Research.TemplateID == "" {
		return nil
	}
	selected := b
	selected.TemplateID = p.Research.TemplateID
	selected.TemplateVersion = p.Research.TemplateVersion
	if len(snapshots) > 0 && snapshots[0] != nil {
		snapshot := snapshots[0]
		if !frozenTemplateMatches(selected, snapshot) {
			return adaptiveResearchError()
		}
		if _, err := validateTemplateSelection(selected, []VideoTemplate{snapshot.Template}); err != nil {
			return err
		}
		if p.Research.Mode == "template" && !snapshotAllowsTemplateMode(b, snapshot) {
			return adaptiveResearchError()
		}
		return nil
	}
	// 执行使用项目冻结快照，不能按同ID的当前目录映射借用另一模型签名的批准。
	templates, err := s.videoTemplatesForModels(ctx, models)
	if err != nil {
		return err
	}
	t, err := validateTemplateSelection(selected, templates)
	if err != nil {
		return err
	}
	if len(t.SkillIDs) > 0 || p.Research.Mode == "template" && !slices.Contains(t.VerifiedModelIDs, b.VideoModel) {
		return adaptiveResearchError()
	}
	return nil
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
