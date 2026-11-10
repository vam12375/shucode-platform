package media

import "slices"

// 制作单拥有模板身份；研究说明只投影合法展示字段，不让模型漏填或自评阻断完整镜头。
// 所有修改都在副本上完成，必须先规范化再比较重放、持久化和生成，避免内存存储别名污染。
func normalizeAutoPlanMetadata(b Brief, p Plan, snapshot *TemplateSnapshot) Plan {
	if b.Mode != "auto" || !adaptiveResearch(b) || b.ReferenceAssetID != "" || b.SourceOnly {
		return p
	}
	r := ResearchDecision{}
	if p.Research != nil {
		r = *p.Research
	}
	r.Mode = "original"
	if len(p.References) > 0 {
		r.Mode = "reference"
	}
	if b.TemplateID != "" {
		r.TemplateID, r.TemplateVersion = b.TemplateID, b.TemplateVersion
	}
	if !boundedNonempty(r.TemplateID, 100) || !boundedNonempty(r.TemplateVersion, 100) {
		r.TemplateID, r.TemplateVersion = "", ""
	}
	if r.Mode == "original" && snapshot != nil && frozenTemplateMatches(b, snapshot) && snapshotAllowsTemplateMode(b, snapshot) {
		r.Mode = "template"
	}
	if !boundedNonempty(r.Reason, 1000) {
		r.Reason = "完整方案已生成，创作说明见原文。"
	}
	if !boundedNonempty(r.IntentSummary, 1000) {
		r.IntentSummary = "按当前制作单执行完整方案。"
	}
	observations, seen := []AssetObservation{}, map[string]bool{}
	for _, item := range r.AssetObservations {
		if len(observations) < 20 && slices.Contains(videoProductAssetIDs(b), item.AssetID) && !seen[item.AssetID] && boundedNonempty(item.Description, 1000) {
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
