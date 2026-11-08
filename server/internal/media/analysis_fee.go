package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func equalAnalysisPlan(left, right Plan) bool {
	a, err := json.Marshal(withoutReferenceClipBindings(left))
	if err != nil {
		return false
	}
	b, err := json.Marshal(withoutReferenceClipBindings(right))
	return err == nil && bytes.Equal(a, b)
}

func (s *Service) bindAnalysis(ctx context.Context, team, id string, version int64, a Analysis) (*Project, error) {
	p, err := s.Repo.Get(ctx, team, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, ErrInvalid
	}
	if (p.Brief.ReferenceAssetID != "" || p.Brief.ImageAnalysisFirst) && p.Status == "analyzing" && p.Analysis != nil && reflect.DeepEqual(*p.Analysis, a) {
		return p, nil
	}
	if p.Version != version || (p.Status != "draft" && p.Status != "analysis_failed" && !(p.Brief.ImageAnalysisFirst && p.Status == "image_review")) || p.CancelRequested {
		return nil, ErrConflict
	}
	if a.TaskID == "" || a.InputID == "" || a.WorkspacePath == "" {
		return nil, ErrInvalid
	}
	if p.Brief.ImageAnalysisFirst {
		// 阶段与任务身份由平台绑定所有；UI 不能跳过看图或换任务继续规划。
		if !validImageAnalysisBinding(p, a) {
			return nil, ErrConflict
		}
	} else if (p.Brief.SourceOnly && a.Kind != "source") || (p.SourceAnalysis != nil && !p.Brief.SourceOnly && a.Kind != "adaptation") || (!p.Brief.SourceOnly && p.SourceAnalysis == nil && a.Kind != "") {
		return nil, ErrInvalid
	}
	adaptation := a.Kind == "adaptation"
	if adaptation {
		if p.AnalysisFee == nil || p.SourceAnalysis == nil || p.SourceAnalysis.SourceChecksum != p.SourceChecksum {
			return nil, ErrConflict
		}
		identity, err := s.Repo.Store.GetQuotaReservation(ctx, p.AnalysisFee.ID)
		if err != nil {
			return nil, err
		}
		if identity == nil || !identity.Postpaid || identity.Status != store.ReservationSettled || identity.TeamID != team || identity.UserID != p.UserID || identity.RequestID != p.AnalysisFee.RequestID {
			return nil, ErrConflict
		}
	}
	// 失败状态已落盘但释放可能中断；先关闭旧身份，避免重试覆盖唯一恢复引用。
	if p.Status == "analysis_failed" && p.AnalysisFee != nil && !adaptation {
		if err := s.releaseFixedFee(ctx, team, p.UserID, *p.AnalysisFee); err != nil {
			return nil, err
		}
	}
	var fee *FixedFee
	if adaptation {
		// 原片事实已成功结算；素材方案只复用同一费用身份，不再次准入或实扣。
		fee = p.AnalysisFee
	} else if p.Brief.ReferenceAssetID != "" {
		pricing, err := s.Repo.ReferencePricing(ctx)
		if err != nil {
			return nil, err
		}
		value := newFixedFee(team, p.UserID, "media:reference-analysis:"+p.ID+":"+a.InputID, "media:reference-analysis", pricing.AnalysisMicro)
		bindFixedFeeIdentity(ctx, &value)
		fee = &value
		if err := s.admitFixedFee(ctx, team, p.UserID, value); err != nil {
			return nil, err
		}
	}
	bound, err := s.Mutate(ctx, team, id, version, func(v *Project) error {
		if v.Status != "draft" && v.Status != "analysis_failed" && !(v.Brief.ImageAnalysisFirst && v.Status == "image_review") {
			return ErrConflict
		}
		v.Analysis = &a
		v.PlanningResult = nil
		v.PlanReview = nil
		v.AnalysisFee = fee
		v.Status = "analyzing"
		v.Error = ""
		return nil
	})
	if err != nil && fee != nil && !adaptation {
		// CAS 响应可能丢失；先确认真实绑定，不能释放并发获胜者的收费身份。
		checkCtx, checkCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer checkCancel()
		latest, readErr := s.Repo.Get(checkCtx, team, id)
		if readErr == nil && latest != nil {
			if latest.AnalysisFee != nil && latest.AnalysisFee.ID == fee.ID {
				if latest.Status == "analyzing" && latest.Analysis != nil && reflect.DeepEqual(*latest.Analysis, a) {
					return latest, nil
				}
			} else {
				_ = s.releaseFixedFee(checkCtx, team, p.UserID, *fee)
			}
		}
	}
	return bound, err
}

