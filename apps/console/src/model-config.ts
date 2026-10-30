/** 模型能力配置与客户端 packages/shared/src/model-config.ts 同构，控制台只维护可下发叶子。 */
export type ModelConfigMode = "recommended" | "manual";

export interface ModelInputFormat {
  supportsText: boolean;
  supportsImage: boolean;
  supportsVideo: boolean;
  supportsAudio: boolean;
  supportsPdf: boolean;
}

export interface ModelReasoningLevel {
  /** 按语义强度从低到高排列；客户端把首项当作最低档、末项当作最高档。 */
  values: string[];
  /** CEL 映射表达式，变量为 reasoningLevel，结果必须返回 JSON 对象。 */
  map: string;
}

export interface ModelCapabilityConfig {
  properties: {
    contextWindow: number;
    inputFormat: ModelInputFormat;
    outputFormat: { supportsText: boolean };
    supportsToolCall: boolean;
    supportsJsonSchemaOutput: boolean;
    supportsNativeWebSearch: boolean;
    supportsMidConversationSystem: boolean;
    requiresMfjsToolSchema: boolean;
  };
  optionSpecs: {
    maxOutputTokens: { max: number };
    reasoningLevel?: ModelReasoningLevel | null;
  };
}

export interface ModelConfigTarget {
  modelId: string;
  displayName: string;
  configMode?: ModelConfigMode;
  config?: ModelCapabilityConfig | null;
}

export interface ModelCapabilityAnalysis {
  modelId: string;
  config: ModelCapabilityConfig;
  matchedRules: number;
  complete: boolean;
  releaseRevision: number | null;
}

export interface ConfigFields {
  contextWindow: string;
  maxOutputTokens: string;
  supportsImage: boolean;
  supportsVideo: boolean;
  supportsAudio: boolean;
  supportsPdf: boolean;
  supportsToolCall: boolean;
  supportsJsonSchemaOutput: boolean;
  supportsNativeWebSearch: boolean;
  supportsMidConversationSystem: boolean;
  reasoningEnabled: boolean;
  reasoningLevels: string[];
  reasoningMap: string;
}

/**
 * 已知档位按强度从低到高排列。`enabled` 是开关型上游的“开启”别名，
 * 放在末尾避免与强度档位混排；未知档位仍可原样展示与保存。
 */
export const REASONING_LEVEL_PRESETS: ReadonlyArray<{
  value: string;
  label: string;
}> = [
  { value: "disabled", label: "关闭" },
  { value: "none", label: "无" },
  { value: "minimal", label: "极低" },
  { value: "low", label: "低" },
  { value: "medium", label: "中" },
  { value: "high", label: "高" },
  { value: "xhigh", label: "极高" },
  { value: "max", label: "最高" },
  { value: "enabled", label: "开启" },
];

const REASONING_LEVEL_LABELS = new Map(
  REASONING_LEVEL_PRESETS.map((preset) => [preset.value, preset.label]),
);

export function reasoningLevelLabel(value: string): string {
  return REASONING_LEVEL_LABELS.get(value) ?? value;
}

/** 预设档位在前并保持低→高顺序，自定义档位按首次出现顺序追加。 */
export function orderReasoningLevels(values: readonly string[]): string[] {
  const unique = [...new Set(values.map((value) => value.trim()).filter(Boolean))];
  const seen = new Set(unique);
  const known = REASONING_LEVEL_PRESETS.map((preset) => preset.value).filter((value) =>
    seen.has(value),
  );
  const custom = unique.filter((value) => !REASONING_LEVEL_LABELS.has(value));
  return [...known, ...custom];
}

export function toggleReasoningLevel(
  values: readonly string[],
  value: string,
  selected: boolean,
): string[] {
  return orderReasoningLevels(
    selected ? [...values, value] : values.filter((item) => item !== value),
  );
}

export function fieldsFromConfig(config?: ModelCapabilityConfig | null): ConfigFields {
  const reasoning = config?.optionSpecs.reasoningLevel;
  return {
    contextWindow:
      config && config.properties.contextWindow > 0
        ? String(config.properties.contextWindow)
        : "",
    maxOutputTokens:
      config && config.optionSpecs.maxOutputTokens.max > 0
        ? String(config.optionSpecs.maxOutputTokens.max)
        : "",
    supportsImage: config?.properties.inputFormat.supportsImage ?? false,
    supportsVideo: config?.properties.inputFormat.supportsVideo ?? false,
    supportsAudio: config?.properties.inputFormat.supportsAudio ?? false,
    supportsPdf: config?.properties.inputFormat.supportsPdf ?? false,
    supportsToolCall: config?.properties.supportsToolCall ?? false,
    supportsJsonSchemaOutput: config?.properties.supportsJsonSchemaOutput ?? false,
    supportsNativeWebSearch: config?.properties.supportsNativeWebSearch ?? false,
    supportsMidConversationSystem:
      config?.properties.supportsMidConversationSystem ?? false,
    reasoningEnabled: Boolean(reasoning),
    reasoningLevels: reasoning ? [...reasoning.values] : [],
    reasoningMap: reasoning?.map ?? "",
  };
}

/** 手动配置的思考档位；未开启时不下发，客户端继续沿用官方规则。 */
export function configFromFields(fields: ConfigFields): ModelCapabilityConfig {
  const optionSpecs: ModelCapabilityConfig["optionSpecs"] = {
    maxOutputTokens: { max: Number.parseInt(fields.maxOutputTokens, 10) },
  };
  if (fields.reasoningEnabled) {
    optionSpecs.reasoningLevel = {
      values: fields.reasoningLevels.map((value) => value.trim()).filter(Boolean),
      map: fields.reasoningMap.trim(),
    };
  }
  return {
    properties: {
      contextWindow: Number.parseInt(fields.contextWindow, 10),
      inputFormat: {
        supportsText: true,
        supportsImage: fields.supportsImage,
        supportsVideo: fields.supportsVideo,
        supportsAudio: fields.supportsAudio,
        supportsPdf: fields.supportsPdf,
      },
      outputFormat: { supportsText: true },
      supportsToolCall: fields.supportsToolCall,
      supportsJsonSchemaOutput: fields.supportsJsonSchemaOutput,
      supportsNativeWebSearch: fields.supportsNativeWebSearch,
      supportsMidConversationSystem: fields.supportsMidConversationSystem,
      requiresMfjsToolSchema: false,
    },
    optionSpecs,
  };
}

export function reasoningConfigError(fields: ConfigFields): string {
  if (!fields.reasoningEnabled) return "";
  const values = fields.reasoningLevels.map((value) => value.trim()).filter(Boolean);
  if (values.length === 0) return "开启思考强度后至少选择一个档位。";
  if (new Set(values).size !== values.length) return "思考强度档位不能重复。";
  if (!fields.reasoningMap.trim()) return "思考强度映射表达式不能为空。";
  return "";
}

export function reasoningSummaryText(config?: ModelCapabilityConfig | null): string {
  const reasoning = config?.optionSpecs.reasoningLevel;
  if (!reasoning || reasoning.values.length === 0) return "";
  return reasoning.values.map(reasoningLevelLabel).join(" / ");
}
