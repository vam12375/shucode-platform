package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"reflect"
	"strconv"
	"time"
)

type Service struct {
	Repo                Repository
	Blobs               Blobs
	Provider            func(context.Context, Model) (Provider, error)
	WorkerID            string
	SourceParser        *SourceParser
	ReferenceProbe      func(context.Context, string) (ReferenceMetadata, error)
	ArchiveCanvasOutput func(context.Context, CanvasArchiveInput) CanvasTeamAssetArchive
}

func (s *Service) Create(ctx context.Context, team, user, device, id string, b Brief) (*Project, error) {
	if b.SourceOnly {
		return s.CreateReferenceAnalysis(ctx, team, user, device, id, b.ReferenceAssetID)
	}
	if _, e := uuid.Parse(id); e != nil {
		return nil, ErrInvalid
	}
	if existing, e := s.Repo.Get(ctx, team, id); e != nil {
		return nil, e
	} else if existing != nil {
		if b.ReferenceAssetID != "" && b.ReferenceAssetID == existing.Brief.ReferenceAssetID {
			b.ReferenceDurationSeconds = existing.Brief.ReferenceDurationSeconds
			b.ReviewPlan = true
		}
		// 自动检索请求重放复用首次绑定，避免新上传素材改变同 requestId 的制作内容。
		if len(b.AssetIDs) == 0 {
			b.AssetIDs = existing.Brief.AssetIDs
		}
		if existing.UserID != user || !reflect.DeepEqual(existing.Brief, b) {
			return nil, ErrConflict
		}
		return existing, nil
	}
	models, e := s.Repo.Models(ctx)
	if e != nil {
		return nil, e
	}
	policy, e := s.Repo.Store.GetTeamPolicy(ctx, team)
	if e != nil {
		return nil, e
	}
	if !store.TeamModelAllowed(policy, b.VideoModel) || (b.ImageModel != "" && !store.TeamModelAllowed(policy, b.ImageModel)) {
		return nil, fmt.Errorf("model_not_allowed")
	}
	assets, e := s.Repo.Assets(ctx, team)
	if e != nil {
		return nil, e
	}
	if b.ReferenceAssetID != "" {
		found := false
		for _, asset := range assets {
			if asset.ID == b.ReferenceAssetID && asset.Kind == "video" && asset.DurationSeconds > 0 && asset.DurationSeconds <= MaxReferenceSeconds {
				b.ReferenceDurationSeconds = asset.DurationSeconds
				b.ReviewPlan = true
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("reference_asset_not_found")
		}
	} else if len(b.AssetIDs) == 0 {
		b.AssetIDs = matchProductAssets(b.Product, assets)
		if len(b.AssetIDs) == 0 {
			return nil, fmt.Errorf("product_assets_not_found")
		}
	}
	if e = ValidateBrief(b, models, assets); e != nil {
		return nil, e
	}
	now := time.Now().UTC()
	p := &Project{ID: id, TeamID: team, UserID: user, DeviceID: device, Version: 1, Status: "draft", Brief: b, Shots: []Shot{}, Models: models, CreatedAt: now, UpdatedAt: now}
	if e = s.Repo.Index(ctx, ProjectKey(team, id)); e != nil {
		return nil, e
	}
	if e = s.Repo.Save(ctx, p, nil); e != nil {
		return nil, e
	}
	return p, nil
}
func (s *Service) Mutate(ctx context.Context, team, id string, version int64, fn func(*Project) error) (*Project, error) {
	p, e := s.Repo.Get(ctx, team, id)
	if e != nil {
		return nil, e
	}
	if p == nil {
		return nil, fmt.Errorf("project_not_found")
	}
	if version != 0 && version != p.Version {
		return nil, ErrConflict
	}
	raw, _ := json.Marshal(p)
	var old Project
	_ = json.Unmarshal(raw, &old)
	if e = fn(p); e != nil {
		return nil, e
	}
	p.Version++
	p.UpdatedAt = time.Now().UTC()
	if e = s.Repo.Save(ctx, p, &old); e != nil {
		return nil, e
	}
	return p, nil
}
func (s *Service) Bind(ctx context.Context, team, id string, version int64, a Analysis) (*Project, error) {
	return s.bindAnalysis(ctx, team, id, version, a)
}
func (s *Service) SetPlan(ctx context.Context, team, id string, version int64, input string, plan Plan) (*Project, error) {
	return s.acceptAnalysisPlan(ctx, team, id, version, input, plan)
}

func (s *Service) Revise(ctx context.Context, team, id string, version int64, plan Plan) (*Project, error) {
	return s.Mutate(ctx, team, id, version, func(p *Project) error {
		if p.Status != "review" {
			return ErrConflict
		}
		if err := validateSourceBoundaries(p, plan); err != nil {
			return err
		}
		var err error
		plan, err = referenceReviewPlan(p, plan)
		if err != nil {
			return err
		}
		shots, total, err := BuildShots(p.Brief, plan, p.Models)
		if err != nil {
			return err
		}
		if p.Brief.GenerationMode == "video_reference" {
			model, findErr := FindModel(p.Models, p.Brief.VideoModel, "video")
			if findErr != nil || model.VideoReference == nil {
				return fmt.Errorf("video_reference_unsupported")
			}
			for i := range shots {
				if shots[i].Plan.ReferenceVideoAssetID == "" {
					// 保存审阅脚本只重新派生执行与报价；真实完整片段由显式生成动作准备。
					continue
				}
				start, end := shots[i].Plan.SourceStart, shots[i].Plan.SourceEnd
				source, resolveErr := s.resolveVideoReference(ctx, team, p.Brief.ReferenceAssetID, &start, &end, model.VideoReference.MaxDurationSeconds)
				if resolveErr != nil {
					return resolveErr
				}
				if source.Checksum != p.SourceChecksum {
					return fmt.Errorf("video_source_changed")
				}
				if _, resolveErr = s.validateUploadedReferenceClip(ctx, team, source, shots[i].Plan.ReferenceVideoAssetID); resolveErr != nil {
					return resolveErr
				}
				shots[i].ReferenceVideoAssetID = shots[i].Plan.ReferenceVideoAssetID
			}
		} else {
			for _, shot := range shots {
				if shot.Plan.ReferenceVideoAssetID != "" {
					return ErrInvalid
				}
			}
		}
		p.Plan, p.Shots, p.QuoteMicro = &plan, shots, total
		return nil
	})
}
func (s *Service) Action(ctx context.Context, team, id string, version int64, action string) (*Project, error) {
	return s.action(ctx, team, id, version, action, "", "analysis_failed")
}

// FailAnalysis 只保存有限业务码；原始模型异常可能含请求内容，不能成为制作单错误。
func (s *Service) FailAnalysis(ctx context.Context, team, id string, version int64, input, code string) (*Project, error) {
	if input == "" {
		return nil, ErrInvalid
	}
	switch code {
	case "analysis_failed", "video_analysis_scope_invalid", "video_analysis_scope_budget_exhausted", "video_analysis_scope_unavailable", "source_timeline_invalid", "source_url_mismatch", "source_report_invalid", "video_reference_duration_unsupported", "model_not_configured", "duration_or_budget_exceeded", "video_reference_plan_invalid", "video_reference_prompt_limit", "video_reference_unsupported", "video_reference_image_limit", "first_frame_required", "video_source_changed":
	default:
		return nil, ErrInvalid
	}
	return s.action(ctx, team, id, version, "analysis-failed", input, code)
}

func (s *Service) action(ctx context.Context, team, id string, version int64, action, input, failureCode string) (*Project, error) {
	p, err := s.Mutate(ctx, team, id, version, func(p *Project) error {
		switch action {
		case "generate":
			if p.Status != "review" {
				return ErrConflict
			}
			p.Status = "queued"
		case "cancel":
			if p.Status == "completed" || p.Status == "cancelled" {
				return ErrConflict
			}
			p.CancelRequested = true
			if p.Status == "draft" || p.Status == "analyzing" || p.Status == "source_ready" || p.Status == "review" || p.Status == "analysis_failed" {
				p.Status = "cancelled"
			}
		case "analysis-failed":
			// 版本与输入在同一 CAS 路径检查，旧会话终态不能结束后来绑定的新输入。
			sameFailedAdaptation := p.Status == "analysis_failed" && input != "" && p.Analysis != nil && p.Analysis.Kind == "adaptation" && p.Analysis.InputID == input && p.SourceAnalysis != nil && p.Plan == nil && len(p.Shots) == 0 && !p.CancelRequested
			if (p.Status != "analyzing" && !sameFailedAdaptation) || (input != "" && (p.Analysis == nil || p.Analysis.InputID != input)) {
				return ErrConflict
			}
			p.Status = "analysis_failed"
			p.Error = failureCode
		case "retry":
			if p.Status != "failed" {
				return ErrConflict
			}
			if p.CancelRequested {
				return ErrConflict
			}
			var remaining int64
			for i := range p.Shots {
				shot := &p.Shots[i]
				if shot.Status == "failed" {
					if shot.ReservationID != "" {
						return ErrConflict
					}
					shot.Status = "pending"
					shot.Error = ""
					shot.Attempt++
					shot.ReservationID = ""
					shot.VideoTaskID = ""
					shot.PendingChargeMicro = 0
					shot.ReservedMicro = 0
					shot.Stage = ""
					shot.ResultURL = ""
				}
				if shot.Status != "succeeded" {
					remaining += shot.QuoteMicro - shot.ChargedMicro
				}
			}
			budget := p.Brief.MaxCreditsMicro
			if p.Canvas != nil {
				budget = p.QuoteMicro
			}
			if remaining+p.ChargedMicro > budget {
				return fmt.Errorf("budget_exceeded")
			}
			p.Status = "queued"
			p.Error = ""
		default:
			return ErrInvalid
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if (action == "cancel" || action == "analysis-failed") && p.AnalysisFee != nil && p.AnalysisChargedMicro == 0 && (p.Status == "cancelled" || p.Status == "analysis_failed") {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := s.releaseFixedFee(cleanup, p.TeamID, p.UserID, *p.AnalysisFee); err != nil && !errors.Is(err, ErrConflict) {
			return nil, err
		}
	}
	return p, nil
}
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Tick(ctx)
		}
	}
}
func (s *Service) Tick(ctx context.Context) {
	projects, e := s.Repo.All(ctx)
	if e != nil {
		return
	}
	for _, p := range projects {
		if ctx.Err() != nil {
			return
		}
		// 归档有独立恢复事实；先处理所有成功shot，终态和部分成功也不能漏收录。
		p = s.reconcileCanvasArchives(ctx, p)
		if p.Status == "analysis_settling" {
			_, _ = s.finishAnalysisFee(ctx, &p)
			continue
		}
		if (p.Status == "analysis_failed" || p.Status == "cancelled") && p.AnalysisFee != nil && p.AnalysisChargedMicro == 0 {
			// 状态落盘与关闭收费身份之间可能退出；轮询恢复关闭，取消/失败仍为零扣。
			_ = s.releaseFixedFee(ctx, p.TeamID, p.UserID, *p.AnalysisFee)
			continue
		}
		if p.Status != "queued" && p.Status != "running" && p.Status != "composing" && p.Status != "cancelling" {
			continue
		}
		if p.LeaseOwner != "" && p.LeaseUntil.After(time.Now()) {
			continue
		}
		claimed, e := s.Mutate(ctx, p.TeamID, p.ID, p.Version, func(v *Project) error {
			v.LeaseOwner = s.WorkerID
			v.LeaseUntil = time.Now().Add(workerLeaseDuration(v.Brief.Resolution))
			return nil
		})
		if e != nil {
			continue
		}
		// 所有付费请求前先持久化 submitting；崩溃后只查询已知任务，不盲目重发。
		e = s.step(ctx, claimed)
		if e != nil && !errors.Is(e, ErrConflict) {
			_, _ = s.workerUpdate(ctx, claimed, func(v *Project) {
				v.Error = "media_worker_retrying"
				allDone := true
				for _, shot := range v.Shots {
					if shot.Status != "succeeded" {
						allDone = false
					}
				}
				// 基础设施错误保留阶段、身份与锁定售价；只有已完成付费阶段的合成失败可安全显式重试。
				if allDone {
					v.Status = "failed"
					v.Error = "composition_failed"
				}
			})
		}
		_, _ = s.workerUpdate(ctx, claimed, func(v *Project) { v.LeaseOwner = ""; v.LeaseUntil = time.Time{} })
	}
}
func (s *Service) workerUpdate(ctx context.Context, p *Project, fn func(*Project)) (*Project, error) {
	for range 10 {
		current, e := s.Repo.Get(ctx, p.TeamID, p.ID)
		if e != nil || current == nil {
			return nil, e
		}
		if current.LeaseOwner != s.WorkerID || !current.LeaseUntil.After(time.Now()) {
			return nil, ErrConflict
		}
		next, e := s.Mutate(ctx, p.TeamID, p.ID, current.Version, func(v *Project) error { fn(v); return nil })
		if errors.Is(e, ErrConflict) {
			continue
		}
		return next, e
	}
	return nil, ErrConflict
}
func (s *Service) storeOutput(ctx context.Context, team, rawURL string, kind string) (Asset, error) {
	a, e := s.Blobs.Download(ctx, team, rawURL, kind)
	if e != nil {
		return a, e
	}
	e = s.Repo.AddAsset(ctx, team, a)
	return a, e
}
func tokenRateString(microPerMillion int64) string {
	return strconv.FormatFloat(float64(microPerMillion)/1_000_000, 'f', 6, 64)
}

func (s *Service) usage(p *Project, shot Shot, m Model) store.UsageRecord {
	record := store.UsageRecord{ID: shot.ReservationID, RequestID: "media:" + shot.ReservationID, UserID: p.UserID, TeamID: p.TeamID, DeviceTokenID: p.DeviceID, Model: "media:" + m.ID, UpstreamModel: m.UpstreamModel, ProviderID: m.ProviderID, CreditsMicro: shot.pendingCharge(), Status: store.UsageStatusOk, CreatedAt: time.Now().UTC()}
	if m.Kind != "video" || !videoReferenceRequested(p) || m.VideoReference == nil || shot.ProviderUsageTokens <= 0 {
		return record
	}
	rate := shot.TokenRateMicroPerMillion
	if rate <= 0 {
		rate = m.VideoReference.TokenPriceMicroPerMillion
	}
	if rate <= 0 {
		return record
	}
	costRate := shot.TokenCostMicroPerMillion
	if costRate <= 0 {
		costRate = m.VideoReference.TokenCostMicroPerMillion
	}
	record.OutputTokens = shot.ProviderUsageTokens
	record.CreditsMicro = shot.TokenChargeMicro
	if record.CreditsMicro <= 0 {
		record.CreditsMicro = videoTokenChargeMicro(shot.ProviderUsageTokens, rate)
	}
	sale := store.UsagePriceSnapshot{
		InputPerMtok:      "0.000000",
		OutputPerMtok:     tokenRateString(rate),
		CacheReadPerMtok:  "0.000000",
		CacheWritePerMtok: "0.000000",
		Multiplier:        "1.000000",
		Currency:          "CNY",
	}
	costStatus := "priced"
	if costRate <= 0 {
		costStatus = "unknown"
	}
	cost := sale
	cost.OutputPerMtok = tokenRateString(costRate)
	record.PriceSnapshot = &store.UsagePricingSnapshot{
		SchemaVersion: 1,
		CapturedAt:    time.Now().UTC(),
		Sale:          sale,
		Cost: store.UsageCostSnapshot{
			Status:         costStatus,
			PricingVersion: m.PriceVersion,
			ModelID:        m.ID,
			Rates:          &cost,
		},
	}
	return record
}
