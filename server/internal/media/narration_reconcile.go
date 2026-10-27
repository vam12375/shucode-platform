package media

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/netguard"
)

func NarrationNeedsReconciliation(p Project) bool {
	return p.Narration != nil && (p.Narration.Status == "submitting" || p.Narration.Status == "unknown") && !p.LeaseUntil.After(time.Now())
}

// 配音核对复用 root-only 路由与审计；确认旧请求后才能恢复，改脚本不能跳过未知受理。
func (s *Service) reconcileNarration(ctx context.Context, team, id string, r Reconciliation) (*Project, error) {
	if r.Shot != -1 || r.Tokens != 0 || (r.Outcome == "narration_result" && (!ValidURL(r.Value) || len(r.Value) > 4000)) || (r.Outcome == "narration_not_accepted" && r.Value != "") {
		return nil, ErrInvalid
	}
	p, err := s.Mutate(ctx, team, id, r.Version, func(v *Project) error {
		if !NarrationNeedsReconciliation(*v) || v.Canvas != nil || v.Brief.Finishing == nil || !v.Brief.Finishing.Voiceover || v.Narration.RequestID == "" {
			return ErrConflict
		}
		v.LeaseOwner, v.LeaseUntil = s.WorkerID, time.Now().Add(3*time.Minute)
		return nil
	})
	if err != nil {
		return nil, err
	}
	finished := false
	defer func() {
		if finished {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = s.workerUpdate(cleanup, p, func(v *Project) { v.LeaseOwner = ""; v.LeaseUntil = time.Time{} })
	}()
	var asset Asset
	if r.Outcome == "narration_result" {
		readCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		req, makeErr := http.NewRequestWithContext(readCtx, "GET", r.Value, nil)
		if makeErr != nil {
			return nil, ErrInvalid
		}
		client := http.Client{Transport: netguard.NewTransport(false), Timeout: time.Minute}
		if s.Blobs.Client != nil {
			client = *s.Blobs.Client
		}
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		response, readErr := client.Do(req)
		if readErr != nil {
			return nil, fmt.Errorf("narration_result_download_failed")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("narration_result_download_failed")
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxNarrationBytes+1))
		if readErr != nil || len(raw) == 0 || len(raw) > maxNarrationBytes {
			return nil, fmt.Errorf("narration_result_download_failed")
		}
		asset, err = s.persistNarrationAudio(ctx, team, raw)
		if err != nil {
			return nil, err
		}
	}
	next, err := s.workerUpdate(ctx, p, func(v *Project) {
		if v.Narration == nil || v.Narration.RequestID != p.Narration.RequestID {
			return
		}
		if r.Outcome == "narration_not_accepted" {
			v.Narration.Status = "failed"
			v.Status, v.Error = "failed", "narration_not_accepted"
		} else {
			v.Narration.Status, v.Narration.AssetID, v.Narration.DurationSeconds = "ready", asset.ID, asset.DurationSeconds
			for i := range v.Shots {
				shot := &v.Shots[i]
				// 配音前置失败没有提交视频；只恢复这类未受理镜头，成功与未知视频任务仍保留。
				if shot.Status == "failed" && strings.HasPrefix(shot.Error, "narration_") && shot.ReservationID == "" && shot.VideoTaskID == "" && shot.ImageTaskID == "" && shot.OutputAssetID == "" {
					shot.Status, shot.Error = "pending", ""
				}
			}
			v.Status, v.Error = "queued", ""
		}
		if v.CancelRequested {
			v.Status = "cancelled"
		}
		v.LeaseOwner, v.LeaseUntil = "", time.Time{}
	})
	if err != nil {
		return nil, err
	}
	finished = true
	return next, nil
}
