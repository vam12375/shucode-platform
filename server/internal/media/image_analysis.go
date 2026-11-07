package media

import (
	"context"
	"reflect"
	"slices"
)

func validImageAnalysisBinding(p *Project, a Analysis) bool {
	if p.Analysis != nil && (a.TaskID != p.Analysis.TaskID || a.WorkspacePath != p.Analysis.WorkspacePath || a.WorkspaceIdentity != p.Analysis.WorkspaceIdentity || a.InputID == p.Analysis.InputID) {
		return false
	}
	if p.ImageAnalysis == nil {
		return a.Kind == "images"
	}
	return a.Kind == "planning" && p.Analysis != nil
}

func cloneImageAnalysis(value *AssetUnderstanding) *AssetUnderstanding {
	if value == nil {
		return nil
	}
	out := *value
	out.Keywords = slices.Clone(value.Keywords)
	out.Assets = slices.Clone(value.Assets)
	out.TemplateCandidates = slices.Clone(value.TemplateCandidates)
	return &out
}

// 观察结果独立落盘；没有规划阶段的显式输入时，不能把识图完成推进成付费视频任务。
func (s *Service) SetImageAnalysis(ctx context.Context, team, id string, version int64, input string, observation AssetUnderstanding) (*Project, error) {
	p, err := s.Repo.Get(ctx, team, id)
	if err != nil {
		return nil, err
	}
	if p == nil || !p.Brief.ImageAnalysisFirst || p.Analysis == nil || p.Analysis.Kind != "images" || p.Analysis.InputID != input || p.CancelRequested {
		return nil, ErrConflict
	}
	if p.Status == "image_review" && reflect.DeepEqual(p.ImageAnalysis, &observation) {
		return p, nil
	}
	if p.Status != "analyzing" || p.Version != version {
		return nil, ErrConflict
	}
	b := p.Brief
	b.AssetUnderstanding = &observation
	if err := validateAssetUnderstanding(b); err != nil {
		return nil, err
	}
	templates, err := s.videoTemplatesForModels(ctx, p.Models)
	if err != nil {
		return nil, err
	}
	for _, candidate := range observation.TemplateCandidates {
		b.TemplateID, b.TemplateVersion = candidate.TemplateID, candidate.TemplateVersion
		template, err := validateTemplateSelection(b, templates)
		if err != nil {
			return nil, err
		}
		if len(template.SkillIDs) > 0 {
			return nil, ErrInvalid
		}
	}
	return s.Mutate(ctx, team, id, version, func(v *Project) error {
		if v.CancelRequested || v.Status != "analyzing" || v.Analysis == nil || v.Analysis.InputID != input || v.Analysis.Kind != "images" {
			return ErrConflict
		}
		v.ImageAnalysis = cloneImageAnalysis(&observation)
		v.Status, v.Error = "image_review", ""
		return nil
	})
}
