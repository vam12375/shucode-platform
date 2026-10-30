import assert from "node:assert/strict";
import test from "node:test";
import * as modelConfig from "../src/model-config.ts";

const config = {
  properties: {
    contextWindow: 270000,
    inputFormat: {
      supportsText: true,
      supportsImage: true,
      supportsVideo: false,
      supportsAudio: false,
      supportsPdf: true,
    },
    outputFormat: { supportsText: true },
    supportsToolCall: true,
    supportsJsonSchemaOutput: true,
    supportsNativeWebSearch: false,
    supportsMidConversationSystem: false,
    requiresMfjsToolSchema: false,
  },
  optionSpecs: {
    maxOutputTokens: { max: 200000 },
    reasoningLevel: {
      values: ["none", "low", "high", "max"],
      map: '{"reasoning_effort": reasoningLevel}',
    },
  },
};

test("manual config round-trips reasoning levels and map", () => {
  const fields = modelConfig.fieldsFromConfig(config);
  assert.deepEqual(fields.reasoningLevels, ["none", "low", "high", "max"]);
  assert.equal(fields.reasoningMap, '{"reasoning_effort": reasoningLevel}');
  assert.equal(fields.reasoningEnabled, true);
  assert.deepEqual(modelConfig.configFromFields(fields), config);
});

test("disabling reasoning omits the option instead of clearing inherited rules", () => {
  const fields = modelConfig.fieldsFromConfig(config);
  const saved = modelConfig.configFromFields({
    ...fields,
    reasoningEnabled: false,
    reasoningLevels: [],
    reasoningMap: "",
  });
  assert.equal("reasoningLevel" in saved.optionSpecs, false);
  assert.equal(modelConfig.reasoningConfigError({ ...fields, reasoningEnabled: false }), "");
});

test("normalization rejects empty levels and empty map", () => {
  const fields = modelConfig.fieldsFromConfig(config);
  assert.match(modelConfig.reasoningConfigError({ ...fields, reasoningLevels: [] }), /至少选择一个档位/);
  assert.match(
    modelConfig.reasoningConfigError({ ...fields, reasoningLevels: ["low", " "], reasoningMap: "" }),
    /映射表达式不能为空/,
  );
});

test("preset order stays low-to-high and unknown levels are preserved", () => {
  assert.deepEqual(
    modelConfig.orderReasoningLevels(["max", "low", "custom", "high", "low"]),
    ["low", "high", "max", "custom"],
  );
  assert.deepEqual(
    modelConfig.toggleReasoningLevel(["low", "high"], "medium", true),
    ["low", "medium", "high"],
  );
  assert.deepEqual(modelConfig.toggleReasoningLevel(["low", "high"], "low", false), ["high"]);
});

test("summary uses the configured order and translates known levels only", () => {
  assert.equal(modelConfig.reasoningSummaryText(config), "无 / 低 / 高 / 最高");
  assert.equal(
    modelConfig.reasoningSummaryText({
      ...config,
      optionSpecs: { ...config.optionSpecs, reasoningLevel: { values: ["turbo"], map: "{}" } },
    }),
    "turbo",
  );
  assert.equal(modelConfig.reasoningSummaryText({ ...config, optionSpecs: { maxOutputTokens: { max: 1 } } }), "");
});
