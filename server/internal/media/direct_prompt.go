package media

import "fmt"

// 看图直出提示词：客户端 shared/videoDirectPrompt.ts 为权威，规则见 multi-tenant-saas 契约
// 「2026-11-16 看图直出提示词」。direct 只在单段全能参考的图片优先制作单中成立，
// 执行参数全部来自冻结 Brief，模型只写观察与提示词正文。
const promptPolicyDirect = "direct"

func directPromptBrief(b Brief) bool { return b.PromptPolicy == promptPolicyDirect }

// validateDirectPromptBrief 与客户端 refineVideoPromptPolicyBrief 同口径；
// 需要模型额外产出结构（分段、后期脚本、首帧、模板快照、多方案）的设置都不能走直出。
func validateDirectPromptBrief(b Brief) error {
	switch b.PromptPolicy {
	case "", "staged":
		return nil
	case promptPolicyDirect:
	default:
		return ErrInvalid
	}
	if b.Mode != "auto" || !b.ImageAnalysisFirst || b.GenerationPolicy != "whole-first" || b.ReviewPlan || b.ReferenceMode != "multimodal" || b.Finishing == nil || b.Finishing.Voiceover || b.Finishing.Subtitles != "none" || b.TemplateID != "" || b.TemplateVersion != "" || b.ImageModel != "" || b.ReferenceAssetID != "" || len(b.ReferenceURLs) != 0 || b.SourceOnly || b.VariantCount > 1 {
		return fmt.Errorf("video_direct_prompt_ineligible")
	}
	return nil
}

// validateDirectPromptCapacity 在创建时确认模型一次容纳冻结时长与全部选图；
// 与客户端正文投影 compileVideoNarrativePlan 的单段条件一致，不能留到规划阶段才发现需要分段。
func validateDirectPromptCapacity(b Brief, m Model) error {
	if !directPromptBrief(b) {
		return nil
	}
	if len(planImageGenerationDurations(b, m)) != 1 || len(b.AssetIDs) > imageReferenceLimit(b, m) {
		return fmt.Errorf("video_direct_prompt_ineligible")
	}
	return nil
}

// imageFirstPlanningBound 判断图片优先制作单是否处于可接收视频方案的阶段：
// 分步流程须已保存识图并绑定 planning；直出没有识图审阅，由 direct 绑定代替该前置。
func imageFirstPlanningBound(p *Project) bool {
	if !p.Brief.ImageAnalysisFirst {
		return true
	}
	if p.Analysis == nil {
		return false
	}
	if directPromptBrief(p.Brief) {
		return p.Analysis.Kind == "direct" && p.ImageAnalysis == nil
	}
	return p.ImageAnalysis != nil && p.Analysis.Kind == "planning"
}
