package media

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

type RetrySettings struct {
	VideoModel    string  `json:"videoModel"`
	ImageModel    *string `json:"imageModel,omitempty"`
	Resolution    string  `json:"resolution,omitempty"`
	ReferenceMode string  `json:"referenceMode,omitempty"`
}
type RetryContext struct {
	PreviousVideoModel    string `json:"previousVideoModel"`
	Plan                  Plan   `json:"plan"`
	SuccessfulShotIndices []int  `json:"successfulShotIndices"`
}
type retryState struct {
	Shots []Shot `json:"shots"`
}

func (s *Service) RetryWithModel(ctx context.Context, team, id string, version int64, settings RetrySettings) (*Project, error) {
	return s.Mutate(ctx, team, id, version, func(p *Project) error {
		if p.Status != "failed" || p.CancelRequested || p.Brief.Mode != "auto" || p.Brief.ReferenceAssetID != "" || p.Brief.GenerationMode == "video_reference" || p.Analysis == nil || p.Plan == nil || p.Canvas != nil || p.Agent != nil || p.Composition != nil || p.OutputAssetID != "" || p.LeaseUntil.After(time.Now()) {
			return ErrConflict
		}
		if len(p.Shots) == 0 || len(p.Shots) != len(p.Plan.Shots) {
			return ErrConflict
		}
		if settings.VideoModel == "" || len(settings.VideoModel) > 200 || len(settings.Resolution) > 50 || settings.ReferenceMode != "" && settings.ReferenceMode != "frames" && settings.ReferenceMode != "multimodal" || settings.ImageModel != nil && len(*settings.ImageModel) > 200 {
			return ErrInvalid
		}
		for _, shot := range p.Shots {
			if shot.ReservationID != "" || shot.PendingChargeMicro != 0 || shot.ReservedMicro != 0 || (shot.Status != "succeeded" && shot.Status != "failed") {
				return ErrConflict
			}
		}
		if p.Narration != nil && (p.Narration.Status == "unknown" || p.Narration.Status == "submitting") {
			return ErrConflict
		}
		models, err := s.Repo.Models(ctx)
		if err != nil {
			return err
		}
		policy, err := s.Repo.Store.GetTeamPolicy(ctx, team)
		if err != nil {
			return err
		}
		brief := p.Brief
		brief.VideoModel = settings.VideoModel
		if settings.Resolution != "" {
			brief.Resolution = settings.Resolution
		}
		if settings.ReferenceMode != "" {
			brief.ReferenceMode = settings.ReferenceMode
		}
		if settings.ImageModel != nil {
			brief.ImageModel = *settings.ImageModel
		}
		if !store.TeamModelAllowed(policy, brief.VideoModel) || (brief.ImageModel != "" && !store.TeamModelAllowed(policy, brief.ImageModel)) {
			return fmt.Errorf("model_not_allowed")
		}
		// 根因：直出只能一次生成；换成短时长模型后必须重新规划结构化分段，不能复制整片提示词。
		if directPromptBrief(brief) {
			brief.PromptPolicy = "staged"
		}
		assets, err := s.Repo.Assets(ctx, team)
		if err != nil {
			return err
		}
		validationBrief := brief
		if brief.ImageAnalysisFirst {
			validationBrief.ReviewPlan = false
		}
		if err = ValidateBrief(validationBrief, models, assets); err != nil {
			return err
		}
		if _, err = FindModel(models, brief.VideoModel, "video"); err != nil {
			return err
		}
		indices := []int{}
		for i, shot := range p.Shots {
			if shot.Status == "succeeded" {
				indices = append(indices, i)
			}
		}
		contextPlan := *p.Plan
		contextPlan.Shots = slices.Clone(p.Plan.Shots)
		for _, index := range indices {
			contextPlan.Shots[index] = p.Shots[index].Plan
		}
		p.RetryContext = &RetryContext{PreviousVideoModel: p.Brief.VideoModel, Plan: contextPlan, SuccessfulShotIndices: indices}
		p.RetryState = &retryState{Shots: p.Shots}
		p.Brief, p.Models = brief, models
		p.Plan, p.PlanningResult, p.PlanReview = nil, nil, nil
		p.Shots = []Shot{}
		p.QuoteMicro = p.ChargedMicro
		p.Error = ""
		p.Status = "draft"
		p.CompositionAssetID = ""
		p.LeaseOwner = ""
		p.LeaseUntil = time.Time{}
		return nil
	})
}

