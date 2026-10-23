package apiapp

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func registerMediaSourceRoutes(router *gin.Engine, deps *Deps) {
	router.POST("/api/v1/media/reference-analyses", func(c *gin.Context) {
		auth, membership, ok := mediaIdentity(c, deps)
		if !ok {
			return
		}
		var body struct {
			ID      string `json:"id"`
			AssetID string `json:"assetId"`
		}
		if !mediaBody(c, &body) {
			return
		}
		p, err := deps.Media.CreateReferenceAnalysis(c.Request.Context(), membership.Team.ID, auth.User.ID, auth.Device.ID, body.ID, body.AssetID)
		if err != nil {
			mediaError(c, deps, err)
			return
		}
		c.JSON(200, okData(p.View()))
	})
	router.GET("/api/v1/media/analysis-model", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		auth, membership, ok := mediaIdentity(c, deps)
		if !ok {
			return
		}
		rawVision := c.Query("vision")
		if rawVision != "0" && rawVision != "1" {
			c.JSON(400, fail(400, "analysis_vision_required"))
			return
		}
		ctx := c.Request.Context()
		policy, err := deps.Store.GetTeamPolicy(ctx, membership.Team.ID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		models, err := deps.Store.ListModels(ctx, false)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		for _, model := range models {
			if !model.Enabled || !store.TeamModelAllowed(policy, model.ModelID) {
				continue
			}
			allowed, err := analysisModelAvailable(ctx, deps, model, rawVision == "1")
			if err != nil {
				internalError(c, deps, err)
				return
			}
			if !allowed {
				continue
			}
			envelope := buildUserConfigEnvelope(BuildUserConfigInput{UserID: auth.User.ID, TeamID: membership.Team.ID, DeviceTokenID: auth.Device.ID, GatewayBaseURL: deps.Env.GatewayBaseURL, GatewayAPI: gatewayAPI{Type: deps.Env.GatewayAPIType, BaseURL: deps.Env.GatewayAPIBaseURL}, Models: []userConfigModel{{ModelID: model.ModelID, DisplayName: model.DisplayName, ConfigMode: model.ConfigMode, Config: canonicalModelConfig(model.ModelConfig)}}, DefaultModelID: model.ModelID, ProviderName: "Mendao Video Analysis"})
			setSourceAnalysisReasoning(envelope)
			c.JSON(200, okData(gin.H{"envelope": withPlatformAccessToken(envelope, auth.PlaintextToken)}))
			return
		}
		c.JSON(503, fail(503, "platform_analysis_model_unavailable"))
	})
}

func analysisModelAvailable(ctx context.Context, deps *Deps, model store.PlatformModelRecord, vision bool) (bool, error) {
	config, err := effectiveAnalysisModelCapabilities(ctx, deps, model)
	if err != nil {
		return false, err
	}
	if config == nil || config.Properties.ContextWindow <= 0 || config.OptionSpecs.MaxOutputTokens.Max <= 0 || !config.Properties.InputFormat.SupportsText || !config.Properties.OutputFormat.SupportsText || (vision && !config.Properties.InputFormat.SupportsImage) {
		return false, nil
	}
	// 两阶段分析均固定 high；能力缺失时拒绝，不能让改编阶段继承历史 max。
	reasoning := config.OptionSpecs.ReasoningLevel
	if reasoning == nil || strings.TrimSpace(reasoning.Map) == "" {
		return false, nil
	}
	high := false
	for _, value := range reasoning.Values {
		if value == "high" {
			high = true
			break
		}
	}
	if !high {
		return false, nil
	}
	routes, err := deps.Store.ListUpstreamRoutes(ctx, model.ModelID, true)
	if err != nil {
		return false, err
	}
	available := false
	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		provider, err := deps.Store.GetUpstreamProvider(ctx, route.ProviderID)
		if err != nil {
			return false, err
		}
		if !analysisProviderAvailable(deps, provider) {
			continue
		}
		upstream := route.UpstreamModel
		if strings.TrimSpace(upstream) == "" {
			upstream = model.UpstreamModel
		}
		if strings.TrimSpace(upstream) == "" {
			upstream = model.ModelID
		}
		// 混合路由池仍可能被调度到 Pro，不能仅凭其中一个 Flash 路由放行。
		if !media.IsDeepSeekAnalysisModel(upstream) {
			return false, nil
		}
		available = true
	}
	if available {
		return true, nil
	}
	// 已声明的路由池不可用时不回退其它供应商；沿用网关的唯一调度配置。
	if len(routes) > 0 {
		return false, nil
	}
	upstream := model.UpstreamModel
	if strings.TrimSpace(upstream) == "" {
		upstream = model.ModelID
	}
	if !media.IsDeepSeekAnalysisModel(upstream) {
		return false, nil
	}
	if model.UpstreamProviderID != "" {
		provider, err := deps.Store.GetUpstreamProvider(ctx, model.UpstreamProviderID)
		if err != nil {
			return false, err
		}
		return analysisProviderAvailable(deps, provider), nil
	}
	return false, nil
}

func setSourceAnalysisReasoning(envelope map[string]any) {
	selection := envelope["personalConfig"].(map[string]any)["defaultModelSelection"].(map[string]any)
	selection["options"] = map[string]any{"reasoningLevel": "high"}
}

func analysisProviderAvailable(deps *Deps, provider *store.UpstreamProviderRecord) bool {
	if provider == nil || !provider.Enabled || strings.TrimSpace(provider.BaseURL) == "" || provider.APIKeyCiphertext == "" || deps.SecretBox == nil {
		return false
	}
	if string(provider.Protocol) != deps.Env.GatewayAPIType {
		return false
	}
	key, err := deps.SecretBox.Decrypt(provider.APIKeyCiphertext)
	return err == nil && strings.TrimSpace(key) != ""
}
