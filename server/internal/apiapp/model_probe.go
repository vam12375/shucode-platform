package apiapp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/store"
	"github.com/vam12375/shucode-platform/server/internal/upstreamurl"
)

// 平台超级管理员的运维诊断：走真实渠道池，不经过团队计费链路。
// 探测请求只要求最小输出，结果仅用于展示连通性与首包耗时，不写用量、不扣额度。
const (
	modelProbeDefaultTimeoutMs int64 = 20_000
	modelProbeMaxTimeoutMs     int64 = 30_000
	modelProbeMaxBodyBytes           = 256 << 10
)

type modelProbeResult struct {
	ModelID      string `json:"modelId"`
	OK           bool   `json:"ok"`
	Kind         string `json:"kind"`
	Status       *int   `json:"status"`
	FirstTokenMs *int64 `json:"firstTokenMs"`
	TotalMs      int64  `json:"totalMs"`
	TestedAt     string `json:"testedAt"`
}

// registerModelProbeRoute 需要挂在 /console/api 分组下，与 /models 共用团队边界。
func registerModelProbeRoute(group *gin.RouterGroup, deps *Deps) {
	group.POST("/models/:modelId/test", func(c *gin.Context) {
		ctx := c.Request.Context()
		user, ok := requireConsoleUser(ctx, deps, c)
		if !ok {
			return
		}
		// 最小探测同样有供应商成本；普通用户反复调用免费入口会绕过团队余额和预算。
		if user.Role != store.RoleRoot {
			c.JSON(403, fail(403, "免费模型诊断仅限平台超级管理员，请通过客户端发起计费请求"))
			return
		}

		modelID := strings.TrimSpace(c.Param("modelId"))
		if modelID == "" || len(modelID) > 200 {
			c.JSON(400, fail(400, "模型 ID 不合法"))
			return
		}
		model, err := deps.Store.GetModel(ctx, modelID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if model == nil || !model.Enabled {
			c.JSON(404, fail(404, "模型不存在或已停用"))
			return
		}
		if deps.ModelProbeLimiter != nil {
			allowed, err := deps.ModelProbeLimiter.Allow(ctx, "model-probe:"+user.ID)
			if err != nil {
				// 与登录限流一致：Redis 故障时 fail-open，不能因此阻断目录探测。
				deps.Logger.Warn("模型探测限流不可用", "error", err)
			} else if !allowed {
				c.JSON(429, fail(429, "测试过于频繁，请稍后再试"))
				return
			}
		}

		result, err := probeModelUpstream(ctx, deps, model)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.JSON(200, okData(result))
	})
}

type modelProbeTarget struct {
	provider      *store.UpstreamProviderRecord
	upstreamModel string
}

func probeModelUpstream(ctx context.Context, deps *Deps, model *store.PlatformModelRecord) (modelProbeResult, error) {
	base := modelProbeResult{ModelID: model.ModelID, TestedAt: isoString(deps.Now())}
	targets, err := resolveModelProbeTargets(ctx, deps, model)
	if err != nil {
		return base, err
	}
	if len(targets) == 0 {
		base.Kind = "not_configured"
		return base, nil
	}
	if deps.HTTPClient == nil {
		base.Kind = "config"
		return base, nil
	}

	var last modelProbeResult
	attempted := 0
	configFailed := false
	for _, target := range targets {
		// 与网关默认的 3 次尝试对齐；渠道池更大时只测前三条。
		if attempted >= 3 {
			break
		}
		apiKey, ok := decryptModelProbeKey(deps, model.ModelID, target.provider)
		if !ok {
			configFailed = true
			continue
		}
		attempted++
		result := attemptModelProbe(ctx, deps, target, apiKey)
		result.ModelID = model.ModelID
		result.TestedAt = base.TestedAt
		if result.OK {
			return result, nil
		}
		last = result
		// 与网关一致：401/403/408/429/5xx/网络错误才切换下一渠道。
		if !modelProbeRetryable(result) {
			return result, nil
		}
	}
	if attempted > 0 {
		return last, nil
	}
	if configFailed {
		base.Kind = "config"
		return base, nil
	}
	base.Kind = "not_configured"
	return base, nil
}

func decryptModelProbeKey(deps *Deps, modelID string, provider *store.UpstreamProviderRecord) (string, bool) {
	if provider.APIKeyCiphertext == "" {
		return "", true
	}
	if deps.SecretBox == nil {
		deps.Logger.Warn("模型探测缺少平台密钥", "model", modelID, "providerId", provider.ID)
		return "", false
	}
	apiKey, err := deps.SecretBox.Decrypt(provider.APIKeyCiphertext)
	if err != nil {
		deps.Logger.Warn("模型探测解密供应商密钥失败", "model", modelID, "providerId", provider.ID)
		return "", false
	}
	return apiKey, true
}

func attemptModelProbe(ctx context.Context, deps *Deps, target modelProbeTarget, apiKey string) modelProbeResult {
	provider := target.provider
	result := modelProbeResult{}
	path, payload := modelProbeRequest(provider.Protocol, target.upstreamModel)
	requestCtx, cancel := context.WithTimeout(ctx, modelProbeTimeout(provider))
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost,
		upstreamurl.Join(provider.BaseURL, path), bytes.NewReader(payload))
	if err != nil {
		result.Kind = "config"
		return result
	}
	req.Header.Set("content-type", "application/json")
	if provider.Protocol == store.ProtocolAnthropicMessages {
		if apiKey != "" {
			req.Header.Set("x-api-key", apiKey)
		}
		version := provider.AnthropicVersion
		if version == "" {
			version = anthropicVersionFallback
		}
		req.Header.Set("anthropic-version", version)
	} else if apiKey != "" {
		req.Header.Set("authorization", "Bearer "+apiKey)
	}

	started := time.Now()
	resp, err := deps.HTTPClient.Do(req)
	if err != nil {
		kind := "network"
		if requestCtx.Err() == context.DeadlineExceeded || isTimeoutError(err) {
			kind = "timeout"
		}
		result.Kind = kind
		result.TotalMs = time.Since(started).Milliseconds()
		deps.Logger.Warn("模型探测请求失败", "providerId", provider.ID, "kind", kind)
		return result
	}
	defer resp.Body.Close()

	status := resp.StatusCode
	result.Status = &status
	if status < 200 || status >= 300 {
		detailBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		result.Kind = modelProbeFailureKind(status)
		result.TotalMs = time.Since(started).Milliseconds()
		deps.Logger.Warn("模型探测上游返回异常", "providerId", provider.ID,
			"status", status, "detail", redactSecretString(string(detailBytes), apiKey))
		return result
	}

	firstTokenMs, totalMs, readErr := measureModelProbeResponse(resp.Body, started)
	result.FirstTokenMs = firstTokenMs
	result.TotalMs = totalMs
	if readErr != nil {
		result.Kind = "stream"
		deps.Logger.Warn("模型探测响应读取中断", "providerId", provider.ID, "error", readErr)
		return result
	}
	result.OK = true
	result.Kind = "ok"
	return result
}

