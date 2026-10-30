package gwapp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/routepool"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"github.com/vam12375/shucode-platform/server/internal/upstreamurl"
)

// upstreamTarget:一次请求可尝试的具体上游。渠道池按 priority/weight 产出候选，
// 网关在流式响应开始前按顺序重试（429/5xx/鉴权失败/网络错误）。
type upstreamTarget struct {
	ProviderID       string
	ProviderName     string
	BaseURL          string
	APIKey           string
	AnthropicVersion string
	UpstreamModel    string
	TimeoutMs        int64
	Priority         int64
	Weight           int64
}

type routeFailure struct {
	Status  int
	Type    string
	Message string
}

func (e *routeFailure) Error() string { return e.Message }

func providerFamily(protocol store.UpstreamProtocol) string {
	if protocol == store.ProtocolAnthropicMessages {
		return "anthropic"
	}
	return "openai"
}

func providerCompatible(provider *store.UpstreamProviderRecord, spec protocolSpec) (bool, string) {
	if providerFamily(provider.Protocol) != spec.family {
		return false, "上游协议（" + string(provider.Protocol) + "）与请求协议（" + spec.family + "）不一致"
	}
	// 旧逻辑只拦截 Responses 供应商处理 Chat 请求，漏掉了反向情况，导致 Chat 选项实际混入两个端点。
	if provider.Protocol == store.ProtocolOpenAIChatCompletions && spec.upstreamPath != "/v1/chat/completions" {
		return false, "供应商按 Chat Completions 协议接入，不支持该请求路径"
	}
	if provider.Protocol == store.ProtocolOpenAIResponses && spec.upstreamPath != "/v1/responses" {
		return false, "供应商按 Responses 协议接入，不支持该请求路径"
	}
	if provider.Protocol == store.ProtocolAnthropicMessages && spec.upstreamPath != "/v1/messages" {
		return false, "供应商按 Anthropic Messages 协议接入，不支持该请求路径"
	}
	return true, ""
}

func decryptProviderKey(deps Deps, provider *store.UpstreamProviderRecord) (string, error) {
	if provider.APIKeyCiphertext == "" {
		return "", nil
	}
	if deps.SecretBox == nil {
		return "", fmt.Errorf("平台密钥未配置，无法解析上游凭据")
	}
	return deps.SecretBox.Decrypt(provider.APIKeyCiphertext)
}

func disabledProviderMessage(provider *store.UpstreamProviderRecord) string {
	reason := "已手动停用"
	if provider.DisabledMode == "auto" {
		detail := provider.AutoDisabledReason
		if detail == "" {
			detail = "上游异常"
		}
		reason = "自动熔断（连续失败：" + detail + "）"
	}
	return "上游供应商不可用（" + reason + "）"
}

