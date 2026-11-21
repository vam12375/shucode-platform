package media

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

type ReplicaAdaptation struct {
	Status   string           `json:"status"`
	Blockers []ReplicaBlocker `json:"blockers"`
}

type ReplicaBlocker struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	ShotIndices []int  `json:"shotIndices,omitempty"`
}

type ReplicaReferencePurpose string

// 只约束新增可选值，避免更换整份 Brief 解码器破坏旧存储快照；API 的未知字段仍由原严格入站解码负责。
func (p *ReplicaReferencePurpose) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ErrInvalid
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	*p = ReplicaReferencePurpose(value)
	return nil
}

func (a *ReplicaAdaptation) UnmarshalJSON(raw []byte) error {
	type plain ReplicaAdaptation
	var value plain
	if _, err := decodeCompositionFields(raw, &value, []string{"status", "blockers"}, nil); err != nil {
		return err
	}
	*a = ReplicaAdaptation(value)
	return nil
}

func (b *ReplicaBlocker) UnmarshalJSON(raw []byte) error {
	type plain ReplicaBlocker
	var value plain
	if _, err := decodeCompositionFields(raw, &value, []string{"code", "message"}, []string{"shotIndices"}); err != nil {
		return err
	}
	*b = ReplicaBlocker(value)
	b.Message = strings.TrimSpace(b.Message)
	return nil
}

func replicaAdaptationBrief(b Brief) bool {
	return b.Mode == "replicate" && b.ReferenceAssetID != "" && !b.SourceOnly
}

func validateReplicaPurpose(b Brief) error {
	if b.ReferenceVideoPurpose != "" && (!replicaAdaptationBrief(b) || !slices.Contains([]string{"motion", "preserve"}, string(b.ReferenceVideoPurpose))) {
		return fmt.Errorf("video_replica_purpose_invalid")
	}
	// 新输入的分析附件与供应商参考必须一一对应；供应商去重会改变重复素材的编号，历史任务保持原冻结输入。
	if b.ReferenceVideoPurpose != "" && len(uniqueReferenceImageIDs(b.AssetIDs)) != len(b.AssetIDs) {
		return fmt.Errorf("video_replica_reference_mapping_invalid")
	}
	return nil
}

func validateReplicaAdaptation(b Brief, p Plan) error {
	if err := validateReplicaPurpose(b); err != nil {
		return err
	}
	a := p.Adaptation
	if a == nil {
		if b.ReferenceVideoPurpose != "" {
			return fmt.Errorf("video_replica_adaptation_required")
		}
		return nil
	}
	invalid := func() error { return fmt.Errorf("video_replica_adaptation_invalid") }
	if !replicaAdaptationBrief(b) || !slices.Contains([]string{"ready", "blocked"}, a.Status) || a.Blockers == nil || len(a.Blockers) > 20 || (a.Status == "ready" && len(a.Blockers) != 0) || (a.Status == "blocked" && len(a.Blockers) == 0) {
		return invalid()
	}
	for _, blocker := range a.Blockers {
		if !slices.Contains([]string{"missing_target_view", "missing_target_structure", "missing_target_copy", "unsupported_edit", "unresolved_mapping", "analysis_requires_review"}, blocker.Code) || strings.TrimSpace(blocker.Message) == "" || len(utf16.Encode([]rune(strings.TrimSpace(blocker.Message)))) > 500 || len(blocker.ShotIndices) > 30 {
			return invalid()
		}
		seen := map[int]bool{}
		for _, index := range blocker.ShotIndices {
			if index < 0 || index >= len(p.Shots) || seen[index] {
				return invalid()
			}
			seen[index] = true
		}
	}
	return nil
}

