package gwapp

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
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
	if provider.Protocol == store.ProtocolOpenAIResponses && spec.upstreamPath == "/v1/chat/completions" {
		return false, "供应商按 Responses 协议接入，不支持 chat completions 请求"
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
		return orderTargets(targets), nil
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

// orderTargets:低 priority 优先；同一 priority 内按权重做加权随机排序。
func orderTargets(targets []upstreamTarget) []upstreamTarget {
	ordered := append([]upstreamTarget(nil), targets...)
	// 稳定排序，保证同 priority 的输入顺序确定后再加权随机。
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && ordered[j].Priority < ordered[j-1].Priority; j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}
	out := make([]upstreamTarget, 0, len(ordered))
	for start := 0; start < len(ordered); {
		end := start + 1
		for end < len(ordered) && ordered[end].Priority == ordered[start].Priority {
			end++
		}
		group := append([]upstreamTarget(nil), ordered[start:end]...)
		for len(group) > 0 {
			total := int64(0)
			for _, target := range group {
				total += maxInt64(1, target.Weight)
			}
			pick := rand.Int64N(total)
			index := 0
			for i, target := range group {
				pick -= maxInt64(1, target.Weight)
				if pick < 0 {
					index = i
					break
				}
			}
			out = append(out, group[index])
			group = append(group[:index], group[index+1:]...)
		}
		start = end
	}
	return out
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
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
	url := strings.TrimRight(provider.BaseURL, "/") + "/v1/models"
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
	// /4 会低估中文与多模态输入；完整 JSON 字节数加网关改写余量作为保守上界。
	return int64(len(mustJSON(body))) + 128
}

func estimateRequestTokens(body map[string]any, maxTokens int64) int64 {
	// TPM 保持近似 token 速率口径；费用准入单独使用保守上界。
	total := int64(countJSONText(body)/4) + maxTokens
	if total < 1 {
		return 1
	}
	return total
}

func countJSONText(value any) int {
	switch typed := value.(type) {
	case string:
		return len(typed)
	case []any:
		total := 0
		for _, item := range typed {
			total += countJSONText(item)
		}
		return total
	case map[string]any:
		total := 0
		for key, item := range typed {
			total += len(key) + countJSONText(item)
		}
		return total
	default:
		return 0
	}
}
