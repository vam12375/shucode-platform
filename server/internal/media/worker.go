package media

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"time"
)

func (s *Service) step(ctx context.Context, p *Project) error {
	index := -1
	for i, shot := range p.Shots {
		if shot.Status != "succeeded" {
			index = i
			break
		}
	}
	if index < 0 {
		if p.CancelRequested {
			_, e := s.workerUpdate(ctx, p, func(v *Project) { v.Status = "cancelled" })
			return e
		}
		if p.Canvas != nil {
			_, e := s.workerUpdate(ctx, p, func(v *Project) {
				v.Status = "completed"
				if len(v.Shots) > 0 {
					v.OutputAssetID = v.Shots[0].OutputAssetID
				}
			})
			return e
		}
		return s.compose(ctx, p)
	}
	shot := p.Shots[index]
	update := func(fn func(*Shot)) *Project {
		next, e := s.workerUpdate(ctx, p, func(v *Project) {
			v.Status = "running"
			if v.CancelRequested {
				v.Status = "cancelling"
			}
			fn(&v.Shots[index])
		})
		if e != nil {
			return nil
		}
		return next
	}
	downloadVideo := func(rawURL string) error {
		a, err := s.storeOutput(ctx, p.TeamID, rawURL, "video")
		if err != nil {
			if update(func(v *Shot) { v.Status = "video_ready"; v.Error = "result_download_failed" }) == nil {
				return ErrConflict
			}
			return nil
		}
		if update(func(v *Shot) { v.OutputAssetID = a.ID; v.Status = "video_saved"; v.Error = "" }) == nil {
			return ErrConflict
		}
		return nil
	}
	if p.CancelRequested && (shot.Status == "pending" || shot.Status == "image_ready" || shot.Status == "failed") {
		if shot.ReservationID != "" {
			if e := s.Repo.Store.ReleaseReservationByID(ctx, shot.ReservationID, time.Now()); e != nil {
				return e
			}
		}
		_, e := s.workerUpdate(ctx, p, func(v *Project) {
			v.Status = "cancelled"
			v.Shots[index].clearPending()
		})
		return e
	}
	if shot.Status == "image_submitting" || shot.Status == "video_submitting" {
		_, e := s.workerUpdate(ctx, p, func(v *Project) {
			v.Status = "reconciliation_required"
			v.Error = "provider_submission_unknown"
			v.Shots[index].Status = "reconciliation_required"
		})
		return e
	}
	if shot.Status == "failed" {
		_, e := s.workerUpdate(ctx, p, func(v *Project) {
			v.Status = "failed"
			if v.CancelRequested {
				v.Status = "cancelled"
			}
			v.Error = shot.Error
		})
		return e
	}
	kind := "video"
	modelID := p.Brief.VideoModel
	if shot.Status == "pending" && shot.Plan.ImagePrompt != "" && shot.ImageAssetID == "" {
		kind = "image"
		modelID = p.Brief.ImageModel
	}
	if shot.Stage == "image" && shot.ReservationID != "" {
		kind = "image"
		modelID = p.Brief.ImageModel
	}
	if p.Canvas != nil {
		kind = p.Canvas.Kind
		modelID = p.Canvas.ModelID
	}
	if p.Canvas == nil && (shot.Status == "pending" || shot.Status == "image_ready") && ((kind == "image" && modelID == "") || (kind == "video" && shot.Plan.AssetID == "" && shot.ImageAssetID == "")) {
		// 旧快照可能绕过新计划校验；缺首帧必须在准入前失败，不能发送空图后误判为未知受理。
		if shot.ReservationID != "" {
			if e := s.Repo.Store.ReleaseReservationByID(ctx, shot.ReservationID, time.Now()); e != nil {
				return e
			}
		}
		_, e := s.workerUpdate(ctx, p, func(v *Project) {
			v.Status = "failed"
			v.Error = "first_frame_required"
			v.Shots[index].Status = "failed"
			v.Shots[index].Error = "first_frame_required"
			v.Shots[index].clearPending()
		})
		return e
	}
	m, e := FindModel(p.Models, modelID, kind)
	if e != nil {
		return e
	}
	if shot.Status == "image_saved" || shot.Status == "video_saved" {
		if e = s.Repo.Store.SettleReservation(ctx, shot.ReservationID, s.usage(p, shot, m)); e != nil {
			return e
		}
		_, e = s.workerUpdate(ctx, p, func(v *Project) {
			ss := &v.Shots[index]
			charge := ss.pendingCharge()
			ss.ChargedMicro += charge
			v.ChargedMicro += charge
			ss.clearPending()
			if shot.Status == "image_saved" {
				ss.Status = "image_ready"
				if p.Canvas != nil {
					ss.OutputAssetID = ss.ImageAssetID
					ss.Status = "succeeded"
				}
			} else {
				ss.Status = "succeeded"
			}
		})
		return e
	}
	if shot.Status == "image_generated" {
		// 生图返回 URL 已代表供应商完成；下载失败仅重试落盘，成功保存后再结算，不能重复生成。
		a, err := s.storeOutput(ctx, p.TeamID, shot.ResultURL, "image")
		if err != nil {
			if update(func(v *Shot) { v.Error = "result_download_failed" }) == nil {
				return ErrConflict
			}
			return nil
		}
		if update(func(v *Shot) { v.ImageAssetID = a.ID; v.Status = "image_saved"; v.Error = "" }) == nil {
			return ErrConflict
		}
		return nil
	}
	if shot.Status == "video_ready" && shot.ResultURL != "" {
		// 已观察到成功的 task 不应再依赖供应商查询；先保存 URL 后直接重试媒体下载。
		return downloadVideo(shot.ResultURL)
	}
	provider, e := s.Provider(ctx, m)
	if e != nil {
		return e
	}
	if shot.Status == "video_running" || shot.Status == "video_ready" {
		result, e := provider.Query(ctx, shot.VideoTaskID)
		if e != nil {
			if e.Error() == "provider_http_401" || e.Error() == "provider_http_403" || e.Error() == "provider_http_404" {
				_, err := s.workerUpdate(ctx, p, func(v *Project) {
					v.Status = "reconciliation_required"
					v.Error = "provider_query_requires_reconciliation"
					v.Shots[index].Status = "reconciliation_required"
					v.Shots[index].Error = "provider_query_requires_reconciliation"
				})
				return err
			}
			if update(func(v *Shot) { v.Error = "provider_query_retrying" }) == nil {
				return ErrConflict
			}
			return nil
		}
		switch result.Status {
		case "queued", "running":
			return nil
		case "failed", "cancelled", "expired":
			if e = s.Repo.Store.ReleaseReservationByID(ctx, shot.ReservationID, time.Now()); e != nil {
				return e
			}
			if update(func(v *Shot) {
				v.Status = "failed"
				v.Error = "provider_" + result.Status
				v.clearPending()
			}) == nil {
				return ErrConflict
			}
			return nil
		case "succeeded":
			if update(func(v *Shot) { v.ResultURL = result.Content.VideoURL; v.Status = "video_ready" }) == nil {
				return ErrConflict
			}
			return downloadVideo(result.Content.VideoURL)
		default:
			if update(func(v *Shot) { v.Error = "provider_status_unknown" }) == nil {
				return ErrConflict
			}
			return nil
		}
	}
	if shot.Status != "pending" && shot.Status != "image_ready" {
		return fmt.Errorf("invalid_shot_state")
	}
	amount := m.PriceMicro
	if kind == "video" {
		amount *= int64(shot.Plan.Duration)
	}
	if shot.ReservationID == "" {
		shot.ReservationID = uuid.NewString()
		shot.PendingChargeMicro = amount
		shot.Stage = kind
		if update(func(v *Shot) {
			v.ReservationID = shot.ReservationID
			v.PendingChargeMicro = amount
			v.ReservedMicro = 0
			v.Stage = kind
		}) == nil {
			return ErrConflict
		}
	}
	// 请求身份和锁定售价必须可恢复，但进行中的生成不再占用账户余额。
	amount = shot.pendingCharge()
	r, e := s.Repo.Store.AdmitUsage(ctx, store.ReserveQuotaInput{HeldByTask: true, ID: shot.ReservationID, UserID: p.UserID, TeamID: p.TeamID, RequestID: "media:" + shot.ReservationID, Micro: amount, TTL: 24 * time.Hour, CreatedAt: time.Now()})
	if e != nil {
		return e
	}
	if r == store.ReserveDenied || r == store.ReserveDeniedBudget || r == store.ReserveDeniedInactive {
		reason := "insufficient_credits"
		if r == store.ReserveDeniedBudget {
			reason = "budget_exhausted"
		} else if r == store.ReserveDeniedInactive {
			reason = "team_inactive"
		}
		if update(func(v *Shot) {
			v.Status = "failed"
			v.Error = reason
			v.clearPending()
		}) == nil {
			return ErrConflict
		}
		return nil
	}
	if r != store.ReserveReserved && r != store.ReserveDuplicate {
		return fmt.Errorf("unknown media usage admission result: %s", r)
	}
	if update(func(v *Shot) { v.Status = kind + "_submitting" }) == nil {
		return ErrConflict
	}
	latest, e := s.Repo.Get(ctx, p.TeamID, p.ID)
	if e != nil {
		return e
	}
	if latest.CancelRequested {
		if e = s.Repo.Store.ReleaseReservationByID(ctx, shot.ReservationID, time.Now()); e != nil {
			return e
		}
		_, e = s.workerUpdate(ctx, p, func(v *Project) {
			v.Status = "cancelled"
			v.Shots[index].Status = "pending"
			v.Shots[index].clearPending()
		})
		return e
	}
	imageID := shot.Plan.AssetID
	if shot.ImageAssetID != "" {
		imageID = shot.ImageAssetID
	}
	image := ""
	if imageID != "" {
		image = s.Blobs.URL(p.TeamID, imageID)
	}
	if p.Canvas != nil && len(p.Canvas.AssetIDs) > 0 {
		image = s.Blobs.URL(p.TeamID, p.Canvas.AssetIDs[0])
	}
	if kind == "image" {
		var url string
		var e error
		if p.Canvas != nil {
			images := []string{}
			for _, id := range p.Canvas.AssetIDs {
				images = append(images, s.Blobs.URL(p.TeamID, id))
			}
			url, e = provider.CanvasImage(ctx, m, *p.Canvas, images)
		} else {
			url, e = provider.Image(ctx, m, shot.Plan.ImagePrompt, image)
		}
		if e != nil {
			return s.submissionFailure(ctx, p, index, shot, e)
		}
		if update(func(v *Shot) { v.ResultURL = url; v.Status = "image_generated" }) == nil {
			return ErrConflict
		}
		return nil
	}
	var id string
	if p.Canvas != nil {
		images := []string{}
		for _, id := range p.Canvas.AssetIDs {
			images = append(images, s.Blobs.URL(p.TeamID, id))
		}
		id, e = provider.CanvasVideo(ctx, m, *p.Canvas, images)
	} else {
		id, e = provider.Submit(ctx, m, p.Brief, shot.Plan, image)
	}
	if e != nil {
		return s.submissionFailure(ctx, p, index, shot, e)
	}
	if update(func(v *Shot) { v.VideoTaskID = id; v.Status = "video_running" }) == nil {
		return ErrConflict
	}
	return nil
}
func (s *Service) unknown(ctx context.Context, p *Project, i int) error {
	_, e := s.workerUpdate(ctx, p, func(v *Project) {
		v.Status = "reconciliation_required"
		v.Error = "provider_submission_unknown"
		v.Shots[i].Status = "reconciliation_required"
	})
	return e
}

// 上游明确拒绝才关闭身份；超时与 5xx 无法证明未受理，保留身份与锁价核对但不占余额。
func (s *Service) submissionFailure(ctx context.Context, p *Project, index int, shot Shot, cause error) error {
	switch cause.Error() {
	case "provider_http_400", "provider_http_401", "provider_http_403", "provider_http_404", "provider_http_413", "provider_http_422", "provider_http_429":
		if e := s.Repo.Store.ReleaseReservationByID(ctx, shot.ReservationID, time.Now()); e != nil {
			return e
		}
		_, e := s.workerUpdate(ctx, p, func(v *Project) {
			ss := &v.Shots[index]
			ss.Status = "failed"
			ss.Error = "provider_rejected"
			ss.clearPending()
		})
		return e
	}
	return s.unknown(ctx, p, index)
}
