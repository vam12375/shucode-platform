package gwapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/credits"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

const backgroundPollLease = time.Minute

var safeResponseID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

func StartBackgroundGenerationWorker(ctx context.Context, deps Deps, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			reconcileBackgroundGenerations(ctx, deps)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func reconcileBackgroundGenerations(ctx context.Context, deps Deps) {
	now := deps.Now()
	jobs, err := deps.Store.ClaimBackgroundGenerations(ctx, now, now.Add(backgroundPollLease), 20)
	if err != nil {
		deps.Logger.Error("后台生成任务领取失败", "error", err)
		return
	}
	var workers sync.WaitGroup
	for _, job := range jobs {
		job := job
		workers.Add(1)
		go func() {
			defer workers.Done()
			processBackgroundGeneration(ctx, deps, job)
		}()
	}
	workers.Wait()
}

func processBackgroundGeneration(ctx context.Context, deps Deps, job store.BackgroundGenerationJob) {
	baseURL, apiKey, err := backgroundProvider(ctx, deps, job)
	if err != nil {
		retryBackgroundGeneration(ctx, deps, job, err.Error())
		return
	}
	pollCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(pollCtx, http.MethodGet, backgroundResponseURL(baseURL, job.ResponseID, false), nil)
	if err != nil {
		retryBackgroundGeneration(ctx, deps, job, "构造上游查询请求失败")
		return
	}
	setOpenAIAuth(req, apiKey)
	client := backgroundHTTPClient(deps.HTTPClient)
	resp, err := client.Do(req)
	if err != nil {
		retryBackgroundGeneration(ctx, deps, job, "上游后台任务查询失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		retryBackgroundGeneration(ctx, deps, job, fmt.Sprintf("上游后台任务查询返回 HTTP %d", resp.StatusCode))
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamResponseBytes+1))
	if err != nil || len(body) > maxUpstreamResponseBytes {
		retryBackgroundGeneration(ctx, deps, job, "上游后台任务响应读取失败")
		return
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		retryBackgroundGeneration(ctx, deps, job, "上游后台任务响应不是合法 JSON")
		return
	}
	status, _ := payload["status"].(string)
	if !terminalBackgroundStatus(status) {
		if observed := ParseUsageFromResponsesJSON(payload); observed != nil {
			micro, costErr := credits.ComputeUsageMicroCredits(credits.UsageTokenCounts{
				InputTokens: observed.InputTokens, OutputTokens: observed.OutputTokens,
				CacheReadTokens: observed.CacheReadTokens, CacheCreationTokens: observed.CacheCreationTokens,
			}, credits.ModelPriceRates{
				InputPerMtok: job.Rates.InputPerMtok, OutputPerMtok: job.Rates.OutputPerMtok,
				CacheReadPerMtok: job.Rates.CacheReadPerMtok, CacheWritePerMtok: job.Rates.CacheWritePerMtok,
			})
			if costErr == nil && micro >= job.Record.CreditsMicro {
				// 无限额度团队没有冻结额，不能按估算预留额取消真实生成。
				unlimited := false
				if team, teamErr := deps.Store.GetTeam(ctx, job.TeamID); teamErr == nil && team != nil {
					unlimited = team.Unlimited
				}
				// 断开创建请求的 HTTP 连接不会取消后台生成，达到冻结额必须显式取消上游任务。
				if !unlimited {
					if err := cancelBackgroundUpstream(ctx, deps, baseURL, apiKey, job.ResponseID); err != nil {
						deps.Logger.Error("后台生成费用达到冻结额，取消请求暂未确认", "reservationId", job.ReservationID)
					}
				}
			}
		}
		_ = deps.Store.RetryBackgroundGeneration(ctx, job.ReservationID, deps.Now().Add(2*time.Second), "")
		return
	}

	completedAt := deps.Now()
	record := job.Record
	record.CreatedAt = completedAt
	latency := completedAt.Sub(job.CreatedAt).Milliseconds()
	if latency < 0 {
		latency = 0
	}
	record.LatencyMs = &latency
	if status == "completed" {
		record.Status = store.UsageStatusOk
		record.ErrorType = ""
	} else {
		record.Status = store.UsageStatusError
		record.ErrorType = "background_" + status
	}
	usage := ParseUsageFromResponsesJSON(payload)
	if usage == nil {
		record.CreditsMicro = job.Record.CreditsMicro
		if record.PriceSnapshot != nil {
			record.PriceSnapshot.Cost.Status = "unknown"
			record.PriceSnapshot.Cost.Reason = "usage_unavailable"
			record.PriceSnapshot.Cost.EstimatedMicro = nil
			record.PriceSnapshot.Cost.UsageEstimated = true
		}
		if record.ErrorType == "" {
			record.ErrorType = "background_usage_missing_estimated"
			if record.Status == store.UsageStatusOk {
				record.Status = store.UsageStatusError
			}
		}
	} else {
		record.InputTokens = usage.InputTokens
		record.OutputTokens = usage.OutputTokens
		record.CacheReadTokens = usage.CacheReadTokens
		record.CacheCreationTokens = usage.CacheCreationTokens
		charge, chargeErr := credits.ComputeUsageMicroCredits(credits.UsageTokenCounts{
			InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
			CacheReadTokens: usage.CacheReadTokens, CacheCreationTokens: usage.CacheCreationTokens,
		}, credits.ModelPriceRates{
			InputPerMtok: job.Rates.InputPerMtok, OutputPerMtok: job.Rates.OutputPerMtok,
			CacheReadPerMtok: job.Rates.CacheReadPerMtok, CacheWritePerMtok: job.Rates.CacheWritePerMtok,
		})
		if chargeErr != nil {
			record.CreditsMicro = job.Record.CreditsMicro
			record.Status = store.UsageStatusError
			record.ErrorType = "background_usage_invalid_estimated"
		} else {
			record.CreditsMicro = charge
			updateBackgroundCostSnapshot(record.PriceSnapshot, *usage)
		}
	}
	settleCtx, cancelSettle := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	settleErr := deps.Store.SettleReservation(settleCtx, job.ReservationID, record)
	cancelSettle()
	if settleErr != nil && !errors.Is(settleErr, store.ErrCreditChargeCapped) {
		retryBackgroundGeneration(ctx, deps, job, "后台生成结算失败: "+settleErr.Error())
		return
	}
	contentType := strings.TrimSpace(resp.Header.Get("content-type"))
	if contentType == "" {
		contentType = "application/json"
	}
	finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	finishErr := deps.Store.FinishBackgroundGeneration(finishCtx, job.ReservationID, body, http.StatusOK, contentType, completedAt)
	cancelFinish()
	if finishErr != nil {
		deps.Logger.Error("后台生成已结算但最终响应留存失败", "reservationId", job.ReservationID, "error", finishErr)
		return
	}
	if errors.Is(settleErr, store.ErrCreditChargeCapped) {
		deps.Logger.Warn("后台生成结算已按团队额度下限封顶", "requestId", record.RequestID, "reservationId", job.ReservationID)
	}
}

func cancelBackgroundUpstream(ctx context.Context, deps Deps, baseURL, apiKey, responseID string) error {
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cancelCtx, http.MethodPost, backgroundResponseURL(baseURL, responseID, true), nil)
	if err != nil {
		return err
	}
	setOpenAIAuth(req, apiKey)
	resp, err := backgroundHTTPClient(deps.HTTPClient).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxUpstreamErrorBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cancel status %d", resp.StatusCode)
	}
	return nil
}

