package apiapp

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// 客户端模型能力配置（与客户端 packages/shared/src/model-config.ts 同构）。
// recommended 模式不下发 override，由客户端当前内置 Release 的官方规则决定；
// manual 模式把这里的内容写进 providerModelRules[].config。
const (
	modelConfigRecommended = "recommended"
	modelConfigManual      = "manual"
)

type modelInputFormat struct {
	SupportsText  bool `json:"supportsText"`
	SupportsImage bool `json:"supportsImage"`
	SupportsVideo bool `json:"supportsVideo"`
	SupportsAudio bool `json:"supportsAudio"`
	SupportsPdf   bool `json:"supportsPdf"`
}

type modelOutputFormat struct {
	SupportsText bool `json:"supportsText"`
}

type modelConfigProperties struct {
	ContextWindow                 int64             `json:"contextWindow"`
	InputFormat                   modelInputFormat  `json:"inputFormat"`
	OutputFormat                  modelOutputFormat `json:"outputFormat"`
	SupportsToolCall              bool              `json:"supportsToolCall"`
	SupportsJsonSchemaOutput      bool              `json:"supportsJsonSchemaOutput"`
	SupportsNativeWebSearch       bool              `json:"supportsNativeWebSearch"`
	SupportsMidConversationSystem bool              `json:"supportsMidConversationSystem"`
	RequiresMfjsToolSchema        bool              `json:"requiresMfjsToolSchema"`
}

type modelMaxOutputTokens struct {
	Max int64 `json:"max"`
}

type modelOptionSpecs struct {
	MaxOutputTokens modelMaxOutputTokens `json:"maxOutputTokens"`
	ReasoningLevel  *modelReasoningLevel `json:"reasoningLevel,omitempty"`
}

type modelReasoningLevel struct {
	Values []string `json:"values"`
	Map    string   `json:"map"`
}

type modelCapabilityConfig struct {
	Properties  modelConfigProperties `json:"properties"`
	OptionSpecs modelOptionSpecs      `json:"optionSpecs"`
}

type modelCapabilityAnalysis struct {
	ModelID         string                `json:"modelId"`
	Config          modelCapabilityConfig `json:"config"`
	MatchedRules    int                   `json:"matchedRules"`
	Complete        bool                  `json:"complete"`
	ReleaseRevision *int64                `json:"releaseRevision"`
}

type releaseModelRule struct {
	ModelMatch   string         `json:"modelMatch"`
	APITypeMatch string         `json:"apiTypeMatch"`
	BaseURLMatch string         `json:"baseUrlMatch"`
	Config       map[string]any `json:"config"`
}

type releaseModelConfigRules struct {
	ModelRules        []releaseModelRule `json:"modelRules"`
	ModelAPIRules     []releaseModelRule `json:"modelApiRules"`
	ProviderSiteRules []releaseModelRule `json:"providerSiteRules"`
}

func emptyModelCapabilityConfig() modelCapabilityConfig {
	cfg := modelCapabilityConfig{}
	cfg.Properties.InputFormat.SupportsText = true
	cfg.Properties.OutputFormat.SupportsText = true
	return cfg
}

func decodeModelCapabilityConfig(raw []byte) *modelCapabilityConfig {
	if len(raw) == 0 {
		return nil
	}
	var cfg modelCapabilityConfig
	if json.Unmarshal(raw, &cfg) != nil {
		return nil
	}
	return &cfg
}

// canonicalModelConfig:PG 的 jsonb 文本输出会带空格，内存实现保留原始字节；
// 信封与 syncId 必须使用同一份紧凑 JSON，否则同配置会产生不同的同步指纹。
func canonicalModelConfig(raw []byte) []byte {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return encoded
}

func parseManualModelCapabilityConfig(raw any) (*modelCapabilityConfig, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var cfg modelCapabilityConfig
	if err := json.Unmarshal(encoded, &cfg); err != nil {
		return nil, err
	}
	if cfg.Properties.ContextWindow <= 0 || cfg.Properties.ContextWindow > 10_000_000 {
		return nil, errors.New("上下文窗口必须大于 0 且不超过 10,000,000")
	}
	if cfg.OptionSpecs.MaxOutputTokens.Max <= 0 || cfg.OptionSpecs.MaxOutputTokens.Max > 10_000_000 {
		return nil, errors.New("最大输出 Token 必须大于 0 且不超过 10,000,000")
	}
	// 文本输入/输出是客户端完整配置的必要叶子；其余能力保持管理员的选择。
	cfg.Properties.InputFormat.SupportsText = true
	cfg.Properties.OutputFormat.SupportsText = true
	return &cfg, nil
}

