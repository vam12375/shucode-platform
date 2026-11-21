package media

import (
	"context"
	"slices"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

// 制作单拥有模板身份；研究说明只投影合法展示字段，不让模型漏填或自评阻断完整镜头。
// 所有修改都在副本上完成，必须先规范化再比较重放、持久化和生成，避免内存存储别名污染。
func normalizeAutoPlanMetadata(b Brief, p Plan, snapshot *TemplateSnapshot, acceptedAutomatic *ResearchDecision) Plan {
	if b.Mode != "auto" || !adaptiveResearch(b) || b.ReferenceAssetID != "" || b.SourceOnly {
		return p
	}
	r := ResearchDecision{}
	if p.Research != nil {
		r = *p.Research
	}
	useAutomatic := automaticRecipeClaim(b, &p) && acceptedAutomatic != nil && acceptedAutomatic.Mode == "template" &&
		r.TemplateID == acceptedAutomatic.TemplateID && r.TemplateVersion == acceptedAutomatic.TemplateVersion
	r.Mode = "original"
	switch {
	case len(p.References) > 0:
		r.Mode = "reference"
	case b.TemplateID != "" && frozenTemplateMatches(b, snapshot), useAutomatic:
		r.Mode = "template"
	}
	if b.TemplateID != "" {
		r.TemplateID, r.TemplateVersion = b.TemplateID, b.TemplateVersion
	} else if !useAutomatic {
		r.TemplateID, r.TemplateVersion = "", ""
	}
	if !boundedNonempty(r.TemplateID, 100) || !boundedNonempty(r.TemplateVersion, 100) {
		r.TemplateID, r.TemplateVersion = "", ""
	}
	if !boundedNonempty(r.Reason, 1000) {
		r.Reason = "完整方案已生成，创作说明见原文。"
	}
	if !boundedNonempty(r.IntentSummary, 1000) {
		r.IntentSummary = "按当前制作单执行完整方案。"
	}
	observations, seen := []AssetObservation{}, map[string]bool{}
	for _, item := range r.AssetObservations {
		if len(observations) < MaxBriefImages && slices.Contains(videoProductAssetIDs(b), item.AssetID) && !seen[item.AssetID] && boundedNonempty(item.Description, 1000) {
			observations = append(observations, item)
			seen[item.AssetID] = true
		}
	}
	r.AssetObservations = observations
	gaps := []string{}
	for _, gap := range r.CreativeGaps {
		if len(gaps) < 5 && boundedNonempty(gap, 300) {
			gaps = append(gaps, gap)
		}
	}
	r.CreativeGaps = gaps
	p.Research = &r
	if p.References == nil {
		p.References = []Reference{}
	}
	return p
}

func automaticRecipeClaim(b Brief, p *Plan) bool {
	return b.Mode == "auto" && adaptiveResearch(b) && !b.SourceOnly && b.ReferenceAssetID == "" && b.TemplateID == "" &&
		p != nil && len(p.References) == 0 && p.Research != nil && p.Research.Mode == "template" &&
		boundedNonempty(p.Research.TemplateID, 100) && boundedNonempty(p.Research.TemplateVersion, 100)
}

func (p *Project) normalizeAcceptedPlan(plan Plan) Plan {
	var accepted *ResearchDecision
	if automaticRecipeClaim(p.Brief, p.Plan) {
		accepted = p.Plan.Research
	}
	return normalizeAutoPlanMetadata(p.Brief, plan, p.TemplateSnapshot, accepted)
}

// 普通自动规划可以根据公开目录选配方。仅在初次采纳新身份时查目录，重放、生成和未改选的修订沿用已受理结果。
func (s *Service) normalizeNewPlan(ctx context.Context, project *Project, plan Plan) (Plan, error) {
	b := project.Brief
	var err error
	plan, err = normalizeReplicaAdaptationPlan(b, plan)
	if err != nil {
		return Plan{}, err
	}
	normalized := project.normalizeAcceptedPlan(plan)
	if !automaticRecipeClaim(b, &plan) || normalized.Research.Mode == "template" {
		return normalized, nil
	}
	policy, err := s.Repo.Store.GetTeamPolicy(ctx, project.TeamID)
	if err != nil {
		return Plan{}, err
	}
	if !store.TeamModelAllowed(policy, b.VideoModel) {
		return normalized, nil
	}
	models, err := s.Repo.Models(ctx)
	if err != nil {
		return Plan{}, err
	}
	model, err := FindModel(models, b.VideoModel, "video")
	if err != nil {
		return normalized, nil
	}
	ordinary, _, err := s.VideoTemplateCatalog(ctx, []Model{model})
	if err != nil {
		return Plan{}, err
	}
	b.TemplateID, b.TemplateVersion = plan.Research.TemplateID, plan.Research.TemplateVersion
	if _, err := validateTemplateSelection(b, ordinary); err != nil {
		return normalized, nil
	}
	// 这里记录规划采用的身份，不把未向模型下发的技能正文补写成冻结快照。
	normalized.Research.Mode = "template"
	normalized.Research.TemplateID, normalized.Research.TemplateVersion = b.TemplateID, b.TemplateVersion
	return normalized, nil
}