func retryBackgroundGeneration(ctx context.Context, deps Deps, job store.BackgroundGenerationJob, reason string) {
	delay := time.Duration(1<<min(job.Attempts, int64(8))) * time.Second
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	retryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := deps.Store.RetryBackgroundGeneration(retryCtx, job.ReservationID, deps.Now().Add(delay), reason); err != nil {
		deps.Logger.Error("后台生成重试计划写入失败", "reservationId", job.ReservationID, "error", err)
		return
	}
	deps.Logger.Warn("后台生成暂未完成，额度预留保持有效", "reservationId", job.ReservationID, "attempt", job.Attempts, "retryAfter", delay)
}

func updateBackgroundCostSnapshot(snapshot *store.UsagePricingSnapshot, usage UsageCounts) {
	if snapshot == nil || snapshot.Cost.Rates == nil {
		return
	}
	rates := snapshot.Cost.Rates
	parse := func(raw string) float64 { value, _ := strconv.ParseFloat(raw, 64); return value }
	factor := parseSnapshotMultiplier(rates.Multiplier)
	micro, err := credits.ComputeUsageMicroCredits(credits.UsageTokenCounts{
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		CacheReadTokens: usage.CacheReadTokens, CacheCreationTokens: usage.CacheCreationTokens,
	}, credits.ModelPriceRates{
		InputPerMtok: parse(rates.InputPerMtok) * factor, OutputPerMtok: parse(rates.OutputPerMtok) * factor,
		CacheReadPerMtok: parse(rates.CacheReadPerMtok) * factor, CacheWritePerMtok: parse(rates.CacheWritePerMtok) * factor,
	})
	if err == nil {
		snapshot.Cost.Status, snapshot.Cost.Reason = "estimated", ""
		snapshot.Cost.EstimatedMicro = &micro
		snapshot.Cost.UsageEstimated = false
	}
}

