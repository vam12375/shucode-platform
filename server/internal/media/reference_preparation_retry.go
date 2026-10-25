package media

import (
	"context"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func withoutReferenceClipBindings(plan Plan) Plan {
	plan.Shots = append([]ShotPlan(nil), plan.Shots...)
	for i := range plan.Shots {
		plan.Shots[i].ReferenceVideoAssetID = ""
	}
	return plan
}

func referencePreparationFailure(code string) bool {
	return code == "video_reference_clip_invalid" || code == "video_reference_clip_required"
}

func referenceExecutionHasEvidence(shot Shot) bool {
	return shot.VideoTaskID != "" || shot.ImageTaskID != "" || shot.ProviderError != nil || shot.ImageAssetID != "" || shot.OutputAssetID != "" || shot.ResultURL != "" || shot.ReservationID != "" || shot.Stage != "" || shot.ChargedMicro != 0 || shot.PendingChargeMicro != 0 || shot.ReservedMicro != 0 || shot.ReleasedMicro != 0 || shot.ProviderUsageTokens != 0 || shot.TokenChargeMicro != 0 || shot.TokenRateMicroPerMillion != 0 || shot.TokenCostMicroPerMillion != 0
}

func preserveReferencePreparationAttempt(p *Project, plan Plan, shots []Shot, quote int64) {
	if !isWholeVideoReferenceExecution(p) || len(shots) != 1 || p.Plan == nil || p.Shots[0].Attempt <= 1 || p.CancelRequested || p.ChargedMicro != 0 || p.OutputAssetID != "" || p.LeaseUntil.After(time.Now()) {
		return
	}
	current := p.Shots[0]
	if current.Status != "pending" || current.Error != "" || referenceExecutionHasEvidence(current) || quote != p.QuoteMicro || current.QuoteMicro != quote || !equalAnalysisPlan(*p.Plan, plan) {
		return
	}
	previous, next := current.Plan, shots[0].Plan
	previous.ReferenceVideoAssetID, next.ReferenceVideoAssetID = "", ""
	if previous == next {
		// retry 已推进本次 attempt；只绑定已上传片段不能被 BuildShots 重置，显式改脚本仍按原规则重建。
		shots[0].Attempt = current.Attempt
	}
}

func (s *Service) retryReferencePreparation(ctx context.Context, p *Project) error {
	if !isWholeVideoReferenceExecution(p) || p.Brief.SourceOnly || p.Brief.ReferenceAssetID == "" || p.Plan == nil || p.SourceAnalysis == nil || p.AnalysisFee == nil || p.ChargedMicro != 0 || p.OutputAssetID != "" || p.LeaseUntil.After(time.Now()) {
		return ErrConflict
	}
	shot := &p.Shots[0]
	// 该业务码在本次 provider/admission 前确定失败；clearPending 会清准入字段，不能据空值审计历史 attempt。
	// 仅恢复当前无受理、输出、费用或核对证据的单次完整执行，不能让显式 retry 绕过未知受理边界。
	if shot.Status != "failed" || shot.Error != p.Error || referenceExecutionHasEvidence(*shot) {
		return ErrConflict
	}
	plan := withoutReferenceClipBindings(*p.Plan)
	if validateSourceAnalysis(p, *p.SourceAnalysis) != nil || validateSourceBoundaries(p, plan) != nil {
		return ErrConflict
	}
	shots, quote, err := BuildShots(p.Brief, plan, p.Models)
	if err != nil || len(shots) != 1 || quote != p.QuoteMicro || shot.QuoteMicro != quote {
		return ErrConflict
	}
	execution := shot.Plan
	execution.ReferenceVideoAssetID = ""
	if execution != shots[0].Plan {
		return ErrConflict
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
	shot.Plan.ReferenceVideoAssetID, shot.ReferenceVideoAssetID = "", ""
	shot.Status, shot.Error = "pending", ""
	shot.Attempt++
	p.Status, p.Error = "review", ""
	p.LeaseOwner, p.LeaseUntil = "", time.Time{}
	return nil
}