// resolveTargets:渠道池优先；没有路由时回退模型单绑定；再回退 env 单上游。
func resolveTargets(ctx context.Context, deps Deps, model *store.PlatformModelRecord, spec protocolSpec) ([]upstreamTarget, *routeFailure) {
	routes, err := deps.Store.ListUpstreamRoutes(ctx, model.ModelID, true)
	if err != nil {
		deps.Logger.Error("模型路由查询失败", "model", model.ModelID, "error", err)
		return nil, &routeFailure{Status: 500, Type: "api_error", Message: "网关内部错误"}
	}
	configuredRoutes := len(routes) > 0
	targets := []upstreamTarget{}
	lastReason := ""
	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		provider, err := deps.Store.GetUpstreamProvider(ctx, route.ProviderID)
		if err != nil {
			deps.Logger.Error("供应商查询失败", "providerId", route.ProviderID, "error", err)
			continue
		}
		if provider == nil || !provider.Enabled {
			if provider != nil {
				lastReason = disabledProviderMessage(provider)
			}
			continue
		}
		if compatible, reason := providerCompatible(provider, spec); !compatible {
			lastReason = reason
			continue
		}
		apiKey, err := decryptProviderKey(deps, provider)
		if err != nil {
			deps.Logger.Error("供应商凭据解密失败", "providerId", provider.ID, "error", err)
			lastReason = "上游凭据解密失败"
			continue
		}
		upstreamModel := strings.TrimSpace(route.UpstreamModel)
		if upstreamModel == "" {
			upstreamModel = strings.TrimSpace(model.UpstreamModel)
		}
		if upstreamModel == "" {
			upstreamModel = model.ModelID
		}
		targets = append(targets, upstreamTarget{
			ProviderID: provider.ID, ProviderName: provider.Name,
			BaseURL: provider.BaseURL, APIKey: apiKey,
			AnthropicVersion: provider.AnthropicVersion,
			UpstreamModel:    upstreamModel, TimeoutMs: provider.TimeoutMs,
			Priority: route.Priority, Weight: route.Weight,
		})
	}

	if len(targets) > 0 {
		return orderTargets(deps.RoutePool, model.ModelID+":"+spec.upstreamPath, targets), nil
	}
	// 已配置路由池但全部不可用时，不静默回落到 env 单上游（会把请求发到非预期供应商）。
	if configuredRoutes {
		if lastReason == "" {
			lastReason = "路由池没有可用的上游供应商"
		}
		return nil, &routeFailure{Status: 404, Type: "not_found_error", Message: lastReason + "：" + model.ModelID}
	}

	// 兼容回退：模型自身仍绑定单一供应商。
	if model.UpstreamProviderID != "" {
		provider, err := deps.Store.GetUpstreamProvider(ctx, model.UpstreamProviderID)
		if err != nil {
			deps.Logger.Error("供应商查询失败", "providerId", model.UpstreamProviderID, "error", err)
			return nil, &routeFailure{Status: 500, Type: "api_error", Message: "网关内部错误"}
		}
		if provider == nil || !provider.Enabled {
			if provider != nil {
				lastReason = disabledProviderMessage(provider)
			}
			if lastReason == "" {
				lastReason = "模型绑定的上游供应商不可用"
			}
			return nil, &routeFailure{Status: 404, Type: "not_found_error", Message: lastReason + "：" + model.ModelID}
		}
		if compatible, reason := providerCompatible(provider, spec); !compatible {
			return nil, &routeFailure{Status: 404, Type: "not_found_error", Message: "模型 " + model.ModelID + " " + reason}
		}
		apiKey, err := decryptProviderKey(deps, provider)
		if err != nil {
			return nil, &routeFailure{Status: 500, Type: "api_error", Message: err.Error()}
		}
		upstreamModel := strings.TrimSpace(model.UpstreamModel)
		if upstreamModel == "" {
			upstreamModel = model.ModelID
		}
		return []upstreamTarget{{
			ProviderID: provider.ID, ProviderName: provider.Name,
			BaseURL: provider.BaseURL, APIKey: apiKey,
			AnthropicVersion: provider.AnthropicVersion,
			UpstreamModel:    upstreamModel, TimeoutMs: provider.TimeoutMs,
			Priority: 100, Weight: 1,
		}}, nil
	}

	// 最后回退：网关 env 单上游（既有部署兼容）。
	upstreamModel := strings.TrimSpace(model.UpstreamModel)
	if upstreamModel == "" {
		upstreamModel = model.ModelID
	}
	if lastReason != "" {
		// 有路由但全部不可用时，不静默回落到 env（可能把请求发到错误的上游）。
		return nil, &routeFailure{Status: 404, Type: "not_found_error", Message: lastReason + "：" + model.ModelID}
	}
	return []upstreamTarget{{
		BaseURL: deps.Env.UpstreamBaseURL, APIKey: deps.Env.UpstreamAPIKey,
		AnthropicVersion: deps.Env.AnthropicVersion,
		UpstreamModel:    upstreamModel, TimeoutMs: 0,
		Priority: 100, Weight: 1,
	}}, nil
}

// 旧随机选路只能在大量请求后近似均衡；共享轮转状态使相同权重的渠道逐次交替。
func orderTargets(scheduler *routepool.Scheduler, key string, targets []upstreamTarget) []upstreamTarget {
	if scheduler == nil {
		scheduler = routepool.New()
	}
	candidates := make([]routepool.Candidate, len(targets))
	for i, target := range targets {
		candidates[i] = routepool.Candidate{ID: target.ProviderID, Priority: target.Priority, Weight: target.Weight}
	}
	out := make([]upstreamTarget, 0, len(targets))
	for _, index := range scheduler.Order(key, candidates) {
		out = append(out, targets[index])
	}
	return out
}

// StartProviderProber:周期性探测自动熔断的供应商；恢复后重新纳入调度。
func StartProviderProber(ctx context.Context, deps Deps, interval time.Duration) {
	if interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				probeAutoDisabledProviders(ctx, deps)
			}
		}
	}()
}

func probeAutoDisabledProviders(ctx context.Context, deps Deps) {
	providers, err := deps.Store.ListUpstreamProviders(ctx, true)
	if err != nil {
		deps.Logger.Warn("自动恢复探测：供应商列表查询失败", "error", err)
		return
	}
	for i := range providers {
		provider := providers[i]
		if provider.Enabled || provider.DisabledMode != "auto" {
			continue
		}
		apiKey, err := decryptProviderKey(deps, &provider)
		if err != nil {
			deps.Logger.Warn("自动恢复探测：凭据解密失败", "providerId", provider.ID, "error", err)
			continue
		}
		if err := probeProvider(ctx, deps, provider, apiKey); err != nil {
			deps.Logger.Info("自动恢复探测未通过", "providerId", provider.ID, "error", err)
			continue
		}
		recovered, err := deps.Store.RecoverUpstreamProvider(ctx, provider.ID, deps.Now())
		if err != nil {
			deps.Logger.Warn("自动恢复写库失败", "providerId", provider.ID, "error", err)
			continue
		}
		if recovered {
			deps.Logger.Info("供应商已自动恢复", "providerId", provider.ID, "name", provider.Name)
		}
	}
}