// 旧改编把阻塞写进 story，后续清洗曾让占位脚本变成可执行计划；先保留问题事实，再清理不适用的原创字段。
func normalizeReplicaAdaptationPlan(b Brief, p Plan) (Plan, error) {
	if !replicaAdaptationBrief(b) {
		return p, validateReplicaAdaptation(b, p)
	}
	issues := []string{}
	if p.Story != nil {
		if p.Story.Review != nil && p.Story.Review.Status == "warning" {
			issues = append(issues, p.Story.Review.Issues...)
			if len(issues) == 0 {
				issues = append(issues, "原改编方案存在需要确认的问题。")
			}
		}
		for _, gap := range p.Story.Gaps {
			if gap.Resolution == "upload" {
				issues = append(issues, gap.Description)
			}
		}
	}
	if len(issues) > 0 {
		if p.Adaptation != nil && p.Adaptation.Status == "ready" {
			return Plan{}, fmt.Errorf("video_replica_adaptation_conflict")
		}
		if p.Adaptation == nil {
			unique := []string{}
			for _, issue := range issues {
				if !slices.Contains(unique, issue) {
					unique = append(unique, issue)
				}
			}
			issues = unique
			blockers := []ReplicaBlocker{}
			for index, issue := range issues {
				if index == 19 && len(issues) > 20 {
					blockers = append(blockers, ReplicaBlocker{Code: "analysis_requires_review", Message: fmt.Sprintf("另有 %d 项原分析待解决问题，请查看分析原文并补充素材或调整要求后重新提取方案。", len(issues)-19)})
					break
				}
				message := strings.TrimSpace(issue)
				if message == "" || len(utf16.Encode([]rune(message))) > 500 {
					message = "原改编方案存在待确认问题，请补充素材并重新分析。"
				}
				blockers = append(blockers, ReplicaBlocker{Code: "analysis_requires_review", Message: message})
			}
			p.Adaptation = &ReplicaAdaptation{Status: "blocked", Blockers: blockers}
		}
	}
	p.Story, p.Research = nil, nil
	p.Shots = append([]ShotPlan(nil), p.Shots...)
	for i := range p.Shots {
		p.Shots[i].Creative = nil
	}
	return p, validateReplicaAdaptation(b, p)
}

func validateReplicaGenerationReady(b Brief, p Plan) error {
	if err := validateReplicaAdaptation(b, p); err != nil {
		return err
	}
	if p.Adaptation != nil && p.Adaptation.Status == "blocked" {
		return fmt.Errorf("video_replica_adaptation_blocked")
	}
	return nil
}

func validateReplicaReadinessRevision(p *Project, next Plan) error {
	if replicaAdaptationBrief(p.Brief) && p.Plan != nil && !reflect.DeepEqual(p.Plan.Adaptation, next.Adaptation) {
		return fmt.Errorf("video_replica_adaptation_revision_required")
	}
	return nil
}

func canReconfigureBlockedReplica(p *Project) bool {
	if !replicaAdaptationBrief(p.Brief) || p.Status != "review" || p.Plan == nil || p.Plan.Adaptation == nil || p.Plan.Adaptation.Status != "blocked" || validateReplicaAdaptation(p.Brief, *p.Plan) != nil || p.CancelRequested || p.ChargedMicro != 0 || p.OutputAssetID != "" || p.LeaseUntil.After(time.Now()) {
		return false
	}
	for _, shot := range p.Shots {
		if shot.Status != "pending" || referenceExecutionHasEvidence(shot) {
			return false
		}
	}
	return true
}

func replicaInputGuidance(b Brief, imageCount int, hasVideo bool) string {
	if b.ReferenceVideoPurpose == "" {
		return ""
	}
	mentions := make([]string, imageCount)
	for i := range mentions {
		mentions[i] = "@图片" + strconv.Itoa(i+1)
	}
	var out strings.Builder
	out.WriteString("输入角色：" + strings.Join(mentions, "、") + "按实际输入顺序提供目标主体、商品与包装的真实外观依据；图片未展示的内部结构、参数及品牌文案不得当作事实。\n")
	if hasVideo {
		if b.ReferenceVideoPurpose == "motion" {
			out.WriteString("@视频1仅参考相机运动、构图变化、节奏及转场；不继承原商品、子零件、材质配色、Logo、文字与品牌尾卡。目标主体、场景及文字依目标图片和用户要求，动作按真实外观改编，不强制复制不适用的内部拆装动作。\n")
		} else {
			out.WriteString("@视频1保留原片场景、运动与节奏，仅替换用户明确对象；被替换商品及其可见子部件必须一致替换，原品牌或文字与目标矛盾时不得残留或嫁接。\n")
		}
	} else if b.ReferenceVideoPurpose == "motion" {
		out.WriteString("源事实仅参考相机运动、构图变化、节奏及转场；不继承原商品、子零件、配色、Logo、文字与尾卡，目标动作依真实外观改编。\n")
	} else {
		out.WriteString("依据已审阅的保留与替换范围生成；被替换商品及其可见子部件必须一致，不保留与目标矛盾的原品牌文字。\n")
	}
	return out.String()
}

func replicaSegmentImageCount(b Brief, facts []ShotPlan) int {
	return len(replicaExecutionImageIDs(b, facts))
}
