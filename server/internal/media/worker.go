package media

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"math"
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
	// 首帧文本本身不是生图授权；历史方案也必须显式选择 imageModel 才能开启新的生图阶段。
	if shot.Status == "pending" && p.Brief.ImageModel != "" && shot.Plan.ImagePrompt != "" && shot.ImageAssetID == "" {
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
	m, e = s.routedModel(ctx, p, index, shot, m, kind)
	if e != nil {
		return e
	}
	if kind == "image" && m.Adapter == "toapis" && shot.Status == "pending" {
		request := CanvasRequest{Resolution: firstFrameResolution(m), AspectRatio: p.Brief.AspectRatio}
		images := 0
		if shot.Plan.AssetID != "" || shot.ImageAssetID != "" {
			images = 1
		}
		if p.Canvas != nil {
			request, images = *p.Canvas, len(p.Canvas.AssetIDs)
		}
		if validateToAPIsImageInput(m, request, images) != nil {
			// 旧快照也需在准入前校验冻结图片 SKU，确定性输入拒绝不能成为未知付费请求。
			return s.failVideoReference(ctx, p, index, shot, "provider_input_unsupported")
		}
	}
	if kind == "video" && m.Adapter == "toapis" && (shot.Status == "pending" || shot.Status == "image_ready") {
		resolution, ratio, images := p.Brief.Resolution, p.Brief.AspectRatio, 1
		if p.Canvas != nil {
			resolution, ratio, images = p.Canvas.Resolution, p.Canvas.AspectRatio, len(p.Canvas.AssetIDs)
		} else if isWholeVideoReferenceExecution(p) {
			images = len(wholeReferenceImageIDs(p, shot))
		}
		if validateToAPIsInput(m, resolution, ratio, shot.Plan.Duration, images, videoReferenceRequested(p)) != nil {
			// 历史或损坏快照也必须在准入前校验型号输入，不能把本地确定性拒绝误当未知上游受理。
			return s.failVideoReference(ctx, p, index, shot, "provider_input_unsupported")
		}
	}
	var referenceVideoURL, referenceAudioURL string
	if videoReferenceRequested(p) {
		videoModelID := p.Brief.VideoModel
		if p.Canvas != nil {
			videoModelID = p.Canvas.ModelID
		}
		videoModel, findErr := FindModel(p.Models, videoModelID, "video")
		if findErr != nil || videoModel.VideoReference == nil {
			return s.failVideoReference(ctx, p, index, shot, "video_reference_unsupported")
		}
		clip, clipErr := s.ensureVideoReferenceClip(ctx, p, shot, videoModel.VideoReference.MaxDurationSeconds)
		if clipErr != nil {
			return s.failVideoReference(ctx, p, index, shot, clipErr.Error())
		}
		capability, toapisModel := toapisModelCapabilities(videoModel.UpstreamModel)
		if videoModel.Adapter == "toapis" && toapisModel && clip.Size > capability.referenceMaxBytes {
			// ToAPIs 素材上限低于平台存储上限；准入前按校验后的实际片段大小拒绝，避免付费后才发现超限。
			return s.failVideoReference(ctx, p, index, shot, "video_reference_clip_size_limit")
		}
		referenceVideoURL = clip.URL
		// 参考音频只延长卡点一致性，未绑定或超出能力上限时降级为无音频请求，不能阻断付费生成。
		if videoModel.VideoReference.MaxAudioSeconds > 0 &&
			p.Brief.ReferenceDurationSeconds > 0 &&
			p.Brief.ReferenceDurationSeconds <= float64(videoModel.VideoReference.MaxAudioSeconds) {
			audio, audioErr := s.ensureReferenceAudio(ctx, p, shot)
			if audioErr != nil {
				return s.failVideoReference(ctx, p, index, shot, audioErr.Error())
			}
			referenceAudioURL = audio.URL
		}
	}
	if shot.Status == "image_saved" || shot.Status == "video_saved" {
		usage := s.usage(p, shot, m)
		if e = s.Repo.Store.SettleReservation(ctx, shot.ReservationID, usage); e != nil {
			return e
		}
		_, e = s.workerUpdate(ctx, p, func(v *Project) {
			ss := &v.Shots[index]
			charge := usage.CreditsMicro
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
	if shot.Status == "image_running" {
		return s.pollImage(ctx, p, index, shot, provider)
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
				v.ProviderError = sanitizeProviderError(result.Error.Code)
				v.clearPending()
			}) == nil {
				return ErrConflict
			}
			return nil
		case "succeeded":
			tokenBilling := videoReferenceRequested(p) && m.Kind == "video" && m.VideoReference != nil && m.VideoReference.TokenPriceMicroPerMillion > 0
			tokens := result.Usage.CompletionTokens
			if tokens <= 0 {
				tokens = result.Usage.TotalTokens
			}
			if tokens <= 0 {
				tokens = shot.ProviderUsageTokens
			}
			if tokenBilling && tokens <= 0 {
				// 成功但供应商未返回 usage：不估算扣费，保留任务等待 root 补录 token。
				if _, err := s.workerUpdate(ctx, p, func(v *Project) {
					v.Status = "reconciliation_required"
					v.Error = "provider_usage_missing"
					v.Shots[index].ResultURL = result.Content.VideoURL
					v.Shots[index].Status = "reconciliation_required"
					v.Shots[index].Error = "provider_usage_missing"
				}); err != nil {
					return err
				}
				return nil
			}
			charge, rate, costRate := int64(0), int64(0), int64(0)
			if tokenBilling {
				rate = m.VideoReference.TokenPriceMicroPerMillion
				costRate = m.VideoReference.TokenCostMicroPerMillion
				charge = videoTokenChargeMicro(tokens, rate)
			}
			if update(func(v *Shot) {
				v.ResultURL = result.Content.VideoURL
				v.ProviderUsageTokens = tokens
				v.TokenChargeMicro = charge
				v.TokenRateMicroPerMillion = rate
				v.TokenCostMicroPerMillion = costRate
				v.Status = "video_ready"
				v.Error = ""
			}) == nil {
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
	var replacementImageIDs []string
	if kind == "video" && isWholeVideoReferenceExecution(p) {
		replacementImageIDs = wholeReferenceImageIDs(p, shot)
		if len(replacementImageIDs) == 0 || len(replacementImageIDs) > m.VideoReference.MaxImages {
			return s.failVideoReference(ctx, p, index, shot, "video_reference_unsupported")
		}
	}
	amount := m.PriceMicro
	if kind == "video" {
		amount *= int64(shot.Plan.Duration)
		if videoReferenceRequested(p) {
			videoModel, findErr := FindModel(p.Models, modelID, "video")
			if findErr != nil || videoModel.VideoReference == nil {
				return s.failVideoReference(ctx, p, index, shot, "video_reference_unsupported")
			}
			resolution, aspectRatio := p.Brief.Resolution, p.Brief.AspectRatio
			if p.Canvas != nil {
				resolution, aspectRatio = p.Canvas.Resolution, p.Canvas.AspectRatio
			}
			amount = videoReferenceQuoteMicro(videoModel.VideoReference, resolution, aspectRatio, shot.Plan.Duration)
		}
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
		var submission ProviderImageSubmission
		var e error
		if p.Canvas != nil {
			images := []string{}
			for _, id := range p.Canvas.AssetIDs {
				images = append(images, s.Blobs.URL(p.TeamID, id))
			}
			submission, e = provider.SubmitCanvasImage(ctx, m, *p.Canvas, images)
		} else {
			submission, e = provider.SubmitImage(ctx, m, shot.Plan.ImagePrompt, image, p.Brief.AspectRatio)
		}
		if e != nil {
			return s.submissionFailure(ctx, p, index, shot, e)
		}
		if update(func(v *Shot) {
			if submission.TaskID != "" {
				v.ImageTaskID, v.Status = submission.TaskID, "image_running"
				v.ImageNextQueryAt = nextImageQueryAt(time.Now())
			} else {
				v.ResultURL, v.Status = submission.URL, "image_generated"
			}
		}) == nil {
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
		if referenceVideoURL != "" {
			id, e = provider.CanvasVideo(ctx, m, *p.Canvas, images, referenceVideoURL)
		} else {
			id, e = provider.CanvasVideo(ctx, m, *p.Canvas, images)
		}
	} else if replacementImageIDs != nil {
		images := make([]string, 0, len(replacementImageIDs))
		for _, assetID := range replacementImageIDs {
			images = append(images, s.Blobs.URL(p.TeamID, assetID))
		}
		id, e = provider.SubmitWithReferenceImages(ctx, m, p.Brief, shot.Plan, images, referenceVideoURL, referenceAudioURL)
	} else {
		id, e = provider.SubmitWithReference(ctx, m, p.Brief, shot.Plan, image, referenceVideoURL)
	}
	if e != nil {
		return s.submissionFailure(ctx, p, index, shot, e)
	}
	if update(func(v *Shot) { v.VideoTaskID = id; v.Status = "video_running" }) == nil {
		return ErrConflict
	}
	return nil
}

func videoReferenceRequested(p *Project) bool {
	return (p.Canvas != nil && len(p.Canvas.VideoReferences) > 0) || p.Brief.GenerationMode == "video_reference"
}

func isWholeVideoReferenceExecution(p *Project) bool {
	return p.Canvas == nil && p.Brief.Mode == "replicate" && p.Brief.GenerationMode == "video_reference" &&
		len(p.Shots) == 1 && p.Brief.ReferenceDurationSeconds > 0 &&
		math.Abs(p.Shots[0].Plan.SourceStart) <= .001 &&
		math.Abs(p.Shots[0].Plan.SourceEnd-p.Brief.ReferenceDurationSeconds) <= .001
}

func wholeReferenceImageIDs(p *Project, shot Shot) []string {
	if shot.ImageAssetID != "" {
		return []string{shot.ImageAssetID}
	}
	// 整段执行镜头只存首张替换图；其余身份参考仍由已审阅方案按首次出现顺序提供。
	// 旧逐镜执行不走此分支，避免恢复排队任务时改变已冻结的供应商输入。
	images := []string{}
	seen := map[string]bool{}
	if p.Plan != nil {
		for _, plan := range p.Plan.Shots {
			if plan.AssetID != "" && !seen[plan.AssetID] {
				seen[plan.AssetID] = true
				images = append(images, plan.AssetID)
			}
		}
	}
	return images
}

func (s *Service) failVideoReference(ctx context.Context, p *Project, index int, shot Shot, reason string) error {
	if shot.ReservationID != "" {
		if err := s.Repo.Store.ReleaseReservationByID(ctx, shot.ReservationID, time.Now()); err != nil {
			return err
		}
	}
	_, err := s.workerUpdate(ctx, p, func(v *Project) {
		v.Status = "failed"
		v.Error = reason
		v.Shots[index].Status = "failed"
		v.Shots[index].Error = reason
		v.Shots[index].clearPending()
	})
	return err
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
	case "provider_http_400", "provider_http_401", "provider_http_402", "provider_http_403", "provider_http_404", "provider_http_413", "provider_http_422", "provider_http_429":
		if e := s.Repo.Store.ReleaseReservationByID(ctx, shot.ReservationID, time.Now()); e != nil {
			return e
		}
		_, e := s.workerUpdate(ctx, p, func(v *Project) {
			ss := &v.Shots[index]
			ss.Status = "failed"
			ss.Error = "provider_rejected"
			ss.ProviderError = providerErrorFrom(cause)
			ss.clearPending()
		})
		return e
	}
	return s.unknown(ctx, p, index)
}
