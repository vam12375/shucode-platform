package apiapp

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/contracts"
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
	router.GET("/api/v1/media/analysis-models", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		_, membership, ok := mediaIdentity(c, deps)
		if !ok {
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
		result := make([]contracts.VideoAnalysisModel, 0, len(models))
		defaultID := ""
		for _, model := range models {
			if !model.Enabled || !store.TeamModelAllowed(policy, model.ModelID) {
				continue
			}
			reason, err := analysisModelUnavailableReason(ctx, deps, model, true)
			if err != nil {
				internalError(c, deps, err)
				return
			}
			name := strings.TrimSpace(model.DisplayName)
			if name == "" {
				name = model.ModelID
			}
			item := contracts.VideoAnalysisModel{ID: model.ModelID, Name: name, Available: reason == "", UnavailableReason: reason}
			if reason == "" && defaultID == "" {
				defaultID = model.ModelID
			}
			result = append(result, item)
		}
		data := contracts.VideoAnalysisModelCatalog{Models: result, DefaultModelID: defaultID}
		c.JSON(200, okData(data))
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
		selectedID := strings.TrimSpace(c.Query("modelId"))
		if len(selectedID) > 256 {
			c.JSON(400, fail(400, "analysis_model_required"))
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
			if !model.Enabled || !store.TeamModelAllowed(policy, model.ModelID) || (selectedID != "" && selectedID != model.ModelID) {
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
			protocol, available, err := gatewayProtocolForModel(ctx, deps, model)
			if err != nil {
				internalError(c, deps, err)
				return
			}
			if !available {
				continue
			}
			envelope := buildUserConfigEnvelope(BuildUserConfigInput{UserID: auth.User.ID, TeamID: membership.Team.ID, DeviceTokenID: auth.Device.ID, GatewayAPI: gatewayAPI{Type: deps.Env.GatewayAPIType, BaseURL: deps.Env.GatewayAPIBaseURL}, Models: []userConfigModel{{ModelID: model.ModelID, DisplayName: model.DisplayName, Protocol: protocol, ConfigMode: model.ConfigMode, Config: canonicalModelConfig(model.ModelConfig)}}, DefaultModelID: model.ModelID, ProviderName: "Mendao Video Analysis"})

			setSourceAnalysisReasoning(envelope)
			c.JSON(200, okData(gin.H{"envelope": withPlatformAccessToken(envelope, auth.PlaintextToken)}))
			return
		}
		c.JSON(503, fail(503, "platform_analysis_model_unavailable"))
	})
}

func analysisModelAvailable(ctx context.Context, deps *Deps, model store.PlatformModelRecord, vision bool) (bool, error) {
	reason, err := analysisModelUnavailableReason(ctx, deps, model, vision)
	return reason == "", err
}

func analysisModelUnavailableReason(ctx context.Context, deps *Deps, model store.PlatformModelRecord, vision bool) (string, error) {
	config, err := effectiveAnalysisModelCapabilities(ctx, deps, model)
	if err != nil {
		return "", err
	}
	if config == nil || config.Properties.ContextWindow <= 0 || config.OptionSpecs.MaxOutputTokens.Max <= 0 || !config.Properties.InputFormat.SupportsText || !config.Properties.OutputFormat.SupportsText {
		return "model_config_invalid", nil
	}
	if vision && !config.Properties.InputFormat.SupportsImage {
		return "vision_required", nil
	}
	reasoning := config.OptionSpecs.ReasoningLevel
	high := false
	if reasoning != nil && strings.TrimSpace(reasoning.Map) != "" {
		for _, value := range reasoning.Values {
			if value == "high" {
				high = true
				break
			}
		}
	}
	if !high {
		return "high_reasoning_required", nil
	}
	// 能力和真实连接决定资格，不能再按供应商品牌或上游型号名称筛选。
	routes, err := deps.Store.ListUpstreamRoutes(ctx, model.ModelID, true)
	if err != nil {
		return "", err
	}
	available := false
	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		provider, err := deps.Store.GetUpstreamProvider(ctx, route.ProviderID)
		if err != nil {
			return "", err
		}
		if analysisProviderAvailable(deps, provider) {
			available = true
		}
	}
	if len(routes) == 0 && model.UpstreamProviderID != "" {
		provider, err := deps.Store.GetUpstreamProvider(ctx, model.UpstreamProviderID)
		if err != nil {
			return "", err
		}
		available = analysisProviderAvailable(deps, provider)
	}
	if !available {
		return "upstream_unavailable", nil
	}
	_, protocolAvailable, err := gatewayProtocolForModel(ctx, deps, model)
	if err != nil {
		return "", err
	}
	if !protocolAvailable {
		return "upstream_unavailable", nil
	}
	return "", nil
}

func setSourceAnalysisReasoning(envelope map[string]any) {
	selection := envelope["personalConfig"].(map[string]any)["defaultModelSelection"].(map[string]any)
	selection["options"] = map[string]any{"reasoningLevel": "high"}
}

func analysisProviderAvailable(deps *Deps, provider *store.UpstreamProviderRecord) bool {
	if provider == nil || !provider.Enabled || strings.TrimSpace(provider.BaseURL) == "" || provider.APIKeyCiphertext == "" || deps.SecretBox == nil {
		return false
	}
	if provider.Protocol != store.ProtocolAnthropicMessages &&
		provider.Protocol != store.ProtocolOpenAIChatCompletions &&
		provider.Protocol != store.ProtocolOpenAIResponses {
		return false
	}
	key, err := deps.SecretBox.Decrypt(provider.APIKeyCiphertext)
	return err == nil && strings.TrimSpace(key) != ""
}