func (s *Service) acceptAnalysisPlan(ctx context.Context, team, id string, version int64, input string, plan Plan) (*Project, error) {
	// 模型只拥有分析脚本；片段必须由 Host 上传并经 revise 校验，不能把原片 ID 当作上传事实。
	plan = withoutReferenceClipBindings(plan)
	current, err := s.Repo.Get(ctx, team, id)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, ErrInvalid
	}
	if current.Brief.ImageAnalysisFirst && (current.ImageAnalysis == nil || current.Analysis == nil || current.Analysis.Kind != "planning") {
		return nil, ErrConflict
	}
	if current.Brief.CreativeExpansion != "" {
		if current.PlanReview == nil || current.PlanReview.InputID != input || !slices.Contains([]string{"passed", "warning", "unavailable"}, current.PlanReview.Status) {
			return nil, fmt.Errorf("video_story_review_required")
		}
		hash, hashErr := candidatePlanHash(plan)
		if hashErr != nil || hash != current.PlanReview.CandidateHash {
			return nil, fmt.Errorf("video_story_review_conflict")
		}
		if plan.Story == nil {
			return nil, fmt.Errorf("video_story_invalid")
		}
		plan.Story.Review = &StoryPlanReview{Status: current.PlanReview.Status, Issues: slices.Clone(current.PlanReview.Issues)}
		if current.PlanReview.Status == "unavailable" {
			// 复核不可用不等于原分析失败；保留合法计划和原诊断，交由既有审阅，不伪造审核通过。
			plan.Story.Review = &StoryPlanReview{Status: "warning", Issues: []string{"独立故事复核暂不可用，请检查故事、分段与声音衔接后生成。"}}
		}
	}
	if current.AnalysisFee != nil && current.Analysis != nil && current.Analysis.InputID == input && current.Plan != nil && equalAnalysisPlan(*current.Plan, plan) {
		if current.Status == "analysis_settling" {
			return s.finishAnalysisFee(ctx, current)
		}
		// 0 价也有成功结算终态，不能用金额大于零判断已受理计划的重放。
		if current.Status == "review" || current.AnalysisChargedMicro == current.AnalysisFee.Micro {
			return current, nil
		}
	}
	recovering := current.Status == "analysis_failed" && !current.CancelRequested && !current.Brief.SourceOnly && current.Analysis != nil && current.Analysis.Kind == "adaptation" && current.Analysis.InputID == input && current.AnalysisFee != nil && current.SourceAnalysis != nil && current.SourceAnalysis.SourceChecksum == current.SourceChecksum && current.Plan == nil && len(current.Shots) == 0 && current.ChargedMicro == 0 && current.OutputAssetID == ""
	if recovering {
		// 模型成功后的接收失败不能强迫用户重跑；仅同一已结算原片身份、未生成的输入允许恢复。
		identity, readErr := s.Repo.Store.GetQuotaReservation(ctx, current.AnalysisFee.ID)
		if readErr != nil {
			return nil, readErr
		}
		if identity == nil || !identity.Postpaid || identity.Status != store.ReservationSettled || identity.TeamID != team || identity.UserID != current.UserID || identity.RequestID != current.AnalysisFee.RequestID {
			return nil, ErrConflict
		}
	}
	p, err := s.Mutate(ctx, team, id, version, func(v *Project) error {
		if (v.Status != "analyzing" && !(v.Status == "analysis_ready" && planningResultEligible(v, input)) && !(recovering && v.Status == "analysis_failed")) || v.CancelRequested || v.Analysis == nil || v.Analysis.InputID != input {
			return ErrConflict
		}
		if v.Brief.SourceOnly || (v.SourceAnalysis != nil && v.Analysis.Kind != "adaptation") {
			return ErrConflict
		}
		if v.Brief.GenerationMode == "video_reference" && (v.SourceAnalysis == nil || v.Analysis.Kind != "adaptation") {
			return fmt.Errorf("video_reference_source_analysis_required")
		}
		if err := validateSourceBoundaries(v, plan); err != nil {
			return err
		}
		if err := s.validateAdaptivePlan(ctx, v.Brief, plan, v.Models, v.TemplateSnapshot); err != nil {
			return err
		}
		shots, total, err := BuildShots(v.Brief, plan, v.Models)
		if err != nil {
			return err
		}
		v.Plan = &plan
		if v.PlanningResult != nil {
			v.PlanningResult.Issues = []PlanningIssue{}
		}
		v.Shots = shots
		v.QuoteMicro = total
		v.Error = ""
		v.Status = "review"
		if !v.Brief.ReviewPlan && !storyNeedsReview(v.Brief, plan) {
			v.Status = "queued"
		}
		if v.AnalysisFee != nil && v.Analysis.Kind != "adaptation" {
			v.Status = "analysis_settling"
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if p.Status == "analysis_settling" {
		return s.finishAnalysisFee(ctx, p)
	}
	return p, nil
}

func (s *Service) finishAnalysisFee(ctx context.Context, p *Project) (*Project, error) {
	if p.Status != "analysis_settling" || p.AnalysisFee == nil || (p.Plan == nil && (!p.Brief.SourceOnly || p.SourceAnalysis == nil)) {
		return nil, ErrConflict
	}
	settlementCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.settleFixedFee(settlementCtx, p.TeamID, p.UserID, p.DeviceID, *p.AnalysisFee); err != nil {
		return nil, err
	}
	for range 10 {
		latest, err := s.Repo.Get(settlementCtx, p.TeamID, p.ID)
		if err != nil {
			return nil, err
		}
		if latest == nil || latest.AnalysisFee == nil || latest.AnalysisFee.ID != p.AnalysisFee.ID {
			return nil, ErrConflict
		}
		if latest.Status != "analysis_settling" {
			return latest, nil
		}
		result, err := s.Mutate(settlementCtx, latest.TeamID, latest.ID, latest.Version, func(v *Project) error {
			if v.Status != "analysis_settling" || v.AnalysisFee == nil || v.AnalysisFee.ID != p.AnalysisFee.ID {
				return ErrConflict
			}
			v.AnalysisChargedMicro = p.AnalysisFee.Micro
			v.Status = "review"
			if v.Brief.SourceOnly && v.SourceAnalysis != nil {
				v.Status = "source_ready"
			} else if !v.Brief.ReviewPlan && v.Plan != nil && !storyNeedsReview(v.Brief, *v.Plan) {
				// 结算恢复必须服从同一故事审核结果；不能在付清分析费用后绕过 warning/upload 审阅。
				v.Status = "queued"
			}
			if v.CancelRequested {
				v.Status = "cancelled"
			}
			return nil
		})
		if err == ErrConflict {
			continue
		}
		return result, err
	}
	return nil, ErrConflict
}