func analyzeModelCapabilities(ctx context.Context, deps *Deps, modelID string) (modelCapabilityAnalysis, error) {
	analysis := modelCapabilityAnalysis{
		ModelID: modelID,
		Config:  emptyModelCapabilityConfig(),
	}
	active, err := deps.Store.GetActiveRelease(ctx)
	if err != nil {
		return analysis, err
	}
	if active == nil || len(active.Content) == 0 {
		return analysis, nil
	}
	var content struct {
		Config struct {
			ModelConfigRules releaseModelConfigRules `json:"modelConfigRules"`
		} `json:"config"`
	}
	if err := json.Unmarshal(active.Content, &content); err != nil {
		return analysis, err
	}
	analysis.ReleaseRevision = &active.Revision
	rules := content.Config.ModelConfigRules
	merged := map[string]any{}
	apply := func(rule releaseModelRule) {
		merged = overlayModelConfigMap(merged, rule.Config)
		analysis.MatchedRules++
	}
	for _, rule := range rules.ModelRules {
		if capabilityPatternMatches(rule.ModelMatch, modelID, true) {
			apply(rule)
		}
	}
	for _, rule := range rules.ModelAPIRules {
		if !capabilityPatternMatches(rule.ModelMatch, modelID, true) {
			continue
		}
		if rule.APITypeMatch != "" &&
			!capabilityPatternMatches(rule.APITypeMatch, deps.Env.GatewayAPIType, false) {
			continue
		}
		apply(rule)
	}
	for _, rule := range rules.ProviderSiteRules {
		if !capabilityPatternMatches(rule.ModelMatch, modelID, true) {
			continue
		}
		if rule.BaseURLMatch != "" &&
			!capabilityPatternMatches(rule.BaseURLMatch, strings.TrimRight(deps.Env.GatewayAPIBaseURL, "/"), false) {
			continue
		}
		apply(rule)
	}
	if len(merged) > 0 {
		raw, err := json.Marshal(merged)
		if err != nil {
			return analysis, err
		}
		var cfg modelCapabilityConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return analysis, err
		}
		cfg.Properties.InputFormat.SupportsText = true
		cfg.Properties.OutputFormat.SupportsText = true
		analysis.Config = cfg
	}
	analysis.Complete = analysis.Config.Properties.ContextWindow > 0 &&
		analysis.Config.OptionSpecs.MaxOutputTokens.Max > 0
	return analysis, nil
}

// overlayModelConfigMap:与客户端 ConfigOverlay 一致的对象深合并（后者覆盖前者）。
func overlayModelConfigMap(base, next map[string]any) map[string]any {
	if base == nil {
		base = map[string]any{}
	}
	for key, value := range next {
		if value == nil {
			continue
		}
		nextObject, nextIsObject := value.(map[string]any)
		if !nextIsObject {
			base[key] = value
			continue
		}
		baseObject, baseIsObject := base[key].(map[string]any)
		if !baseIsObject {
			baseObject = map[string]any{}
		}
		base[key] = overlayModelConfigMap(baseObject, nextObject)
	}
	return base
}

func effectiveAnalysisModelCapabilities(ctx context.Context, deps *Deps, model store.PlatformModelRecord) (*modelCapabilityConfig, error) {
	analyzed, err := analyzeModelCapabilities(ctx, deps, model.ModelID)
	if err != nil {
		return nil, err
	}
	if model.ConfigMode != modelConfigManual {
		return &analyzed.Config, nil
	}
	if decodeModelCapabilityConfig(model.ModelConfig) == nil {
		return nil, nil
	}

	// manual 是客户端稀疏覆盖：缺省 reasoning 能力应继承 active Release，显式叶子仍由 manual 覆盖。
	baseRaw, err := json.Marshal(analyzed.Config)
	if err != nil {
		return nil, err
	}
	var base map[string]any
	if err := json.Unmarshal(baseRaw, &base); err != nil {
		return nil, err
	}
	var manual map[string]any
	if err := json.Unmarshal(model.ModelConfig, &manual); err != nil || manual == nil {
		return nil, nil
	}
	merged := overlayModelConfigMap(base, manual)
	mergedRaw, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	return decodeModelCapabilityConfig(mergedRaw), nil
}

func capabilityPatternMatches(pattern, value string, ignoreCase bool) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	prefix := "^(?:"
	if ignoreCase {
		prefix = "(?i)^(?:"
	}
	compiled, err := regexp.Compile(prefix + pattern + ")$")
	return err == nil && compiled.MatchString(value)
}

func registerModelCapabilityRoutes(group *gin.RouterGroup, deps *Deps) {
	group.POST("/models/analyze", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		modelID, ok := trimmedRequired(body["modelId"], 1, 200)
		if !ok {
			c.JSON(400, fail(400, "模型 ID 不合法"))
			return
		}
		analysis, err := analyzeModelCapabilities(c.Request.Context(), deps, modelID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.JSON(200, okData(analysis))
	})

	group.PUT("/models/:modelId/config", func(c *gin.Context) {
		admin, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		mode, ok := trimmedRequired(body["mode"], 1, 20)
		if !ok || (mode != modelConfigRecommended && mode != modelConfigManual) {
			c.JSON(400, fail(400, "配置模式必须是 recommended 或 manual"))
			return
		}
		var encoded []byte
		if mode == modelConfigManual {
			raw, exists := body["config"]
			if !exists || raw == nil {
				c.JSON(400, fail(400, "手动配置缺少 config"))
				return
			}
			cfg, err := parseManualModelCapabilityConfig(raw)
			if err != nil {
				c.JSON(400, fail(400, err.Error()))
				return
			}
			encoded, err = jsonMarshalNoHTML(cfg)
			if err != nil {
				internalError(c, deps, err)
				return
			}
		}
		updated, err := deps.Store.SetModelConfig(c.Request.Context(), c.Param("modelId"), mode, encoded)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if updated == nil {
			c.JSON(404, fail(404, "模型不存在"))
			return
		}
		deps.ConfigEvents.Emit(events.DomainModels)
		writeAudit(c.Request.Context(), deps, admin, "model.config.update", "model", updated.ModelID, map[string]any{"mode": mode})
		c.JSON(200, okData(gin.H{"model": toPublicModel(updated)}))
	})
}
