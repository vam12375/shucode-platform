package apiapp

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// user-config-builder.ts 的镜像:构造与客户端 Provider Provisioning 信封同构的载荷。
// 客户端按同一 schema 校验后写入 personal 层;出站前必须保持字段集合与语义完全一致。

const (
	platformProviderID   = "platform:gateway"
	platformProviderName = "Mendao Platform"
)

type gatewayAPI struct {
	Type    string `json:"type"`
	BaseURL string `json:"baseUrl"`
}

type userConfigModel struct {
	ModelID                string
	InputPricePerMtok      string
	OutputPricePerMtok     string
	CacheReadPricePerMtok  string
	CacheWritePricePerMtok string
}

type BuildUserConfigInput struct {
	UserID         string
	TeamID         string
	DeviceTokenID  string
	GatewayBaseURL string
	GatewayAPI     gatewayAPI
	Models         []userConfigModel
	DefaultModelID string
	ProviderName   string
}

// buildUserConfigEnvelope 输出可直接 JSON 化的 map。
func buildUserConfigEnvelope(input BuildUserConfigInput) map[string]any {
	modelIDs := make([]string, 0, len(input.Models))
	for _, model := range input.Models {
		modelIDs = append(modelIDs, model.ModelID)
	}
	providerName := input.ProviderName
	if providerName == "" {
		providerName = platformProviderName
	}
	providerRule := map[string]any{
		"providerId":   platformProviderID,
		"providerName": providerName,
		"enabled":      true,
		"config": map[string]any{
			"group": "standard-personal",
			// apiKey 由 withPlatformAccessToken 填入(设备 token);客户端 Registry
			// 完整校验要求 access 完整,缺 apiKey 时整个 Provider 被丢弃。
			"access": map[string]any{"type": "api-key"},
			"api": map[string]any{
				"type":    input.GatewayAPI.Type,
				"baseUrl": input.GatewayAPI.BaseURL,
			},
			"personalModelIds": modelIDs,
			"modelOrder":       modelIDs,
			"visibility":       "visible",
		},
	}
	providerModelRules := make([]any, 0, len(input.Models))
	for _, model := range input.Models {
		providerModelRules = append(providerModelRules, map[string]any{
			"providerId": platformProviderID,
			"modelId":    model.ModelID,
			"config":     map[string]any{"enabled": true},
		})
	}
	personalConfig := map[string]any{
		"providerConfigRules": map[string]any{"providerRules": []any{providerRule}},
		"modelConfigRules": map[string]any{
			"providerModelRules":       providerModelRules,
			"manualProviderModelRules": []any{},
		},
		"providerOrder": []any{platformProviderID},
	}
	if input.DefaultModelID != "" {
		personalConfig["defaultModelSelection"] = map[string]any{
			"providerId": platformProviderID,
			"modelId":    input.DefaultModelID,
		}
	}
	return map[string]any{
		"schemaVersion":  1,
		"syncId":         computeUserConfigSyncID(input),
		"personalConfig": personalConfig,
		"accountSettings": map[string]any{
			"providerFamilyDomain":               nil,
			"providerFamilyConnectionSelections": map[string]any{},
		},
		"credentials": []any{},
	}
}

// withPlatformAccessToken:把设备 token 写入网关 Provider 的 config.access.apiKey
// (客户端 Registry 只认这里;credentials 通道普通 personal provider 不消费)。
func withPlatformAccessToken(envelope map[string]any, deviceToken string) map[string]any {
	personal, _ := envelope["personalConfig"].(map[string]any)
	providerRulesHolder, _ := personal["providerConfigRules"].(map[string]any)
	rules, _ := providerRulesHolder["providerRules"].([]any)
	updated := make([]any, 0, len(rules))
	for _, rule := range rules {
		typed, ok := rule.(map[string]any)
		if !ok || typed["providerId"] != platformProviderID {
			updated = append(updated, rule)
			continue
		}
		config, ok := typed["config"].(map[string]any)
		if !ok {
			updated = append(updated, rule)
			continue
		}
		access, ok := config["access"].(map[string]any)
		if !ok {
			updated = append(updated, rule)
			continue
		}
		newAccess := make(map[string]any, len(access)+1)
		for key, value := range access {
			newAccess[key] = value
		}
		newAccess["apiKey"] = deviceToken
		newConfig := make(map[string]any, len(config))
		for key, value := range config {
			newConfig[key] = value
		}
		newConfig["access"] = newAccess
		newRule := make(map[string]any, len(typed))
		for key, value := range typed {
			newRule[key] = value
		}
		newRule["config"] = newConfig
		updated = append(updated, newRule)
	}
	providerRulesHolder["providerRules"] = updated
	return envelope
}

// computeUserConfigSyncID:内容寻址的 syncId(同设备+同配置 → 同值,客户端据此跳过重复应用)。
// 稳定序列化口径与 TS 一致:字段固定顺序、models 按 id 排序(大小写无关近似 localeCompare)。
func computeUserConfigSyncID(input BuildUserConfigInput) string {
	type modelStable struct {
		ID  string `json:"id"`
		In  string `json:"in"`
		Out string `json:"out"`
		CR  string `json:"cr"`
		CW  string `json:"cw"`
	}
	models := make([]modelStable, 0, len(input.Models))
	for _, model := range input.Models {
		models = append(models, modelStable{
			ID: model.ModelID, In: model.InputPricePerMtok, Out: model.OutputPricePerMtok,
			CR: model.CacheReadPricePerMtok, CW: model.CacheWritePricePerMtok,
		})
	}
	sort.SliceStable(models, func(i, j int) bool {
		li, lj := strings.ToLower(models[i].ID), strings.ToLower(models[j].ID)
		if li != lj {
			return li < lj
		}
		return models[i].ID < models[j].ID
	})
	stable := struct {
		UserID         string        `json:"userId"`
		TeamID         *string       `json:"teamId"`
		DeviceTokenID  string        `json:"deviceTokenId"`
		GatewayAPI     gatewayAPI    `json:"gatewayApi"`
		DefaultModelID *string       `json:"defaultModelId"`
		Models         []modelStable `json:"models"`
	}{
		UserID:        input.UserID,
		DeviceTokenID: input.DeviceTokenID,
		GatewayAPI:    input.GatewayAPI,
		Models:        models,
	}
	if input.TeamID != "" {
		teamID := input.TeamID
		stable.TeamID = &teamID
	}
	if input.DefaultModelID != "" {
		defaultID := input.DefaultModelID
		stable.DefaultModelID = &defaultID
	}
	raw, err := jsonMarshalNoHTML(stable)
	if err != nil {
		return "ucfg_invalid"
	}
	sum := sha256.Sum256(raw)
	return "ucfg_" + hex.EncodeToString(sum[:])[:40]
}
