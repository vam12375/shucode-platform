package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

type analysisProviderConfig struct {
	ID               string
	BaseURL          string
	Protocol         store.UpstreamProtocol
	APIKeyCiphertext string
	AnthropicVersion string
	TimeoutMs        int64
	Enabled          bool
}

type analysisRouteConfig struct {
	ID            string
	ProviderID    string
	UpstreamModel string
	Priority      int64
	Weight        int64
	Enabled       bool
}

// 按已选模型的真实配置绑定授权，避免把供应商名称当作分析能力或防改配边界。
func AnalysisModelConfigDigest(ctx context.Context, db store.PlatformStore, modelID string) (string, error) {
	model, err := db.GetModel(ctx, modelID)
	if err != nil {
		return "", err
	}
	if model == nil || !model.Enabled {
		return "", ErrAnalysisScopeInvalid
	}
	routes, err := db.ListUpstreamRoutes(ctx, modelID, true)
	if err != nil {
		return "", err
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].ID < routes[j].ID })
	// 健康探测、失败计数和展示名称不是配置，不能让正常探测使进行中的授权失效。
	providers := map[string]*analysisProviderConfig{}
	routeConfigs := make([]analysisRouteConfig, 0, len(routes))
	ids := []string{model.UpstreamProviderID}
	for _, route := range routes {
		ids = append(ids, route.ProviderID)
		routeConfigs = append(routeConfigs, analysisRouteConfig{
			route.ID, route.ProviderID, route.UpstreamModel, route.Priority, route.Weight, route.Enabled,
		})
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		provider, err := db.GetUpstreamProvider(ctx, id)
		if err != nil {
			return "", err
		}
		if provider == nil {
			providers[id] = nil
		} else {
			providers[id] = &analysisProviderConfig{
				provider.ID, provider.BaseURL, provider.Protocol, provider.APIKeyCiphertext,
				provider.AnthropicVersion, provider.TimeoutMs, provider.Enabled,
			}
		}
	}
	raw, err := json.Marshal(struct {
		ModelID            string
		UpstreamProviderID string
		UpstreamModel      string
		ConfigMode         string
		ModelConfig        json.RawMessage
		Routes             []analysisRouteConfig
		Providers          map[string]*analysisProviderConfig
	}{model.ModelID, model.UpstreamProviderID, model.UpstreamModel, model.ConfigMode, model.ModelConfig, routeConfigs, providers})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