func backgroundProvider(ctx context.Context, deps Deps, job store.BackgroundGenerationJob) (string, string, error) {
	if job.ProviderID == "" {
		base := strings.TrimRight(job.BaseURL, "/")
		if base == "" {
			base = strings.TrimRight(deps.Env.UpstreamBaseURL, "/")
		}
		return base, deps.Env.UpstreamAPIKey, nil
	}
	provider, err := deps.Store.GetUpstreamProvider(ctx, job.ProviderID)
	if err != nil {
		return "", "", err
	}
	if provider == nil {
		return "", "", fmt.Errorf("后台生成供应商已不存在")
	}
	key, err := decryptProviderKey(deps, provider)
	if err != nil {
		return "", "", err
	}
	// 任务绑定创建时的 endpoint，供应商后来改址不能把既有任务与凭据发送到新目标。
	base := strings.TrimRight(job.BaseURL, "/")
	if base == "" {
		base = strings.TrimRight(provider.BaseURL, "/")
	}
	return base, key, nil
}

func backgroundResponseURL(baseURL, responseID string, cancel bool) string {
	path := "/v1/responses/" + url.PathEscape(responseID)
	if cancel {
		path += "/cancel"
	}
	return strings.TrimRight(baseURL, "/") + path
}

func backgroundHTTPClient(client *http.Client) *http.Client {
	base := &http.Client{Timeout: 20 * time.Second}
	if client != nil {
		cloned := *client
		base = &cloned
	}
	if base.Timeout == 0 {
		base.Timeout = 20 * time.Second
	}
	// 轮询/取消会携带供应商密钥，绝不能跟随重定向到其他 endpoint。
	base.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errors.New("上游重定向被禁止")
	}
	return base
}

func setOpenAIAuth(req *http.Request, apiKey string) {
	req.Header.Set("accept", "application/json")
	if apiKey != "" {
		req.Header.Set("authorization", "Bearer "+apiKey)
	}
}

func terminalBackgroundStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled", "canceled", "incomplete":
		return true
	default:
		return false
	}
}

func backgroundResponseIdentity(raw []byte) (string, string) {
	var payload struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return "", ""
	}
	return strings.TrimSpace(payload.ID), strings.TrimSpace(payload.Status)
}

