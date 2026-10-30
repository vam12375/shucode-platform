import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { Tags, Upload } from "lucide-react";
import { api, type ApiError } from "../api.js";
import { confirmDialog, toastErr, toastOk } from "../components/Feedback.js";
import { EditableCell } from "../components/EditableCell.js";
import { Glyph, PageTitle } from "../components/Icon.js";
import { Notice } from "../components/StatusChip.js";

/** 可点击编辑的单元格字段。 */
type EditableField =
  | "displayName"
  | "currency"
  | "inputCostPerMillion"
  | "outputCostPerMillion"
  | "cacheReadCostPerMillion"
  | "cacheCreationCostPerMillion"
  | "offPeakMultiplier";

/** 与 /console/api/model-pricing 条目对齐：每 1M token 成本价（币种见条目）。 */
interface PricingEntry {
  modelId: string;
  displayName: string;
  inputCostPerMillion: string;
  outputCostPerMillion: string;
  cacheReadCostPerMillion: string;
  cacheCreationCostPerMillion: string;
  offPeakMultiplier?: string;
  currency?: string;
}

/** GET / PUT 都携带内容版本；PUT 版本不符时后端返回 409（他人已修改）。 */
interface PricingDoc {
  entries: PricingEntry[];
  version: string;
}

interface PricingDraft {
  displayName: string;
  currency: string;
  inputCostPerMillion: string;
  outputCostPerMillion: string;
  cacheReadCostPerMillion: string;
  cacheCreationCostPerMillion: string;
  offPeakMultiplier: string;
}

const EMPTY_ADD = {
  modelId: "",
  displayName: "",
  currency: "CNY",
  inputCostPerMillion: "",
  outputCostPerMillion: "",
  cacheReadCostPerMillion: "",
  cacheCreationCostPerMillion: "",
  offPeakMultiplier: "1",
};

function toDraft(entry: PricingEntry): PricingDraft {
  return {
    displayName: entry.displayName,
    currency: entry.currency || "USD",
    inputCostPerMillion: entry.inputCostPerMillion,
    outputCostPerMillion: entry.outputCostPerMillion,
    cacheReadCostPerMillion: entry.cacheReadCostPerMillion,
    cacheCreationCostPerMillion: entry.cacheCreationCostPerMillion,
    offPeakMultiplier: entry.offPeakMultiplier || "1.000000",
  };
}

/** 空闲价 = 高峰价 × 倍率；倍率空/1 时显示 —。 */
function offPeakPrice(value: string, multiplier: string | undefined): string {
  const price = Number.parseFloat(value) || 0;
  const factor = Number.parseFloat(multiplier ?? "") || 1;
  if (factor === 1) return "—";
  return (price * factor).toLocaleString("en-US", { maximumFractionDigits: 6 });
}

