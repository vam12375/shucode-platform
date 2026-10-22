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
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"github.com/vam12375/shucode-platform/server/internal/upstreamurl"
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
			if _, err := persistBackgroundUsage(ctx, deps, job, *observed); err != nil {
				retryBackgroundGeneration(ctx, deps, job, "后台用量观察保存失败: "+err.Error())
				return
			}
		}
		// 后台任务按终态 usage 实扣，不以估算费用中断或占用余额。
		_ = deps.Store.RetryBackgroundGeneration(ctx, job.ReservationID, deps.Now().Add(2*time.Second), "")
		return
	}

	finishBackgroundGeneration(ctx, deps, job, redactSecret(body, apiKey), status, resp.Header.Get("content-type"))
}

// 创建请求、轮询与删除共用终态结算；不能在删除上游结果前丢失最终 usage。
func finishBackgroundGeneration(ctx context.Context, deps Deps, job store.BackgroundGenerationJob, body []byte, status, contentType string) bool {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil || !terminalBackgroundStatus(status) {
		return false
	}
	// 创建 SSE 与轮询可分别观察 usage；终态使用已持久化的最新值，不能用过期 worker 快照覆盖。
	readCtx, cancelRead := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	current, readErr := deps.Store.FindBackgroundGeneration(readCtx, job.TeamID, job.ResponseID)
	cancelRead()
	if readErr != nil || current == nil {
		retryBackgroundGeneration(ctx, deps, job, "后台用量观察读取失败")
		return false
	}
	job = *current
	if job.CompletedAt != nil {
		return true
	}
	completedAt := deps.Now()
	record := job.Record
	if record.PriceSnapshot != nil {
		record.PriceSnapshot.Cost.UsageEstimated = false
	}
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
	if usage != nil {
		updated, err := persistBackgroundUsage(ctx, deps, job, *usage)
		if err != nil {
			retryBackgroundGeneration(ctx, deps, job, "后台最终用量观察保存失败: "+err.Error())
			return false
		}
		if updated.CompletedAt != nil {
			return true
		}
		record.PriceSnapshot = updated.Record.PriceSnapshot
	}
	missingUsage := usage == nil
	if missingUsage {
		if job.UsageObservedAt != nil {
			usage = &UsageCounts{InputTokens: record.InputTokens, OutputTokens: record.OutputTokens,
				CacheReadTokens: record.CacheReadTokens, CacheCreationTokens: record.CacheCreationTokens}
		}
		if record.ErrorType == "" {
			record.ErrorType = "background_usage_missing"
			if record.Status == store.UsageStatusOk {
				record.Status = store.UsageStatusError
			}
		}
	}
	// 旧任务模板 CreditsMicro 是最大输出预估，绝不能作为终态无 usage 时的真实费用。
	record.CreditsMicro = 0
	record.InputTokens, record.OutputTokens, record.CacheReadTokens, record.CacheCreationTokens = 0, 0, 0, 0
	if usage != nil {
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
			record.CreditsMicro = 0
			record.Status = store.UsageStatusError
			record.ErrorType = "background_usage_invalid"
			if record.PriceSnapshot != nil {
				record.PriceSnapshot.Cost.Status = "unknown"
				record.PriceSnapshot.Cost.Reason = "usage_invalid"
				record.PriceSnapshot.Cost.EstimatedMicro = nil
			}
		} else {
			record.CreditsMicro = charge
			updateBackgroundCostSnapshot(record.PriceSnapshot, *usage)
		}
	}
	if missingUsage && record.PriceSnapshot != nil {
		record.PriceSnapshot.Cost.Status = "unknown"
		record.PriceSnapshot.Cost.Reason = "usage_unavailable"
		if usage != nil {
			record.PriceSnapshot.Cost.Reason = "usage_incomplete"
		}
		record.PriceSnapshot.Cost.EstimatedMicro = nil
		record.PriceSnapshot.Cost.UsageEstimated = false
	}
	settleCtx, cancelSettle := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	settleErr := deps.Store.SettleReservation(settleCtx, job.ReservationID, record)
	cancelSettle()
	if settleErr != nil {
		retryBackgroundGeneration(ctx, deps, job, "后台生成结算失败: "+settleErr.Error())
		return false
	}
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		contentType = "application/json"
	}
	finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	finishErr := deps.Store.FinishBackgroundGeneration(finishCtx, job.ReservationID, body, http.StatusOK, contentType, completedAt)
	cancelFinish()
	if finishErr != nil {
		deps.Logger.Error("后台生成已结算但最终响应留存失败", "reservationId", job.ReservationID, "error", finishErr)
		return false
	}
	return true
}