func shotDeliveryFrames(p ShotPlan) int {
	if p.Creative != nil {
		return p.Creative.OutFrame - p.Creative.InFrame
	}
	return p.Duration * 30
}

// 成功输出是已结算事实，只允许在原绝对交付位置逐字复用；不能因重新规划丢失或再次计费。
func retrySuccessMatches(p *Project, plan Plan) (map[int]Shot, error) {
	matches := map[int]Shot{}
	if p.RetryContext == nil || p.RetryState == nil {
		return matches, nil
	}
	type saved struct {
		shot   Shot
		offset int
	}
	frozen := []saved{}
	offset := 0
	for _, shot := range p.RetryState.Shots {
		if shot.Status == "succeeded" {
			frozen = append(frozen, saved{shot, offset})
		}
		offset += shotDeliveryFrames(shot.Plan)
	}
	offset = 0
	for i, shot := range plan.Shots {
		for _, old := range frozen {
			if old.offset == offset && reflect.DeepEqual(old.shot.Plan, shot) {
				matches[i] = old.shot
			}
		}
		offset += shotDeliveryFrames(shot)
	}
	if len(matches) != len(frozen) {
		return nil, fmt.Errorf("video_retry_success_changed")
	}
	return matches, nil
}

func preserveRetrySuccesses(p *Project, plan Plan, shots []Shot) ([]Shot, int64, error) {
	matches, err := retrySuccessMatches(p, plan)
	if err != nil {
		return nil, 0, err
	}
	attempt := 0
	if p.RetryState != nil {
		for _, shot := range p.RetryState.Shots {
			attempt = max(attempt, shot.Attempt)
		}
	}
	var total int64
	for i := range shots {
		if old, ok := matches[i]; ok {
			shots[i] = old
		} else if p.RetryState != nil {
			shots[i].Attempt = attempt + 1
		}
		total += shots[i].QuoteMicro
	}
	return shots, total, nil
}

func retryBuildInput(p *Project, plan Plan) (Brief, []Model, error) {
	brief := p.Brief
	models := slices.Clone(p.Models)
	if p.RetryContext == nil {
		return brief, models, nil
	}
	matches, err := retrySuccessMatches(p, plan)
	if err != nil {
		return brief, nil, err
	}
	target, err := FindModel(models, brief.VideoModel, "video")
	if err != nil {
		return brief, nil, err
	}
	for i, shot := range plan.Shots {
		if _, ok := matches[i]; !ok {
			if !target.DurationSupported(shot.Duration) {
				return brief, nil, fmt.Errorf("video_generation_duration_unsupported")
			}
			if shot.Creative != nil && len(shot.Creative.AssetIDs) > imageReferenceLimit(brief, target) {
				return brief, nil, fmt.Errorf("video_generation_reference_images_exceeded")
			}
		}
	}
	if len(matches) > 0 && brief.GenerationPolicy == "whole-first" {
		// 校验拍点/切点的临时模型只容纳原成功段；真实供应商准入仍使用未修改目录。
		expanded := target
		durations := target.AllowedDurations()
		for _, shot := range matches {
			durations = append(durations, shot.Plan.Duration)
		}
		slices.Sort(durations)
		expanded.Durations = slices.Compact(durations)
		expanded.MinDuration, expanded.MaxDuration = 0, 0
		if err = validateGenerationPlanDetails(brief, plan, expanded, matches); err != nil {
			return brief, nil, err
		}
		brief.GenerationPolicy = ""
	}
	return brief, models, nil
}
func (s *Service) buildRetryPlan(ctx context.Context, p *Project, plan Plan) ([]Shot, int64, error) {
	brief, models, err := retryBuildInput(p, plan)
	if err != nil {
		return nil, 0, err
	}
	if err = s.validateAdaptivePlan(ctx, brief, plan, models, p.TemplateSnapshot); err != nil {
		return nil, 0, err
	}
	matches, err := retrySuccessMatches(p, plan)
	if err != nil {
		return nil, 0, err
	}
	shots, total, err := buildShotsPreservingSuccesses(brief, plan, models, 1, matches)
	if err != nil {
		return nil, 0, err
	}
	if p.RetryContext == nil {
		return shots, total, nil
	}
	// BuildShots 仍检查精确总交付时长；新段使用真实目标能力，成功段完整匹配后原样投影。
	return preserveRetrySuccesses(p, plan, shots)
}

func cloneRetryContext(value *RetryContext) *RetryContext {
	if value == nil {
		return nil
	}
	raw, _ := json.Marshal(value)
	var copy RetryContext
	_ = json.Unmarshal(raw, &copy)
	return &copy
}
