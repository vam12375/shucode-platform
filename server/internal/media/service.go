package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"reflect"
	"time"
)

type Service struct {
	Repo     Repository
	Blobs    Blobs
	Provider func(context.Context, Model) (Provider, error)
	WorkerID string
}

func (s *Service) Create(ctx context.Context, team, user, device, id string, b Brief) (*Project, error) {
	if _, e := uuid.Parse(id); e != nil {
		return nil, ErrInvalid
	}
	if existing, e := s.Repo.Get(ctx, team, id); e != nil {
		return nil, e
	} else if existing != nil {
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
	if len(b.AssetIDs) == 0 {
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
	return s.Mutate(ctx, team, id, version, func(p *Project) error {
		if p.Status != "draft" && p.Status != "analysis_failed" {
			return ErrConflict
		}
		if a.TaskID == "" || a.InputID == "" || a.WorkspacePath == "" {
			return ErrInvalid
		}
		p.Analysis = &a
		p.Status = "analyzing"
		p.Error = ""
		return nil
	})
}
func (s *Service) SetPlan(ctx context.Context, team, id string, version int64, input string, plan Plan) (*Project, error) {
	return s.Mutate(ctx, team, id, version, func(p *Project) error {
		if p.Status != "analyzing" || p.Analysis == nil || p.Analysis.InputID != input {
			return ErrConflict
		}
		shots, total, e := BuildShots(p.Brief, plan, p.Models)
		if e != nil {
			return e
		}
		p.Plan = &plan
		p.Shots = shots
		p.QuoteMicro = total
		p.Status = "review"
		if !p.Brief.ReviewPlan {
			p.Status = "queued"
		}
		return nil
	})
}
func (s *Service) Action(ctx context.Context, team, id string, version int64, action string) (*Project, error) {
	return s.Mutate(ctx, team, id, version, func(p *Project) error {
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
			if p.Status == "draft" || p.Status == "analyzing" || p.Status == "review" || p.Status == "analysis_failed" {
				p.Status = "cancelled"
			}
		case "analysis-failed":
			if p.Status != "analyzing" {
				return ErrConflict
			}
			p.Status = "analysis_failed"
			p.Error = "analysis_failed"
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
func (s *Service) usage(p *Project, shot Shot, m Model) store.UsageRecord {
	return store.UsageRecord{ID: shot.ReservationID, RequestID: "media:" + shot.ReservationID, UserID: p.UserID, TeamID: p.TeamID, DeviceTokenID: p.DeviceID, Model: "media:" + m.ID, UpstreamModel: m.UpstreamModel, ProviderID: m.ProviderID, CreditsMicro: shot.pendingCharge(), Status: store.UsageStatusOk, CreatedAt: time.Now().UTC()}
}