func probeProvider(ctx context.Context, deps Deps, provider store.UpstreamProviderRecord, apiKey string) error {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	url := upstreamurl.Join(provider.BaseURL, "/v1/models")
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if apiKey != "" {
		if provider.Protocol == store.ProtocolAnthropicMessages {
			req.Header.Set("x-api-key", apiKey)
			version := provider.AnthropicVersion
			if version == "" {
				version = "2023-06-01"
			}
			req.Header.Set("anthropic-version", version)
		} else {
			req.Header.Set("authorization", "Bearer "+apiKey)
		}
	}
	client := deps.HTTPClient
	if client == nil {
		client = &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return fmt.Errorf("上游重定向被禁止")
		}}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("upstream status %d", resp.StatusCode)
	}
	return nil
}

func estimatePromptTokens(body map[string]any) int64 {
	// 费用准入保留完整 JSON 字节数加网关改写余量的保守口径。
	return int64(len(mustJSON(body))) + 128
}

func estimateRequestTokens(body map[string]any, maxTokens int64) int64 {
	// 图片的 data URI 不是自然语言：Base64 字符数会把同一张图放大成
	// 数万“token”，既不符合视觉模型计量，也会让几十张抽帧错误触发
	// 团队 TPM。文本与图片分别估算后再加输出上限；不用于费用实扣。
	textBytes, imageTokens := estimateMultimodalInput(body)
	total := ceilDiv(textBytes, 4) + imageTokens + maxTokens
	if total < 1 {
		return 1
	}
	return total
}

// 图片压缩字节数不能推出视觉 token 数；这是按每个真实协议图片输入
// 固定消耗的保守准入额度，并非供应商的实际 token 或费用估算。
const imageInputTokenAllowance int64 = 16_384

func ceilDiv(value, divisor int64) int64 {
	if value <= 0 {
		return 0
	}
	return (value + divisor - 1) / divisor
}

func estimateMultimodalInput(value any) (textBytes, imageTokens int64) {
	switch typed := value.(type) {
	case string:
		return int64(len(typed)), 0
	case []any:
		for _, item := range typed {
			text, images := estimateMultimodalInput(item)
			textBytes += text
			imageTokens += images
		}
	case map[string]any:
		imageField := imagePayloadField(typed)
		for key, item := range typed {
			textBytes += int64(len(key))
			if key == imageField {
				imageTokens += imageInputTokenAllowance
				textBytes += imagePayloadMetadataBytes(item)
				continue
			}
			text, images := estimateMultimodalInput(item)
			textBytes += text
			imageTokens += images
		}
	}
	return textBytes, imageTokens
}

func imagePayloadField(value map[string]any) string {
	// 只识别已支持协议的图片块，避免工具 schema 或普通 text 里出现
	// "image"/data URI 时跳过真实文字计量。额外的非图片字段仍按文本累计。
	typ, _ := value["type"].(string)
	switch typ {
	case "image_url": // Chat Completions
		if _, ok := value["image_url"]; ok {
			return "image_url"
		}
	case "input_image": // Responses
		for _, field := range []string{"image_url", "file_id"} {
			if _, ok := value[field]; ok {
				return field
			}
		}
	case "image": // Anthropic Messages
		if source, ok := value["source"].(map[string]any); ok {
			if sourceType, _ := source["type"].(string); sourceType == "url" {
				if _, exists := source["url"]; exists {
					return "source"
				}
			}
			if mediaType, _ := source["media_type"].(string); strings.HasPrefix(mediaType, "image/") {
				if _, exists := source["data"]; exists {
					return "source"
				}
			}
		}
	}
	return ""
}

func imagePayloadMetadataBytes(value any) int64 {
	// 图片载荷旁的 detail、source.type 或其它文字仍计量，不能因为同处
	// 图片对象就把额外大段文本一起隐藏。只排除协议承载图片的字段值。
	payload, ok := value.(map[string]any)
	if !ok {
		return 0
	}
	var textBytes int64
	for key, item := range payload {
		textBytes += int64(len(key))
		if key == "url" || key == "data" {
			continue
		}
		textBytes += int64(len(mustJSON(item)))
	}
	return textBytes
}
