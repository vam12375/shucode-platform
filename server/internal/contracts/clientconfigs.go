package contracts

// client-configs.ts 镜像:buildClientConfigsEnvelope 构造(字段顺序与 TS 一致:
// feedbackUrl、builtin_provider_config_json、forceUpdate、pluginStoreOrder)。
// help / pluginStoreOrder 是 KV 里已反序列化的任意 JSON 值,原样透传。

type BuildClientConfigsEnvelopeInput struct {
	Help                      any // 非 nil 即透传(HelpConfig 语义在 KV 写入端保证)
	BuiltinReleaseURL         string
	ForceUpdateMinimalVersion string
	PluginStoreOrder          any // 非 nil 即透传
}

func BuildClientConfigsEnvelope(input BuildClientConfigsEnvelopeInput) map[string]any {
	configs := make(map[string]any, 4)
	if input.Help != nil {
		configs["feedbackUrl"] = input.Help
	}
	if input.BuiltinReleaseURL != "" {
		configs["builtin_provider_config_json"] = input.BuiltinReleaseURL
	}
	if input.ForceUpdateMinimalVersion != "" {
		configs["forceUpdate"] = map[string]any{"minimalVersion": input.ForceUpdateMinimalVersion}
	}
	if input.PluginStoreOrder != nil {
		configs["pluginStoreOrder"] = input.PluginStoreOrder
	}
	return map[string]any{
		"code": 0,
		"data": map[string]any{"configs": configs},
	}
}