func handleBackgroundResponseProxy(c *gin.Context, deps Deps, method string, cancel bool) {
	_, user, ok := authenticate(c.Request, deps, openAIError, c)
	if !ok {
		return
	}
	if !safeResponseID.MatchString(c.Param("responseID")) {
		openAIError(c, 404, "not_found_error", "后台生成任务不存在")
		return
	}
	membership, _, ok := resolveGatewayTeam(c, deps, user.ID, openAIError)
	if !ok {
		return
	}
	job, err := deps.Store.FindBackgroundGeneration(c.Request.Context(), membership.Team.ID, c.Param("responseID"))
	if err != nil {
		deps.Logger.Error("后台生成任务查询失败", "error", err)
		openAIError(c, 500, "api_error", "网关内部错误")
		return
	}
	if job == nil || job.UserID != user.ID {
		openAIError(c, 404, "not_found_error", "后台生成任务不存在")
		return
	}
	if method == http.MethodDelete && job.CompletedAt == nil {
		// 删除前强制 worker 轮询一次并结算；未终结任务不能删除或释放额度。
		if err := deps.Store.RetryBackgroundGeneration(c.Request.Context(), job.ReservationID, deps.Now().Add(-time.Second), ""); err != nil {
			openAIError(c, 500, "api_error", "后台任务状态更新失败")
			return
		}
		reconcileBackgroundGenerations(c.Request.Context(), deps)
		job, err = deps.Store.FindBackgroundGeneration(c.Request.Context(), membership.Team.ID, c.Param("responseID"))
		if err != nil {
			openAIError(c, 500, "api_error", "网关内部错误")
			return
		}
		if job == nil {
			openAIError(c, 404, "not_found_error", "后台生成任务不存在")
			return
		}
		if job.CompletedAt == nil {
			openAIError(c, 409, "conflict_error", "后台任务尚未完成，暂不能删除")
			return
		}
	}
	if job.CompletedAt != nil {
		if method == http.MethodDelete {
			baseURL, apiKey, err := backgroundProvider(c.Request.Context(), deps, *job)
			if err != nil {
				deps.Logger.Warn("读取后台生成供应商失败", "reservationId", job.ReservationID, "error", err)
				openAIError(c, 502, "api_error", "后台任务供应商暂时不可用，额度仍在预留")
				return
			}
			deleteCtx, cancelDelete := context.WithTimeout(c.Request.Context(), 20*time.Second)
			defer cancelDelete()
			req, err := http.NewRequestWithContext(deleteCtx, http.MethodDelete, backgroundResponseURL(baseURL, job.ResponseID, false), nil)
			if err != nil {
				openAIError(c, 500, "api_error", "网关内部错误")
				return
			}
			setOpenAIAuth(req, apiKey)
			resp, err := backgroundHTTPClient(deps.HTTPClient).Do(req)
			if err != nil {
				openAIError(c, 502, "api_error", "上游后台任务暂时不可用")
				return
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxUpstreamResponseBytes))
			resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				openAIError(c, 502, "api_error", "上游后台任务删除失败")
				return
			}
			if err := deps.Store.DeleteBackgroundGeneration(c.Request.Context(), job.ReservationID); err != nil {
				openAIError(c, 500, "api_error", "删除后台生成留存失败")
				return
			}
			c.JSON(200, gin.H{"id": job.ResponseID, "object": "response", "deleted": true})
			return
		}
		contentType := job.FinalType
		if contentType == "" {
			contentType = "application/json"
		}
		c.Data(job.FinalStatus, contentType, job.FinalResponse)
		return
	}
	baseURL, apiKey, err := backgroundProvider(c.Request.Context(), deps, *job)
	if err != nil {
		deps.Logger.Warn("读取后台生成供应商失败", "reservationId", job.ReservationID, "error", err)
		openAIError(c, 502, "api_error", "后台任务供应商暂时不可用，额度仍在预留")
		return
	}
	upstreamMethod := method
	if cancel {
		upstreamMethod = http.MethodPost
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), upstreamMethod, backgroundResponseURL(baseURL, job.ResponseID, cancel), nil)
	if err != nil {
		openAIError(c, 500, "api_error", "网关内部错误")
		return
	}
	setOpenAIAuth(req, apiKey)
	proxyCtx, proxyCancel := context.WithTimeout(req.Context(), 20*time.Second)
	defer proxyCancel()
	resp, err := backgroundHTTPClient(deps.HTTPClient).Do(req.WithContext(proxyCtx))
	if err != nil {
		openAIError(c, 502, "api_error", "上游后台任务暂时不可用")
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamResponseBytes+1))
	if err != nil || len(body) > maxUpstreamResponseBytes {
		openAIError(c, 502, "api_error", "上游后台任务响应异常")
		return
	}
	contentType := strings.TrimSpace(resp.Header.Get("content-type"))
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(resp.StatusCode, contentType, redactSecret(body, apiKey))
}