func resolveModelProbeTargets(ctx context.Context, deps *Deps, model *store.PlatformModelRecord) ([]modelProbeTarget, error) {
	upstreamModel := strings.TrimSpace(model.UpstreamModel)
	if upstreamModel == "" {
		upstreamModel = model.ModelID
	}
	routes, err := deps.Store.ListUpstreamRoutes(ctx, model.ModelID, true)
	if err != nil {
		return nil, err
	}
	targets := make([]modelProbeTarget, 0, len(routes)+1)
	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		provider, err := deps.Store.GetUpstreamProvider(ctx, route.ProviderID)
		if err != nil {
			return nil, err
		}
		if provider == nil || !provider.Enabled {
			continue
		}
		name := upstreamModel
		if routeName := strings.TrimSpace(route.UpstreamModel); routeName != "" {
			name = routeName
		}
		targets = append(targets, modelProbeTarget{provider: provider, upstreamModel: name})
	}
	if len(targets) > 0 {
		return targets, nil
	}
	if model.UpstreamProviderID != "" {
		provider, err := deps.Store.GetUpstreamProvider(ctx, model.UpstreamProviderID)
		if err != nil {
			return nil, err
		}
		if provider != nil && provider.Enabled {
			targets = append(targets, modelProbeTarget{provider: provider, upstreamModel: upstreamModel})
		}
	}
	return targets, nil
}

func modelProbeRetryable(result modelProbeResult) bool {
	switch result.Kind {
	case "network", "timeout", "auth", "rate_limited":
		return true
	case "upstream_error":
		return result.Status != nil && (*result.Status == http.StatusRequestTimeout || *result.Status >= 500)
	default:
		return false
	}
}

