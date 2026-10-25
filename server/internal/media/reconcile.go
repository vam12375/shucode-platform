package media

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type Reconciliation struct {
	Version int64  `json:"version"`
	Shot    int    `json:"shot"`
	Outcome string `json:"outcome"`
	Value   string `json:"value"`
	Tokens  int64  `json:"tokens,omitempty"`
	Reason  string `json:"reason"`
}

// 核对先持有 CAS lease 再关闭身份；进程中断后同一核对可重试，不能与 worker 并发提交。
func (s *Service) Reconcile(ctx context.Context, team, id string, r Reconciliation) (*Project, error) {
	if r.Version <= 0 || len(strings.TrimSpace(r.Reason)) < 5 || len(r.Reason) > 1000 {
		return nil, ErrInvalid
	}
	p, err := s.Mutate(ctx, team, id, r.Version, func(p *Project) error {
		if p.Status != "reconciliation_required" || p.LeaseUntil.After(time.Now()) || r.Shot < 0 || r.Shot >= len(p.Shots) {
			return ErrConflict
		}
		shot := &p.Shots[r.Shot]
		if shot.Status != "reconciliation_required" {
			return ErrConflict
		}
		switch r.Outcome {
		case "not_accepted":
		case "video_task":
			if shot.Stage != "video" || r.Value == "" || len(r.Value) > 300 || strings.ContainsAny(r.Value, "/?#") || r.Tokens < 0 {
				return ErrInvalid
			}
		case "video_usage":
			if shot.Stage != "video" || shot.VideoTaskID == "" || r.Tokens <= 0 {
				return ErrInvalid
			}
		case "image_task":
			modelID := p.Brief.ImageModel
			if p.Canvas != nil {
				modelID = p.Canvas.ModelID
			}
			model, modelErr := FindModel(p.Models, modelID, "image")
			if shot.Stage != "image" || modelErr != nil || model.Adapter != "toapis" || validateToAPIsImageModel(model) != nil || providerTaskID(r.Value) == "" {
				return ErrInvalid
			}
		case "image_result":
			if shot.Stage != "image" || !ValidURL(r.Value) {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		p.LeaseOwner = s.WorkerID
		p.LeaseUntil = time.Now().Add(time.Minute)
		return nil
	})
	if err != nil {
		return nil, err
	}
	shot := p.Shots[r.Shot]
	if r.Outcome == "not_accepted" {
		if err = s.Repo.Store.ReleaseReservationByID(ctx, shot.ReservationID, time.Now()); err != nil {
			return nil, fmt.Errorf("reconciliation_release_pending")
		}
	}
	return s.workerUpdate(ctx, p, func(v *Project) {
		ss := &v.Shots[r.Shot]
		switch r.Outcome {
		case "not_accepted":
			ss.Status = "failed"
			ss.clearPending()
			ss.Error = "provider_not_accepted"
			v.Status = "failed"
			if v.CancelRequested {
				v.Status = "cancelled"
			}
		case "video_task":
			ss.VideoTaskID = r.Value
			if r.Tokens > 0 {
				ss.ProviderUsageTokens = r.Tokens
			}
			ss.Status = "video_running"
			v.Status = "queued"
		case "video_usage":
			ss.ProviderUsageTokens = r.Tokens
			ss.Status = "video_running"
			v.Status = "queued"
		case "image_result":
			ss.ResultURL = r.Value
			ss.Status = "image_generated"
			v.Status = "queued"
		case "image_task":
			ss.ImageTaskID = r.Value
			ss.ImageNextQueryAt = nextImageQueryAt(time.Now())
			ss.Status = "image_running"
			v.Status = "queued"
		}
		v.Error = ""
		v.LeaseOwner = ""
		v.LeaseUntil = time.Time{}
	})
}
