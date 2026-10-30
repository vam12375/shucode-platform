import { useEffect, useRef, useState } from "react";
import { api } from "../api.js";
import {
  REASONING_LEVEL_PRESETS,
  configFromFields,
  fieldsFromConfig,
  reasoningConfigError,
  reasoningLevelLabel,
  reasoningSummaryText,
  toggleReasoningLevel,
  type ConfigFields,
  type ModelCapabilityAnalysis,
  type ModelConfigMode,
  type ModelConfigTarget,
} from "../model-config.js";
import { toastErr, toastOk } from "./Feedback.js";
import { Notice } from "./StatusChip.js";

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "操作失败";
}

function formatTokenCount(value: number): string {
  if (value >= 1_000_000) {
    return `${(value / 1_000_000).toFixed(value % 1_000_000 === 0 ? 0 : 1)}M`;
  }
  if (value >= 1000) return `${Math.round(value / 1000)}k`;
  return String(value);
}

type BooleanFieldKey =
  | "supportsImage"
  | "supportsVideo"
  | "supportsAudio"
  | "supportsPdf"
  | "supportsToolCall"
  | "supportsJsonSchemaOutput"
  | "supportsNativeWebSearch"
  | "supportsMidConversationSystem";

const INPUT_OPTIONS: Array<{ key: BooleanFieldKey; label: string }> = [
  { key: "supportsImage", label: "图片" },
  { key: "supportsVideo", label: "视频" },
  { key: "supportsAudio", label: "音频" },
  { key: "supportsPdf", label: "PDF" },
];

const CAPABILITY_OPTIONS: Array<{ key: BooleanFieldKey; label: string }> = [
  { key: "supportsToolCall", label: "工具调用" },
  { key: "supportsJsonSchemaOutput", label: "结构化输出" },
  { key: "supportsNativeWebSearch", label: "原生联网搜索" },
  { key: "supportsMidConversationSystem", label: "对话中系统消息" },
];

