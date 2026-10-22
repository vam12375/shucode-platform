package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func equalAnalysisPlan(left, right Plan) bool {
	a, err := json.Marshal(left)
	if err != nil {
		return false
	}
	b, err := json.Marshal(right)
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
	if p.Brief.ReferenceAssetID != "" && p.Status == "analyzing" && p.Analysis != nil && reflect.DeepEqual(*p.Analysis, a) {
		return p, nil
	}
	if p.Version != version || (p.Status != "draft" && p.Status != "analysis_failed") {
		return nil, ErrConflict
	}
	if a.TaskID == "" || a.InputID == "" || a.WorkspacePath == "" {
		return nil, ErrInvalid
	}
	if (p.Brief.SourceOnly && a.Kind != "source") || (p.SourceAnalysis != nil && !p.Brief.SourceOnly && a.Kind != "adaptation") || (!p.Brief.SourceOnly && p.SourceAnalysis == nil && a.Kind != "") {
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
		fee = &value
		if err := s.admitFixedFee(ctx, team, p.UserID, value); err != nil {
			return nil, err
		}
	}
	bound, err := s.Mutate(ctx, team, id, version, func(v *Project) error {
		if v.Status != "draft" && v.Status != "analysis_failed" {
			return ErrConflict
		}
		v.Analysis = &a
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
	current, err := s.Repo.Get(ctx, team, id)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, ErrInvalid
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
	p, err := s.Mutate(ctx, team, id, version, func(v *Project) error {
		if v.Status != "analyzing" || v.Analysis == nil || v.Analysis.InputID != input {
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
		shots, total, err := BuildShots(v.Brief, plan, v.Models)
		if err != nil {
			return err
		}
		v.Plan = &plan
		v.Shots = shots
		v.QuoteMicro = total
		v.Status = "review"
		if !v.Brief.ReviewPlan {
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
			} else if !v.Brief.ReviewPlan {
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
