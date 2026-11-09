package media

import (
	"context"
	"strings"
)

type TemplateSelection struct {
	TemplateID      string `json:"templateId"`
	TemplateVersion string `json:"templateVersion"`
}

func (selection *TemplateSelection) normalized() *TemplateSelection {
	if selection == nil {
		return nil
	}
	return &TemplateSelection{TemplateID: strings.TrimSpace(selection.TemplateID), TemplateVersion: strings.TrimSpace(selection.TemplateVersion)}
}

func (selection *TemplateSelection) valid() bool {
	return selection == nil || boundedNonempty(selection.TemplateID, 100) && boundedNonempty(selection.TemplateVersion, 100)
}

func (selection *TemplateSelection) matches(p *Project) bool {
	return selection == nil || selection.TemplateID == p.Brief.TemplateID && selection.TemplateVersion == p.Brief.TemplateVersion && frozenTemplateMatches(p.Brief, p.TemplateSnapshot)
}

func (s *Service) prepareImageTemplateSelection(ctx context.Context, p *Project, a Analysis, selection *TemplateSelection) (Brief, *TemplateSnapshot, error) {
	if selection == nil {
		return p.Brief, p.TemplateSnapshot, nil
	}
	if p.Brief.Mode != "auto" || !p.Brief.ImageAnalysisFirst || p.Brief.GenerationPolicy != "whole-first" || p.Brief.ReviewPlan || a.Kind != "planning" || p.ImageAnalysis == nil || p.Plan != nil || p.PlanningResult != nil || len(p.Shots) != 0 || p.OutputAssetID != "" {
		return Brief{}, nil, ErrConflict
	}
	if p.TemplateSnapshot != nil {
		// 规划重试只能沿用已接受的快照；目录更新、停用均不能替换本单技能正文。
		if !selection.matches(p) {
			return Brief{}, nil, ErrConflict
		}
		return p.Brief, p.TemplateSnapshot, nil
	}
	if p.Status != "image_review" || p.Analysis == nil || p.Analysis.Kind != "images" || p.Brief.TemplateID != "" && (p.Brief.TemplateID != selection.TemplateID || p.Brief.TemplateVersion != selection.TemplateVersion) {
		return Brief{}, nil, ErrConflict
	}
	brief := p.Brief
	brief.TemplateID, brief.TemplateVersion = selection.TemplateID, selection.TemplateVersion
	snapshot, err := s.freezeTemplate(ctx, brief, p.Models)
	if err != nil {
		return Brief{}, nil, err
	}
	if snapshot == nil {
		return Brief{}, nil, ErrInvalid
	}
	return brief, snapshot, nil
}