func modelProbeTimeout(provider *store.UpstreamProviderRecord) time.Duration {
	timeoutMs := modelProbeDefaultTimeoutMs
	if provider.TimeoutMs > 0 {
		timeoutMs = provider.TimeoutMs
	}
	if timeoutMs > modelProbeMaxTimeoutMs {
		timeoutMs = modelProbeMaxTimeoutMs
	}
	return time.Duration(timeoutMs) * time.Millisecond
}

// modelProbeRequest 与网关的协议路径保持一致；stream=true 才能测到首包时间。
func modelProbeRequest(protocol store.UpstreamProtocol, upstreamModel string) (string, []byte) {
	switch protocol {
	case store.ProtocolOpenAIResponses:
		return "/v1/responses", mustJSONBytes(map[string]any{
			"model": upstreamModel, "input": "ping", "max_output_tokens": 16, "stream": true,
		})
	case store.ProtocolAnthropicMessages:
		return "/v1/messages", mustJSONBytes(map[string]any{
			"model": upstreamModel, "max_tokens": 1, "stream": true,
			"messages": []any{map[string]any{"role": "user", "content": "ping"}},
		})
	default:
		return "/v1/chat/completions", mustJSONBytes(map[string]any{
			"model": upstreamModel, "stream": true, "max_tokens": 1,
			"messages": []any{map[string]any{"role": "user", "content": "ping"}},
		})
	}
}

// measureModelProbeResponse 读到 EOF，分别记录首个数据包与首个内容增量的到达时间。
// 首包兜底用于部分忽略 stream 参数、直接返回整段 JSON 的上游。
func measureModelProbeResponse(body io.Reader, started time.Time) (*int64, int64, error) {
	reader := bufio.NewReaderSize(body, 32<<10)
	var firstPacketMs, firstTokenMs *int64
	totalBytes := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			totalBytes += len(line)
			elapsed := time.Since(started).Milliseconds()
			if firstPacketMs == nil && len(bytes.TrimSpace(line)) > 0 {
				value := elapsed
				firstPacketMs = &value
			}
			if firstTokenMs == nil && modelProbeLineHasContent(line) {
				value := elapsed
				firstTokenMs = &value
			}
		}
		if readErr != nil {
			totalMs := time.Since(started).Milliseconds()
			if errors.Is(readErr, io.EOF) {
				return firstContentOrPacket(firstTokenMs, firstPacketMs), totalMs, nil
			}
			return firstContentOrPacket(firstTokenMs, firstPacketMs), totalMs, readErr
		}
		if totalBytes > modelProbeMaxBodyBytes {
			return firstContentOrPacket(firstTokenMs, firstPacketMs), time.Since(started).Milliseconds(), nil
		}
	}
}

func firstContentOrPacket(firstTokenMs, firstPacketMs *int64) *int64 {
	if firstTokenMs != nil {
		return firstTokenMs
	}
	return firstPacketMs
}

func modelProbeLineHasContent(raw []byte) bool {
	line := bytes.TrimSpace(raw)
	if bytes.HasPrefix(line, []byte("data:")) {
		line = bytes.TrimSpace(line[len("data:"):])
		if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) {
			return false
		}
	}
	if len(line) == 0 || line[0] != '{' {
		return false
	}
	var payload map[string]any
	if json.Unmarshal(line, &payload) != nil {
		return false
	}
	if typ, _ := payload["type"].(string); typ == "content_block_delta" ||
		typ == "response.output_text.delta" || typ == "response.completed" {
		return true
	}
	if choices, ok := payload["choices"].([]any); ok {
		for _, rawChoice := range choices {
			choice, ok := rawChoice.(map[string]any)
			if !ok {
				continue
			}
			if delta, ok := choice["delta"].(map[string]any); ok {
				if text, _ := delta["content"].(string); text != "" {
					return true
				}
			}
			if message, ok := choice["message"].(map[string]any); ok {
				if text, _ := message["content"].(string); text != "" {
					return true
				}
			}
			if text, _ := choice["text"].(string); text != "" {
				return true
			}
		}
	}
	if delta, ok := payload["delta"].(string); ok && delta != "" {
		return true
	}
	if content, ok := payload["content"].([]any); ok && len(content) > 0 {
		return true
	}
	if _, ok := payload["output"]; ok {
		return true
	}
	return false
}

func modelProbeFailureKind(status int) string {
	switch {
	case status == 401 || status == 403:
		return "auth"
	case status == 404:
		return "not_found"
	case status == 429:
		return "rate_limited"
	default:
		return "upstream_error"
	}
}

func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