export function ModelConfigDrawer({
  model,
  onClose,
  onSaved,
}: {
  model: ModelConfigTarget;
  onClose: () => void;
  onSaved: (model: ModelConfigTarget) => void;
}) {
  const initialMode: ModelConfigMode = model.configMode ?? "recommended";
  const [mode, setMode] = useState<ModelConfigMode>(initialMode);
  const [fields, setFields] = useState<ConfigFields>(() =>
    fieldsFromConfig(initialMode === "manual" ? model.config : null),
  );
  const [analysis, setAnalysis] = useState<ModelCapabilityAnalysis | null>(null);
  const [analysisError, setAnalysisError] = useState("");
  const [analyzing, setAnalyzing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const analyzed = useRef(false);

  const runAnalysis = async (apply: boolean): Promise<void> => {
    setAnalyzing(true);
    setAnalysisError("");
    try {
      const result = await api<ModelCapabilityAnalysis>("/models/analyze", {
        method: "POST",
        body: JSON.stringify({ modelId: model.modelId }),
      });
      setAnalysis(result);
      if (apply) setFields(fieldsFromConfig(result.config));
    } catch (analyzeError) {
      setAnalysisError(errorMessage(analyzeError));
    } finally {
      setAnalyzing(false);
    }
  };

  useEffect(() => {
    if (analyzed.current) return;
    analyzed.current = true;
    // 打开时即按官方 Release 分析；智能模式直接展示推荐值，手动模式供“填充”使用。
    void runAnalysis(initialMode === "recommended");
  }, []);

  useEffect(() => {
    const onKey = (event: KeyboardEvent): void => {
      if (event.key === "Escape" && !busy) onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [busy, onClose]);

  const switchMode = (next: ModelConfigMode): void => {
    setMode(next);
    setError("");
    if (next === "recommended") {
      if (analysis) setFields(fieldsFromConfig(analysis.config));
      else void runAnalysis(true);
    } else if (analysis) {
      setFields(fieldsFromConfig(analysis.config));
    }
  };

  const updateField = <K extends keyof ConfigFields>(
    key: K,
    value: ConfigFields[K],
  ): void => {
    setFields((previous) => ({ ...previous, [key]: value }));
    setError("");
  };

  // 首次开启时用官方档位补齐，避免只打开开关却下发空档位。
  const toggleReasoning = (enabled: boolean): void => {
    setFields((previous) => {
      if (!enabled) return { ...previous, reasoningEnabled: false };
      const official = analysis?.config.optionSpecs.reasoningLevel;
      return {
        ...previous,
        reasoningEnabled: true,
        reasoningLevels:
          previous.reasoningLevels.length > 0
            ? previous.reasoningLevels
            : (official?.values ?? []),
        reasoningMap: previous.reasoningMap || official?.map || "",
      };
    });
    setError("");
  };

  const save = async (): Promise<void> => {
    let body: Record<string, unknown> = { mode };
    if (mode === "manual") {
      const contextWindow = Number.parseInt(fields.contextWindow, 10);
      const maxOutputTokens = Number.parseInt(fields.maxOutputTokens, 10);
      if (!Number.isInteger(contextWindow) || contextWindow <= 0) {
        setError("上下文窗口必须是大于 0 的整数。");
        return;
      }
      if (!Number.isInteger(maxOutputTokens) || maxOutputTokens <= 0) {
        setError("最大输出 Token 必须是大于 0 的整数。");
        return;
      }
      const reasoningError = reasoningConfigError(fields);
      if (reasoningError) {
        setError(reasoningError);
        return;
      }
      body = { mode, config: configFromFields(fields) };
    }
    setBusy(true);
    setError("");
    try {
      const data = await api<{ model: ModelConfigTarget }>(
        `/models/${encodeURIComponent(model.modelId)}/config`,
        { method: "PUT", body: JSON.stringify(body) },
      );
      toastOk(
        mode === "recommended"
          ? "已恢复为官方推荐配置"
          : "模型能力配置已保存",
        "已保存",
      );
      onSaved({
        modelId: data.model.modelId,
        displayName: data.model.displayName,
        configMode: data.model.configMode,
        config: data.model.config,
      });
    } catch (saveError) {
      const message = errorMessage(saveError);
      setError(message);
      toastErr(message, "保存失败");
    } finally {
      setBusy(false);
    }
  };

  const readOnly = mode === "recommended";
  const analysisLabel = analysis
    ? analysis.complete
      ? `已匹配 ${analysis.matchedRules} 条官方规则${analysis.releaseRevision ? ` · Release r${analysis.releaseRevision}` : ""}`
      : "官方规则未给出完整配置，可切换手动补齐"
    : "";

  return (
    <>
      <div className="drawer-backdrop" onClick={onClose} aria-hidden="true" />
      <aside
        className="drawer"
        role="dialog"
        aria-modal="true"
        aria-labelledby="model-config-title"
      >
        <header className="drawer-h">
          <div className="drawer-title">
            <p className="readout">模型目录 / 能力配置</p>
            <h3 id="model-config-title">编辑模型配置</h3>
            <p className="readout">
              {model.displayName} · {model.modelId}
            </p>
          </div>
          <div className="drawer-actions">
            <button
              className="btn btn-ghost btn-sm"
              type="button"
              disabled={busy}
              onClick={onClose}
            >
              关闭
            </button>
          </div>
        </header>

        <div className="drawer-b">
          <section className="drawer-sec">
            <div className="config-switch-row">
              <div>
                <h4 className="drawer-sec-title">智能配置</h4>
                <p className="readout config-hint">
                  跟随当前生效 Release 的官方模型规则，规则更新后客户端自动同步；关闭后使用下方手动配置。
                </p>
              </div>
              <label className="switch" title="切换智能配置">
                <input
                  type="checkbox"
                  aria-label="智能配置"
                  checked={mode === "recommended"}
                  disabled={busy}
                  onChange={(event) =>
                    switchMode(event.target.checked ? "recommended" : "manual")
                  }
                />
                <i />
              </label>
            </div>
            <div className="form-row" style={{ marginTop: 10 }}>
              <button
                className="btn btn-ghost btn-sm"
                type="button"
                disabled={busy || analyzing}
                onClick={() => void runAnalysis(mode === "manual")}
              >
                {analyzing
                  ? "分析中…"
                  : mode === "recommended"
                    ? "重新分析官方规则"
                    : "按官方推荐填充"}
              </button>
              {analysisLabel ? (
                <span className="readout">{analysisLabel}</span>
              ) : null}
            </div>
            {analysisError ? (
              <Notice tone="stop" k="分析失败">
                {analysisError}
              </Notice>
            ) : null}
          </section>

          <section className="drawer-sec">
            <h4 className="drawer-sec-title">基础参数</h4>
            <div className="form-stack">
              <label className="field">
                <span className="field-label">上下文窗口（token）</span>
                <input
                  className="input num"
                  inputMode="numeric"
                  readOnly={readOnly}
                  value={fields.contextWindow}
                  placeholder="如 200000"
                  onChange={(event) =>
                    updateField("contextWindow", event.target.value)
                  }
                />
                {fields.contextWindow ? (
                  <span className="readout">
                    ≈ {formatTokenCount(Number(fields.contextWindow) || 0)} tokens
                  </span>
                ) : null}
              </label>
              <label className="field">
                <span className="field-label">最大输出 Token</span>
                <input
                  className="input num"
                  inputMode="numeric"
                  readOnly={readOnly}
                  value={fields.maxOutputTokens}
                  placeholder="如 32000"
                  onChange={(event) =>
                    updateField("maxOutputTokens", event.target.value)
                  }
                />
                {fields.maxOutputTokens ? (
                  <span className="readout">
                    ≈ {formatTokenCount(Number(fields.maxOutputTokens) || 0)} tokens
                  </span>
                ) : null}
              </label>
            </div>
          </section>

          <section className="drawer-sec">
            <h4 className="drawer-sec-title">输入类型</h4>
            <p className="readout config-hint">文本输入为必选能力。</p>
            <div className="choice-row">
              <button className="choice-chip selected" type="button" disabled>
                文本（必选）
              </button>
              {INPUT_OPTIONS.map((option) => (
                <button
                  key={option.key}
                  className={
                    "choice-chip" +
                    (fields[option.key] ? " selected" : "")
                  }
                  type="button"
                  aria-pressed={Boolean(fields[option.key])}
                  disabled={readOnly || busy}
                  onClick={() =>
                    updateField(option.key, !fields[option.key])
                  }
                >
                  {option.label}
                </button>
              ))}
            </div>
          </section>

          <section className="drawer-sec">
            <h4 className="drawer-sec-title">模型能力</h4>
            <div className="choice-row">
              {CAPABILITY_OPTIONS.map((option) => (
                <button
                  key={option.key}
                  className={
                    "choice-chip" +
                    (fields[option.key] ? " selected" : "")
                  }
                  type="button"
                  aria-pressed={Boolean(fields[option.key])}
                  disabled={readOnly || busy}
                  onClick={() =>
                    updateField(option.key, !fields[option.key])
                  }
                >
                  {option.label}
                </button>
              ))}
            </div>
          </section>

          <section className="drawer-sec">
            <div className="config-switch-row">
              <div>
                <h4 className="drawer-sec-title">思考强度</h4>
                <p className="readout config-hint">
                  声明客户端可选的思考档位（按强度从低到高）及档位到上游参数的映射；关闭时不下发档位，客户端沿用官方规则。
                </p>
              </div>
              <label className="switch" title="下发思考档位">
                <input
                  type="checkbox"
                  aria-label="下发思考档位"
                  checked={fields.reasoningEnabled}
                  disabled={readOnly || busy}
                  onChange={(event) => toggleReasoning(event.target.checked)}
                />
                <i />
              </label>
            </div>
            {fields.reasoningEnabled ? (
              <div className="form-stack" style={{ marginTop: 10 }}>
                <div className="field">
                  <span className="field-label">档位（从低到高）</span>
                  <div className="choice-row">
                    {[
                      ...REASONING_LEVEL_PRESETS,
                      ...fields.reasoningLevels
                        .filter(
                          (value) =>
                            !REASONING_LEVEL_PRESETS.some(
                              (preset) => preset.value === value,
                            ),
                        )
                        .map((value) => ({ value, label: value })),
                    ].map((option) => {
                      const selected = fields.reasoningLevels.includes(option.value);
                      return (
                        <button
                          key={option.value}
                          className={"choice-chip" + (selected ? " selected" : "")}
                          type="button"
                          aria-pressed={selected}
                          disabled={readOnly || busy}
                          onClick={() =>
                            updateField(
                              "reasoningLevels",
                              toggleReasoningLevel(
                                fields.reasoningLevels,
                                option.value,
                                !selected,
                              ),
                            )
                          }
                        >
                          {option.label}
                          {option.label === option.value ? "" : ` · ${option.value}`}
                        </button>
                      );
                    })}
                  </div>
                  <span className="readout">
                    {fields.reasoningLevels.length > 0
                      ? `下发顺序：${fields.reasoningLevels.map(reasoningLevelLabel).join(" / ")}`
                      : "至少选择一个档位。"}
                  </span>
                </div>
                <label className="field">
                  <span className="field-label">档位映射（CEL 表达式）</span>
                  <textarea
                    className="input"
                    style={{ minHeight: 96 }}
                    aria-label="思考档位映射"
                    readOnly={readOnly}
                    value={fields.reasoningMap}
                    placeholder={'如 {"reasoning_effort": reasoningLevel}'}
                    onChange={(event) =>
                      updateField("reasoningMap", event.target.value)
                    }
                  />
                  <span className="readout">
                    变量为 reasoningLevel，必须返回 JSON 对象；官方规则通常给出上游字段映射。
                  </span>
                </label>
              </div>
            ) : (
              <p className="readout config-hint" style={{ marginTop: 10 }}>
                {reasoningSummaryText(analysis?.config)
                  ? `官方档位：${reasoningSummaryText(analysis?.config)}`
                  : "官方规则未声明档位；开启后需补齐档位与映射。"}
              </p>
            )}
          </section>

          {readOnly ? (
            <Notice>
              当前为智能配置，数组来自官方规则匹配结果。需要微调时关闭上方开关，再修改并保存。
            </Notice>
          ) : null}
          {error ? (
            <Notice tone="stop" k="无法保存">
              {error}
            </Notice>
          ) : null}
        </div>

        <footer className="drawer-footer">
          <button
            className="btn btn-ghost"
            type="button"
            disabled={busy}
            onClick={onClose}
          >
            取消
          </button>
          <button className="btn" type="button" disabled={busy} onClick={() => void save()}>
            {busy ? "保存中…" : "保存配置"}
          </button>
        </footer>
      </aside>
    </>
  );
}
