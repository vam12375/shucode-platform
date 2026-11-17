package media

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

func nextImageQueryAt(now time.Time) time.Time {
	return now.Add(5*time.Second + time.Duration(rand.Int64N(int64(5*time.Second))))
}

func (s *Service) pollImage(ctx context.Context, p *Project, index int, shot Shot, provider Provider) error {
	if shot.ImageNextQueryAt.After(time.Now()) {
		return nil
	}
	update := func(fn func(*Shot)) error {
		_, err := s.workerUpdate(ctx, p, func(v *Project) {
			v.Status = "running"
			if v.CancelRequested {
				v.Status = "cancelling"
			}
			fn(&v.Shots[index])
		})
		return err
	}
	// 查询前先保存时刻；重启、临时错误和新 worker 都遵守同一限频事实，不能在恢复时突发轮询。
	if err := update(func(v *Shot) { v.ImageNextQueryAt = nextImageQueryAt(time.Now()) }); err != nil {
		return err
	}
	result, err := provider.QueryImage(ctx, shot.ImageTaskID)
	if err != nil {
		var upstream *providerHTTPError
		if errors.As(err, &upstream) && (upstream.status == 401 || upstream.status == 403 || upstream.status == 404) {
			_, err = s.workerUpdate(ctx, p, func(v *Project) {
				v.Status, v.Error = "reconciliation_required", "provider_query_requires_reconciliation"
				v.Shots[index].Status, v.Shots[index].Error = "reconciliation_required", "provider_query_requires_reconciliation"
				recordProviderDiagnostic(v, index, "image_query", providerDiagnosticFrom(err))
			})
			return err
		}
		_, updateErr := s.workerUpdate(ctx, p, func(v *Project) {
			shot := &v.Shots[index]
			shot.Error = "provider_query_retrying"
			if upstream != nil && upstream.status == 429 && upstream.retryAfterAt.After(shot.ImageNextQueryAt) {
				shot.ImageNextQueryAt = upstream.retryAfterAt
			}
			recordProviderDiagnostic(v, index, "image_query", providerDiagnosticFrom(err))
		})
		return updateErr
	}
	switch result.Status {
	case "queued", "running":
		return update(func(v *Shot) { v.Error = "" })
	case "failed":
		if err = s.Repo.Store.ReleaseReservationByID(ctx, shot.ReservationID, time.Now()); err != nil {
			return err
		}
		_, updateErr := s.workerUpdate(ctx, p, func(v *Project) {
			shot := &v.Shots[index]
			shot.Status, shot.Error = "failed", "provider_failed"
			shot.ProviderError = sanitizeProviderError(result.Error.Code)
			recordProviderDiagnostic(v, index, "image_query", result.Error.Diagnostic)
			shot.clearPending()
		})
		return updateErr
	case "succeeded":
		// URL 先落盘，下载失败只重试此结果；供应商 usage 与平台按张锁价结算无关。
		return update(func(v *Shot) {
			v.ResultURL, v.Status, v.Error = result.URL, "image_generated", ""
			v.ImageNextQueryAt = time.Time{}
		})
	default:
		return update(func(v *Shot) { v.Error = "provider_status_unknown" })
	}
}
