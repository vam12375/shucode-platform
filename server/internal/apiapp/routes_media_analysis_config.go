package apiapp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func registerMediaAnalysisConfigRoutes(router *gin.Engine, deps *Deps) {
	router.GET("/api/v1/media/projects/:id/analysis-config", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		auth, membership, ok := mediaIdentity(c, deps)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		p, err := deps.Media.Repo.Get(ctx, membership.Team.ID, c.Param("id"))
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if p == nil || p.UserID != auth.User.ID || p.DeviceID != auth.Device.ID {
			c.JSON(403, fail(403, "video_analysis_scope_invalid"))
			return
		}
		modelID := strings.TrimSpace(c.Query("modelId"))
		if modelID == "" || len(modelID) > 256 {
			c.JSON(400, fail(400, "analysis_model_required"))
			return
		}
		policy, err := deps.Store.GetTeamPolicy(ctx, membership.Team.ID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		model, err := deps.Store.GetModel(ctx, modelID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if model == nil || !model.Enabled || !store.TeamModelAllowed(policy, modelID) {
			c.JSON(403, fail(403, "analysis_model_not_allowed"))
			return
		}
		ticket, err := media.IssueAnalysisScope(ctx, deps.Media.Repo, deps.SecretBox, p, modelID, deps.Now())
		if err != nil {
			status := 403
			if errors.Is(err, media.ErrAnalysisScopeUnavailable) {
				status = 503
			}
			c.JSON(status, fail(status, err.Error()))
			return
		}
		providerID := "platform:video-analysis:" + p.ID + ":" + p.Analysis.InputID
		envelope := buildUserConfigEnvelope(BuildUserConfigInput{
			UserID: auth.User.ID, TeamID: membership.Team.ID, DeviceTokenID: auth.Device.ID,
			GatewayBaseURL: deps.Env.GatewayBaseURL,
			GatewayAPI:     gatewayAPI{Type: deps.Env.GatewayAPIType, BaseURL: deps.Env.GatewayAPIBaseURL},
			Models:         []userConfigModel{{ModelID: model.ModelID, ConfigMode: model.ConfigMode, Config: canonicalModelConfig(model.ModelConfig)}},
			DefaultModelID: model.ModelID, ProviderName: "Mendao Video Analysis",
		})
		envelope = withPlatformAccessToken(envelope, auth.PlaintextToken)
		personal := envelope["personalConfig"].(map[string]any)
		holder := personal["providerConfigRules"].(map[string]any)
		for _, value := range holder["providerRules"].([]any) {
			rule := value.(map[string]any)
			rule["providerId"] = providerID
			config := rule["config"].(map[string]any)
			config["api"].(map[string]any)["headers"] = map[string]string{media.AnalysisScopeHeader: ticket}
		}
		models := personal["modelConfigRules"].(map[string]any)
		for _, value := range models["providerModelRules"].([]any) {
			value.(map[string]any)["providerId"] = providerID
		}
		personal["providerOrder"] = []any{providerID}
		personal["defaultModelSelection"].(map[string]any)["providerId"] = providerID
		// ticket 有效期随签发更新；内容身份也必须变化，避免客户端跳过新scope。
		digest := sha256.Sum256([]byte(providerID + ":" + ticket))
		envelope["syncId"] = "ucfg_" + hex.EncodeToString(digest[:])[:40]
		c.JSON(200, okData(gin.H{"providerId": providerID, "envelope": envelope}))
	})
}
