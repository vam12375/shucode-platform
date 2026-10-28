package media

import (
	"context"
	"math"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func withoutReferenceClipBindings(plan Plan) Plan {
	plan.Shots = append([]ShotPlan(nil), plan.Shots...)
	for i := range plan.Shots {
		plan.Shots[i].ReferenceVideoAssetID = ""
		// 音频绑定与视频片段同批准备，不一起清空会让 equalAnalysisPlan 误判方案已变。
		plan.Shots[i].ReferenceAudioAssetID = ""
	}
	return plan
}

func referencePreparationFailure(code string) bool {
	return code == "video_reference_clip_invalid" || code == "video_reference_clip_required"
}

func referenceExecutionHasEvidence(shot Shot) bool {
	return shot.VideoTaskID != "" || shot.ImageTaskID != "" || shot.ProviderError != nil || shot.ImageAssetID != "" || shot.OutputAssetID != "" || shot.ResultURL != "" || shot.ReservationID != "" || shot.Stage != "" || shot.ProviderID != "" || shot.ChargedMicro != 0 || shot.PendingChargeMicro != 0 || shot.ReservedMicro != 0 || shot.ReleasedMicro != 0 || shot.ProviderUsageTokens != 0 || shot.TokenChargeMicro != 0 || shot.TokenRateMicroPerMillion != 0 || shot.TokenCostMicroPerMillion != 0
}

func referenceExecutionFactsMatch(plan Plan, execution Shot, expectedVideo, expectedAudio string) bool {
	count := 0
	start, end := execution.Plan.SourceStart, execution.Plan.SourceEnd
	lastEnd := start
	for _, fact := range plan.Shots {
		if fact.SourceStart < start-.001 || fact.SourceEnd > end+.001 {
			continue
		}
		if fact.ReferenceVideoAssetID != expectedVideo || fact.ReferenceAudioAssetID != expectedAudio || math.Abs(fact.SourceStart-lastEnd) > .001 {
			return false
		}
		lastEnd = fact.SourceEnd
		count++
	}
	return count > 0 && math.Abs(lastEnd-end) <= .001
}

func preserveReferencePreparationState(p *Project, plan Plan, shots []Shot, quote int64) error {
	if !isWholeVideoReferenceExecution(p) {
		return nil
	}
	hasSucceeded := false
	for _, shot := range p.Shots {
		hasSucceeded = hasSucceeded || shot.Status == "succeeded"
	}
	// 普通 review 保存沿用原重建行为；部分成功状态则不能静默丢弃任何执行事实。
	if !hasSucceeded {
		if len(shots) == len(p.Shots) && p.Plan != nil && !p.CancelRequested && p.OutputAssetID == "" && !p.LeaseUntil.After(time.Now()) && quote == p.QuoteMicro && equalAnalysisPlan(*p.Plan, plan) {
			preserve := true
			for i, current := range p.Shots {
				if current.Status != "pending" || current.Error != "" || referenceExecutionHasEvidence(current) || current.QuoteMicro != shots[i].QuoteMicro {
					preserve = false
					break
				}
				previous, next := current.Plan, shots[i].Plan
				previous.ReferenceVideoAssetID, next.ReferenceVideoAssetID = "", ""
				previous.ReferenceAudioAssetID, next.ReferenceAudioAssetID = "", ""
				if previous != next {
					preserve = false
					break
				}
			}
			if preserve {
				for i := range shots {
					// retry 已推进失败段 attempt；只绑定已上传片段不能被 BuildShots 重置。
					shots[i].Attempt = p.Shots[i].Attempt
				}
			}
		}
		return nil
	}
	if len(shots) != len(p.Shots) || p.Plan == nil || p.CancelRequested || p.OutputAssetID != "" || p.LeaseUntil.After(time.Now()) || quote != p.QuoteMicro {
		return ErrConflict
	}
	var charged int64
	seenPending := false
	for i, current := range p.Shots {
		if current.QuoteMicro != shots[i].QuoteMicro {
			return ErrConflict
		}
		switch current.Status {
		case "succeeded":
			if seenPending {
				return ErrConflict
			}
			previous, next := current.Plan, shots[i].Plan
			previousClip, nextClip := previous.ReferenceVideoAssetID, next.ReferenceVideoAssetID
			previousAudio, nextAudio := previous.ReferenceAudioAssetID, next.ReferenceAudioAssetID
			previous.ReferenceVideoAssetID, next.ReferenceVideoAssetID = "", ""
			previous.ReferenceAudioAssetID, next.ReferenceAudioAssetID = "", ""
			// 已结算段的供应商输入与汇总脚本都是执行事实；不能通过 revise 偷换后让它重新 pending。
			if current.Error != "" || current.OutputAssetID == "" || previousClip == "" || previousClip != nextClip || previousAudio != nextAudio || current.ReferenceVideoAssetID != previousClip || current.ReferenceAudioAssetID != previousAudio || previous != next || !referenceExecutionFactsMatch(*p.Plan, current, previousClip, previousAudio) || !referenceExecutionFactsMatch(plan, shots[i], nextClip, nextAudio) {
				return ErrConflict
			}
			charged += current.ChargedMicro
		case "pending":
			if current.Error != "" || referenceExecutionHasEvidence(current) {
				return ErrConflict
			}
			seenPending = true
		default:
			return ErrConflict
		}
	}
	if charged != p.ChargedMicro {
		return ErrConflict
	}
	preservePendingAttempts := equalAnalysisPlan(*p.Plan, plan)
	for i := range shots {
		if p.Shots[i].Status == "succeeded" {
			shots[i] = p.Shots[i]
		} else if preservePendingAttempts {
			shots[i].Attempt = p.Shots[i].Attempt
		}
	}
	return nil
}

func (s *Service) retryReferencePreparation(ctx context.Context, p *Project) error {
	if !isWholeVideoReferenceExecution(p) || p.Brief.SourceOnly || p.Brief.ReferenceAssetID == "" || p.Plan == nil || p.SourceAnalysis == nil || p.AnalysisFee == nil || p.OutputAssetID != "" || p.LeaseUntil.After(time.Now()) {
		return ErrConflict
	}
	// 该业务码在本次 provider/admission 前确定失败；clearPending 会清准入字段，不能据空值审计历史 attempt。
	// 仅恢复当前无受理、输出、费用或核对证据的执行段，不能让显式 retry 绕过未知受理边界。
	failed := -1
	var charged int64
	for i, shot := range p.Shots {
		switch shot.Status {
		case "succeeded":
			if failed >= 0 {
				return ErrConflict
			}
			if shot.Error != "" || shot.OutputAssetID == "" || shot.Plan.ReferenceVideoAssetID == "" || shot.ReferenceVideoAssetID != shot.Plan.ReferenceVideoAssetID || shot.ReferenceAudioAssetID != shot.Plan.ReferenceAudioAssetID || !referenceExecutionFactsMatch(*p.Plan, shot, shot.Plan.ReferenceVideoAssetID, shot.Plan.ReferenceAudioAssetID) {
				return ErrConflict
			}
			charged += shot.ChargedMicro
		case "pending":
			if failed < 0 || shot.Error != "" || referenceExecutionHasEvidence(shot) {
				return ErrConflict
			}
		case "failed":
			if shot.Error != p.Error || referenceExecutionHasEvidence(shot) {
				return ErrConflict
			}
			if failed >= 0 {
				return ErrConflict
			}
			failed = i
		default:
			return ErrConflict
		}
	}
	if failed < 0 || charged != p.ChargedMicro {
		return ErrConflict
	}
	plan := *p.Plan
	plan.Shots = append([]ShotPlan(nil), plan.Shots...)
	for i := range plan.Shots {
		for _, execution := range p.Shots {
			if plan.Shots[i].SourceStart >= execution.Plan.SourceStart-.001 && plan.Shots[i].SourceEnd <= execution.Plan.SourceEnd+.001 {
				if execution.Status != "succeeded" {
					plan.Shots[i].ReferenceVideoAssetID = ""
					plan.Shots[i].ReferenceAudioAssetID = ""
				}
				break
			}
		}
	}
	if validateSourceAnalysis(p, *p.SourceAnalysis) != nil || validateSourceBoundaries(p, plan) != nil {
		return ErrConflict
	}
	shots, quote, err := BuildShots(p.Brief, plan, p.Models)
	if err != nil || len(shots) != len(p.Shots) || quote != p.QuoteMicro {
		return ErrConflict
	}
	for i, shot := range p.Shots {
		execution := shot.Plan
		execution.ReferenceVideoAssetID, execution.ReferenceAudioAssetID = "", ""
		candidate := shots[i].Plan
		candidate.ReferenceVideoAssetID, candidate.ReferenceAudioAssetID = "", ""
		if execution != candidate || shot.QuoteMicro != shots[i].QuoteMicro {
			return ErrConflict
		}
	}
	identity, err := s.fixedFeeIdentity(ctx, p.TeamID, p.UserID, *p.AnalysisFee)
	if err != nil {
		return err
	}
	if identity == nil || identity.Status != store.ReservationSettled || p.AnalysisChargedMicro != p.AnalysisFee.Micro {
		return ErrConflict
	}
	// 原报价、模型、源范围和分析结算均保持冻结；同一 CAS 只撤销错误片段并回到 Host 准备入口。
	p.Plan = &plan
	for i := range p.Shots {
		shot := &p.Shots[i]
		if shot.Status == "succeeded" {
			continue
		}
		shot.Plan.ReferenceVideoAssetID, shot.ReferenceVideoAssetID = "", ""
		shot.Plan.ReferenceAudioAssetID, shot.ReferenceAudioAssetID = "", ""
		shot.Status, shot.Error = "pending", ""
		if i == failed {
			shot.Attempt++
		}
	}
	p.Status, p.Error = "review", ""
	p.LeaseOwner, p.LeaseUntil = "", time.Time{}
	return nil
}