func persistBackgroundUsage(ctx context.Context, deps Deps, job store.BackgroundGenerationJob, usage UsageCounts) (*store.BackgroundGenerationJob, error) {
	record := job.Record
	record.InputTokens, record.OutputTokens = usage.InputTokens, usage.OutputTokens
	record.CacheReadTokens, record.CacheCreationTokens = usage.CacheReadTokens, usage.CacheCreationTokens
	record.CreatedAt = deps.Now()
	updateBackgroundCostSnapshot(record.PriceSnapshot, usage)
	// HTTP 断连不应丢弃已观察合法 usage，且保存观察值不触发任何账本扣款。
	persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelPersist()
	return deps.Store.UpdateBackgroundGenerationUsage(persistCtx, job.ReservationID, record)
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
	deps.Logger.Warn("后台生成暂未完成，保留零占用核对任务", "reservationId", job.ReservationID, "attempt", job.Attempts, "retryAfter", delay)
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
	return upstreamurl.Join(baseURL, path)
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
	if _, scoped := c.Request.Header[http.CanonicalHeaderKey(media.AnalysisScopeHeader)]; scoped {
		c.Request.Header.Del(media.AnalysisScopeHeader)
		openAIError(c, 403, "permission_error", "video_analysis_scope_invalid")
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
	if job.CompletedAt != nil && method != http.MethodDelete {
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
		openAIError(c, 502, "api_error", "后台任务供应商暂时不可用，任务将继续核对")
		return
	}
	if method == http.MethodDelete && job.CompletedAt == nil {
		if err := cancelBackgroundUpstream(c.Request.Context(), deps, baseURL, apiKey, job.ResponseID); err != nil {
			openAIError(c, 502, "api_error", "后台任务取消尚未确认，请稍后重试")
			return
		}
		processBackgroundGeneration(c.Request.Context(), deps, *job)
		job, err = deps.Store.FindBackgroundGeneration(c.Request.Context(), membership.Team.ID, job.ResponseID)
		if err != nil || job == nil || job.CompletedAt == nil {
			openAIError(c, 409, "conflict_error", "后台任务正在取消或等待最终用量，请稍后重试删除")
			return
		}
	}
	upstreamMethod := method
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
	if method != http.MethodDelete && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, responseStatus := backgroundResponseIdentity(body)
		if terminalBackgroundStatus(responseStatus) {
			finishBackgroundGeneration(c.Request.Context(), deps, *job, redactSecret(body, apiKey), responseStatus, contentType)
		} else {
			var payload any
			if json.Unmarshal(body, &payload) == nil {
				if usage := ParseUsageFromResponsesJSON(payload); usage != nil {
					if _, err := persistBackgroundUsage(c.Request.Context(), deps, *job, *usage); err != nil {
						deps.Logger.Error("后台查询用量观察保存失败", "reservationId", job.ReservationID, "error", err)
					}
				}
			}
		}
	}
	if method == http.MethodDelete && (resp.StatusCode >= 200 && resp.StatusCode < 300 || resp.StatusCode == http.StatusNotFound) {
		if err := deps.Store.DeleteBackgroundGeneration(c.Request.Context(), job.ReservationID); err != nil {
			openAIError(c, 500, "api_error", "删除后台生成留存失败，请重试")
			return
		}
		c.JSON(200, gin.H{"id": job.ResponseID, "object": "response", "deleted": true})
		return
	}
	c.Data(resp.StatusCode, contentType, redactSecret(body, apiKey))
}