export function PricingPage() {
  const [entries, setEntries] = useState<PricingEntry[]>([]);
  const [drafts, setDrafts] = useState<Record<string, PricingDraft>>({});
  const [add, setAdd] = useState({ ...EMPTY_ADD });
  const [pasteText, setPasteText] = useState("");
  const [pasteOpen, setPasteOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // 单元格点击编辑：同一时刻只编辑一个格子；Esc 还原，回车/失焦自动保存。
  const [editing, setEditing] = useState<{ modelId: string; field: EditableField } | null>(null);

  // 自动保存要读取最新值：ref 跟随 state 同步更新，避免 blur 时闭包仍持有旧草稿。
  const entriesRef = useRef<PricingEntry[]>([]);
  const draftsRef = useRef<Record<string, PricingDraft>>({});
  const versionRef = useRef("");
  const dirtyRef = useRef<Set<string>>(new Set());
  const editRevisionRef = useRef(0);
  const editingRef = useRef<{ modelId: string; field: EditableField } | null>(null);
  const autoSaveTimerRef = useRef<number | null>(null);
  const autoSavePendingRef = useRef(false);
  // 所有 PUT 串行执行：自动保存与手动保存不会拿着旧 expectedVersion 互相覆盖。
  const saveChainRef = useRef<Promise<boolean>>(Promise.resolve(true));

  const replaceEntries = useCallback(
    (nextEntries: PricingEntry[], nextVersion: string, resetDrafts: boolean): void => {
      entriesRef.current = nextEntries;
      versionRef.current = nextVersion;
      setEntries(nextEntries);
      if (!resetDrafts) return;
      const nextDrafts = Object.fromEntries(
        nextEntries.map((entry) => [entry.modelId, toDraft(entry)]),
      );
      draftsRef.current = nextDrafts;
      dirtyRef.current.clear();
      setDrafts(nextDrafts);
    },
    [],
  );

  const load = useCallback(async () => {
    try {
      const data = await api<PricingDoc>("/model-pricing");
      replaceEntries(data.entries, data.version, true);
      setError(null);
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载失败");
    }
  }, [replaceEntries]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    return () => {
      if (autoSaveTimerRef.current !== null) window.clearTimeout(autoSaveTimerRef.current);
    };
  }, []);

  // 全量替换式保存：前端整表 PUT + expectedVersion；版本冲突（他人已改）时不静默覆盖，
  // 提示后自动重载服务端最新值，当前未保存的编辑丢弃。
  const saveList = async (
    next: PricingEntry[],
    successMessage: string,
    silent = false,
  ): Promise<boolean> => {
    setBusy(true);
    const revisionAtSave = editRevisionRef.current;
    try {
      const result = await api<PricingDoc>("/model-pricing", {
        method: "PUT",
        body: JSON.stringify({ entries: next, expectedVersion: versionRef.current }),
      });
      entriesRef.current = result.entries;
      versionRef.current = result.version;
      setEntries(result.entries);
      // 保存期间没有新编辑时才用服务端回值覆盖草稿；否则保留本地草稿等下一轮保存。
      if (editRevisionRef.current === revisionAtSave) {
        const nextDrafts = Object.fromEntries(
          result.entries.map((entry) => [entry.modelId, toDraft(entry)]),
        );
        draftsRef.current = nextDrafts;
        dirtyRef.current.clear();
        setDrafts(nextDrafts);
      }
      if (!silent) toastOk(successMessage, "已保存");
      return true;
    } catch (saveError) {
      if ((saveError as ApiError).code === 409) {
        toastErr("定价库已被他人修改，已为你刷新到最新值", "保存未生效");
        await load();
      } else {
        toastErr(saveError instanceof Error ? saveError.message : "保存失败", "保存失败");
      }
      return false;
    } finally {
      setBusy(false);
    }
  };

  const buildDraftEntries = useCallback((): PricingEntry[] => {
    return entriesRef.current.map((entry) => {
      const draft = draftsRef.current[entry.modelId];
      return draft ? { modelId: entry.modelId, ...draft } : entry;
    });
  }, []);

  // 所有保存串行执行；buildNext 在真正轮到本次保存时才读取最新草稿。
  const enqueueSave = (
    buildNext: () => PricingEntry[],
    successMessage: string,
    silent = false,
  ): Promise<boolean> => {
    const run = (): Promise<boolean> => saveList(buildNext(), successMessage, silent);
    const task = saveChainRef.current.then(run, run);
    saveChainRef.current = task;
    return task;
  };

  const update = (modelId: string, patch: Partial<PricingDraft>): void => {
    const nextDrafts = {
      ...draftsRef.current,
      [modelId]: { ...(draftsRef.current[modelId] as PricingDraft), ...patch },
    };
    draftsRef.current = nextDrafts;
    dirtyRef.current.add(modelId);
    editRevisionRef.current += 1;
    setDrafts(nextDrafts);
  };

  // Esc 放弃当前格子的编辑：回退到服务端值并收起。
  const revertCell = (modelId: string, field: EditableField): void => {
    const entry = entriesRef.current.find((item) => item.modelId === modelId);
    if (entry) update(modelId, { [field]: entry[field] } as Partial<PricingDraft>);
    editingRef.current = null;
    setEditing(null);
  };

  function scheduleAutoSave(): void {
    if (dirtyRef.current.size === 0) return;
    autoSavePendingRef.current = true;
    if (autoSaveTimerRef.current !== null) window.clearTimeout(autoSaveTimerRef.current);
    autoSaveTimerRef.current = window.setTimeout(() => {
      autoSaveTimerRef.current = null;
      void flushAutoSave();
    }, 500);
  }

  async function flushAutoSave(): Promise<void> {
    // 用户还在编辑下一格时不要落盘半截值；等该格 blur 后再触发。
    if (editingRef.current || !autoSavePendingRef.current || dirtyRef.current.size === 0) {
      return;
    }
    autoSavePendingRef.current = false;
    const revisionAtSave = editRevisionRef.current;
    const saved = await enqueueSave(buildDraftEntries, "", true);
    if (saved && editRevisionRef.current !== revisionAtSave) {
      // 保存期间又有新编辑（通常是另一格已 blur），再补一轮。
      scheduleAutoSave();
    }
  }

  const cancelAutoSave = (): void => {
    if (autoSaveTimerRef.current !== null) {
      window.clearTimeout(autoSaveTimerRef.current);
      autoSaveTimerRef.current = null;
    }
    autoSavePendingRef.current = false;
  };

  const saveRow = async (modelId: string): Promise<void> => {
    cancelAutoSave();
    await enqueueSave(buildDraftEntries, `已保存 ${modelId} 的定价`);
  };

  const removeRow = async (modelId: string): Promise<void> => {
    const answer = await confirmDialog({
      k: "删除定价",
      title: `移除 ${modelId} 的定价？`,
      message: "仅从定价库移除；目录中的模型与其已设价格不受影响。",
      confirmText: "删除",
      danger: true,
    });
    if (!answer.confirmed) return;
    cancelAutoSave();
    await enqueueSave(
      () => buildDraftEntries().filter((entry) => entry.modelId !== modelId),
      `已移除 ${modelId} 的定价`,
    );
  };

  const addEntry = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    const modelId = add.modelId.trim();
    if (!modelId) return;
    if (entriesRef.current.some((entry) => entry.modelId === modelId)) {
      toastErr(`${modelId} 已在定价库中，直接在表格里编辑即可`, "已存在");
      return;
    }
    const entry: PricingEntry = {
      modelId,
      displayName: add.displayName.trim() || modelId,
      currency: add.currency.trim() || "CNY",
      inputCostPerMillion: add.inputCostPerMillion || "0",
      outputCostPerMillion: add.outputCostPerMillion || "0",
      cacheReadCostPerMillion: add.cacheReadCostPerMillion || "0",
      cacheCreationCostPerMillion: add.cacheCreationCostPerMillion || "0",
      offPeakMultiplier: add.offPeakMultiplier || "1",
    };
    cancelAutoSave();
    await enqueueSave(
      () => [...buildDraftEntries(), entry],
      `已新增 ${modelId} 的定价`,
    );
    setAdd({ ...EMPTY_ADD });
  };

  // 粘贴 cc-switch 的 model-pricing.json（或其 models 数组）合并导入：同 ID 覆盖。
  const importPaste = async (): Promise<void> => {
    let candidate: unknown;
    try {
      candidate = JSON.parse(pasteText);
    } catch {
      toastErr("内容不是合法 JSON", "解析失败");
      return;
    }
    const rawEntries = Array.isArray(candidate)
      ? candidate
      : ((candidate as { models?: unknown[] } | null)?.models ?? []);
    if (!Array.isArray(rawEntries) || rawEntries.length === 0) {
      toastErr("未解析到条目（期望 cc-switch 的 model-pricing.json）", "解析失败");
      return;
    }
    const incoming = rawEntries as Array<Record<string, unknown>>;
    const parsed: PricingEntry[] = [];
    for (const raw of incoming) {
      const modelId = typeof raw.modelId === "string" ? raw.modelId.trim() : "";
      if (!modelId) continue;
      parsed.push({
        modelId,
        displayName:
          typeof raw.displayName === "string" && raw.displayName.trim()
            ? raw.displayName.trim()
            : modelId,
        currency: typeof raw.currency === "string" ? raw.currency : "CNY",
        inputCostPerMillion: String(raw.inputCostPerMillion ?? "0"),
        outputCostPerMillion: String(raw.outputCostPerMillion ?? "0"),
        cacheReadCostPerMillion: String(raw.cacheReadCostPerMillion ?? "0"),
        cacheCreationCostPerMillion: String(raw.cacheCreationCostPerMillion ?? "0"),
        offPeakMultiplier: String(raw.offPeakMultiplier ?? "1"),
      });
    }
    if (parsed.length === 0) {
      toastErr("未解析到有效条目", "解析失败");
      return;
    }
    const existingIds = new Set(buildDraftEntries().map((entry) => entry.modelId));
    const added = parsed.filter((entry) => !existingIds.has(entry.modelId)).length;
    const overridden = parsed.length - added;
    cancelAutoSave();
    await enqueueSave(() => {
      const merged = new Map(buildDraftEntries().map((entry) => [entry.modelId, entry]));
      for (const entry of parsed) merged.set(entry.modelId, entry);
      return [...merged.values()];
    }, `粘贴导入完成：新增 ${added} / 覆盖 ${overridden} 条定价`);
    setPasteText("");
  };

  const normalizedQuery = query.trim().toLowerCase();
  const visibleEntries = normalizedQuery
    ? entries.filter(
        (entry) =>
          entry.modelId.toLowerCase().includes(normalizedQuery) ||
          entry.displayName.toLowerCase().includes(normalizedQuery),
      )
    : entries;

  const editableCellProps = (modelId: string, field: EditableField, num = false) => ({
    value: drafts[modelId]?.[field] ?? "",
    editing: editing?.modelId === modelId && editing.field === field,
    num,
    ariaLabel: `${modelId} ${field}`,
    onStartEdit: () => {
      editingRef.current = { modelId, field };
      setEditing({ modelId, field });
    },
    onEndEdit: () => {
      editingRef.current = null;
      setEditing(null);
    },
    onChange: (next: string) => update(modelId, { [field]: next } as Partial<PricingDraft>),
    onRevert: () => revertCell(modelId, field),
    onCommit: scheduleAutoSave,
  });

  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">下发 / 参考</p>
          <PageTitle icon={Tags}>模型定价库</PageTitle>
        </div>
        <span className="readout">
          {entries.length} 条 · 每 1M token 成本价（币种见条目）·{" "}
          {busy ? "保存中…" : "失焦自动保存"}
        </span>
      </header>
      <p className="readout" style={{ margin: 0, marginBottom: 18 }}>
        成本价库：入价 / 出价 / 缓存读 / 缓存写，每 1M token。DeepSeek 条目已切换为人民币官方价，
        空闲倍率 0.5（空闲价 = 高峰价 × 倍率）。高峰时段为北京时间周一至周五 09:00-12:00、
        14:00-18:00（法定节假日除外）。历史条目若仍为 USD 参考值，请按供应商人民币价目逐项核对。
        单元格默认只读展示，点击进入编辑，回车/失焦自动保存，Esc 放弃修改。
      </p>

      {error ? (
        <Notice tone="stop" k="加载失败">
          {error}
        </Notice>
      ) : null}

      <section className="pane">
        <div className="pane-h">
          <h2>新增定价条目</h2>
          <button
            className="btn btn-ghost btn-sm"
            type="button"
            disabled={busy}
            onClick={() => setPasteOpen((value) => !value)}
          >
            <Glyph icon={Upload} size={14} strokeWidth={2} />
            {pasteOpen ? "收起粘贴导入" : "从 cc-switch JSON 粘贴导入"}
          </button>
        </div>
        <div className="pane-b">
          <form className="form-row" onSubmit={(event) => void addEntry(event)}>
            <input
              className="input"
              style={{ width: 190 }}
              placeholder="模型 ID（如 deepseek-flash）"
              aria-label="模型 ID"
              value={add.modelId}
              onChange={(event) => setAdd({ ...add, modelId: event.target.value })}
              required
            />
            <input
              className="input"
              style={{ width: 140 }}
              placeholder="显示名（留空同 ID）"
              aria-label="显示名"
              value={add.displayName}
              onChange={(event) => setAdd({ ...add, displayName: event.target.value })}
            />
            <input
              className="input"
              style={{ width: 70 }}
              placeholder="币种"
              aria-label="币种"
              value={add.currency}
              onChange={(event) => setAdd({ ...add, currency: event.target.value })}
            />
            <input
              className="input num"
              style={{ width: 80 }}
              placeholder="空闲倍率"
              aria-label="空闲倍率"
              value={add.offPeakMultiplier}
              onChange={(event) => setAdd({ ...add, offPeakMultiplier: event.target.value })}
            />
            <input
              className="input num"
              style={{ width: 90 }}
              placeholder="入价"
              aria-label="入价"
              value={add.inputCostPerMillion}
              onChange={(event) => setAdd({ ...add, inputCostPerMillion: event.target.value })}
            />
            <input
              className="input num"
              style={{ width: 90 }}
              placeholder="出价"
              aria-label="出价"
              value={add.outputCostPerMillion}
              onChange={(event) => setAdd({ ...add, outputCostPerMillion: event.target.value })}
            />
            <input
              className="input num"
              style={{ width: 90 }}
              placeholder="缓存读"
              aria-label="缓存读"
              value={add.cacheReadCostPerMillion}
              onChange={(event) => setAdd({ ...add, cacheReadCostPerMillion: event.target.value })}
            />
            <input
              className="input num"
              style={{ width: 90 }}
              placeholder="缓存写"
              aria-label="缓存写"
              value={add.cacheCreationCostPerMillion}
              onChange={(event) => setAdd({ ...add, cacheCreationCostPerMillion: event.target.value })}
            />
            <button className="btn" type="submit" disabled={busy || !add.modelId.trim()}>
              添加
            </button>
          </form>
          {pasteOpen ? (
            <div style={{ marginTop: 10 }}>
              <textarea
                placeholder={'粘贴 cc-switch 的 model-pricing.json（{"models":[…]} 或条目数组）'}
                value={pasteText}
                onChange={(event) => setPasteText(event.target.value)}
                style={{ minHeight: 120 }}
              />
              <div className="form-row" style={{ marginTop: 8 }}>
                <button
                  className="btn"
                  type="button"
                  disabled={busy || !pasteText.trim()}
                  onClick={() => void importPaste()}
                >
                  解析并合并导入
                </button>
                <span className="readout">同模型 ID 将覆盖现有条目。</span>
              </div>
            </div>
          ) : null}
        </div>
      </section>

      <section className="pane d1">
        <div className="pane-h">
          <h2>定价清单</h2>
          <div className="form-row" style={{ gap: 8 }}>
            <input
              className="input"
              style={{ width: 220 }}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="搜索模型 ID / 显示名（如 claude、gpt、grok）"
              aria-label="搜索成本价条目"
            />
            <span className="readout">
              {visibleEntries.length} / {entries.length} 条
            </span>
          </div>
        </div>
        <div className="pane-b flush table-scroll vscroll">
          <table className="table table-fixed">
            <colgroup>
              <col style={{ width: "13%" }} />
              <col style={{ width: "15%" }} />
              <col style={{ width: "7%" }} />
              <col style={{ width: "9%" }} />
              <col style={{ width: "9%" }} />
              <col style={{ width: "9%" }} />
              <col style={{ width: "8%" }} />
              <col style={{ width: "8%" }} />
              <col style={{ width: "16%" }} />
              <col style={{ width: "6%" }} />
            </colgroup>
            <thead>
              <tr>
                <th scope="col">模型 ID</th>
                <th scope="col">显示名</th>
                <th scope="col">币种</th>
                <th scope="col" className="num">入价（高峰）</th>
                <th scope="col" className="num">出价（高峰）</th>
                <th scope="col" className="num">缓存读（高峰）</th>
                <th scope="col" className="num">缓存写（高峰）</th>
                <th scope="col" className="num">空闲倍率</th>
                <th scope="col" className="num">空闲价（入 / 出 / 缓存读）</th>
                <th scope="col" aria-label="操作" />
              </tr>
            </thead>
            <tbody>
              {visibleEntries.map((entry) => {
                const draft = drafts[entry.modelId] ?? toDraft(entry);
                const multiplier = draft.offPeakMultiplier;
                return (
                  <tr key={entry.modelId}>
                    <td>{entry.modelId}</td>
                    <td>
                      <EditableCell {...editableCellProps(entry.modelId, "displayName")} />
                    </td>
                    <td>
                      <EditableCell {...editableCellProps(entry.modelId, "currency")} />
                    </td>
                    <td>
                      <EditableCell {...editableCellProps(entry.modelId, "inputCostPerMillion", true)} />
                    </td>
                    <td>
                      <EditableCell {...editableCellProps(entry.modelId, "outputCostPerMillion", true)} />
                    </td>
                    <td>
                      <EditableCell {...editableCellProps(entry.modelId, "cacheReadCostPerMillion", true)} />
                    </td>
                    <td>
                      <EditableCell {...editableCellProps(entry.modelId, "cacheCreationCostPerMillion", true)} />
                    </td>
                    <td>
                      <EditableCell {...editableCellProps(entry.modelId, "offPeakMultiplier", true)} />
                    </td>
                    <td className="num readout" title="空闲价 = 高峰价 × 空闲倍率">
                      {offPeakPrice(draft.inputCostPerMillion, multiplier)} /{" "}
                      {offPeakPrice(draft.outputCostPerMillion, multiplier)} /{" "}
                      {offPeakPrice(draft.cacheReadCostPerMillion, multiplier)}
                    </td>
                    <td>
                      <div className="form-row" style={{ justifyContent: "flex-end" }}>
                        <button
                          className="btn btn-ghost btn-sm"
                          type="button"
                          disabled={busy}
                          onClick={() => void saveRow(entry.modelId)}
                        >
                          保存
                        </button>
                        <button
                          className="btn btn-danger btn-sm"
                          type="button"
                          disabled={busy}
                          onClick={() => void removeRow(entry.modelId)}
                        >
                          删除
                        </button>
                      </div>
                    </td>
                  </tr>
                );
              })}
              {visibleEntries.length === 0 ? (
                <tr>
                  <td colSpan={10} className="readout">
                    {entries.length === 0
                      ? "定价库为空，先添加条目或粘贴 cc-switch JSON。"
                      : "没有匹配的模型，换个关键词试试。"}
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}
