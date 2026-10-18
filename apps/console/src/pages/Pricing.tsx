import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, type ApiError } from "../api.js";
import { confirmDialog, toastErr, toastOk } from "../components/Feedback.js";
import { EditableCell } from "../components/EditableCell.js";
import { Notice } from "../components/StatusChip.js";

/** 可点击编辑的单元格字段。 */
type EditableField =
  | "displayName"
  | "inputCostPerMillion"
  | "outputCostPerMillion"
  | "cacheReadCostPerMillion"
  | "cacheCreationCostPerMillion";

/** 与 /console/api/model-pricing 条目对齐：每 1M token 价格（USD 口径）。 */
interface PricingEntry {
  modelId: string;
  displayName: string;
  inputCostPerMillion: string;
  outputCostPerMillion: string;
  cacheReadCostPerMillion: string;
  cacheCreationCostPerMillion: string;
}

/** GET / PUT 都携带内容版本；PUT 版本不符时后端返回 409（他人已修改）。 */
interface PricingDoc {
  entries: PricingEntry[];
  version: string;
}

interface PricingDraft {
  displayName: string;
  inputCostPerMillion: string;
  outputCostPerMillion: string;
  cacheReadCostPerMillion: string;
  cacheCreationCostPerMillion: string;
}

const EMPTY_ADD = {
  modelId: "",
  displayName: "",
  inputCostPerMillion: "",
  outputCostPerMillion: "",
  cacheReadCostPerMillion: "",
  cacheCreationCostPerMillion: "",
};

function toDraft(entry: PricingEntry): PricingDraft {
  return {
    displayName: entry.displayName,
    inputCostPerMillion: entry.inputCostPerMillion,
    outputCostPerMillion: entry.outputCostPerMillion,
    cacheReadCostPerMillion: entry.cacheReadCostPerMillion,
    cacheCreationCostPerMillion: entry.cacheCreationCostPerMillion,
  };
}

export function PricingPage() {
  const [entries, setEntries] = useState<PricingEntry[]>([]);
  // 当前文档内容版本：保存时携带给后端做乐观锁比对（他人先改则 409）。
  const [version, setVersion] = useState("");
  const [drafts, setDrafts] = useState<Record<string, PricingDraft>>({});
  const [add, setAdd] = useState({ ...EMPTY_ADD });
  const [pasteText, setPasteText] = useState("");
  const [pasteOpen, setPasteOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // 单元格点击编辑：同一时刻只编辑一个格子；Esc 还原，回车/失焦收起，行内「保存」才提交。
  const [editing, setEditing] = useState<{ modelId: string; field: EditableField } | null>(null);

  const load = useCallback(async () => {
    try {
      const data = await api<PricingDoc>("/model-pricing");
      setEntries(data.entries);
      setVersion(data.version);
      setDrafts(
        Object.fromEntries(data.entries.map((entry) => [entry.modelId, toDraft(entry)])),
      );
      setError(null);
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载失败");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // 全量替换式保存：前端整表 PUT + expectedVersion；版本冲突（他人已改）时不静默覆盖，
  // 提示后自动重载服务端最新值，当前未保存的编辑丢弃。
  const saveList = async (next: PricingEntry[], successMessage: string): Promise<void> => {
    setBusy(true);
    try {
      const result = await api<PricingDoc>("/model-pricing", {
        method: "PUT",
        body: JSON.stringify({ entries: next, expectedVersion: version }),
      });
      setEntries(result.entries);
      setVersion(result.version);
      setDrafts(
        Object.fromEntries(result.entries.map((entry) => [entry.modelId, toDraft(entry)])),
      );
      toastOk(successMessage, "已保存");
    } catch (saveError) {
      if ((saveError as ApiError).code === 409) {
        toastErr("定价库已被他人修改，已为你刷新到最新值", "保存未生效");
        await load();
      } else {
        toastErr(saveError instanceof Error ? saveError.message : "保存失败", "保存失败");
      }
    } finally {
      setBusy(false);
    }
  };

  const update = (modelId: string, patch: Partial<PricingDraft>): void => {
    setDrafts((current) => ({
      ...current,
      [modelId]: { ...(current[modelId] as PricingDraft), ...patch },
    }));
  };

  // Esc 放弃当前格子的编辑：回退到服务端值并收起。
  const revertCell = (modelId: string, field: EditableField): void => {
    const entry = entries.find((item) => item.modelId === modelId);
    if (entry) update(modelId, { [field]: entry[field] } as Partial<PricingDraft>);
    setEditing(null);
  };

  const saveRow = async (modelId: string): Promise<void> => {
    const draft = drafts[modelId];
    if (!draft) return;
    await saveList(
      entries.map((entry) =>
        entry.modelId === modelId ? { modelId, ...draft } : entry,
      ),
      `已保存 ${modelId} 的定价`,
    );
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
    await saveList(
      entries.filter((entry) => entry.modelId !== modelId),
      `已移除 ${modelId} 的定价`,
    );
  };

  const addEntry = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    const modelId = add.modelId.trim();
    if (!modelId) return;
    if (entries.some((entry) => entry.modelId === modelId)) {
      toastErr(`${modelId} 已在定价库中，直接在表格里编辑即可`, "已存在");
      return;
    }
    const entry: PricingEntry = {
      modelId,
      displayName: add.displayName.trim() || modelId,
      inputCostPerMillion: add.inputCostPerMillion || "0",
      outputCostPerMillion: add.outputCostPerMillion || "0",
      cacheReadCostPerMillion: add.cacheReadCostPerMillion || "0",
      cacheCreationCostPerMillion: add.cacheCreationCostPerMillion || "0",
    };
    await saveList([...entries, entry], `已新增 ${modelId} 的定价`);
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
    const merged = new Map(entries.map((entry) => [entry.modelId, entry]));
    let added = 0;
    let overridden = 0;
    for (const raw of incoming) {
      const modelId = typeof raw.modelId === "string" ? raw.modelId.trim() : "";
      if (!modelId) continue;
      if (merged.has(modelId)) overridden += 1;
      else added += 1;
      merged.set(modelId, {
        modelId,
        displayName:
          typeof raw.displayName === "string" && raw.displayName.trim()
            ? raw.displayName.trim()
            : modelId,
        inputCostPerMillion: String(raw.inputCostPerMillion ?? "0"),
        outputCostPerMillion: String(raw.outputCostPerMillion ?? "0"),
        cacheReadCostPerMillion: String(raw.cacheReadCostPerMillion ?? "0"),
        cacheCreationCostPerMillion: String(raw.cacheCreationCostPerMillion ?? "0"),
      });
    }
    await saveList(
      [...merged.values()],
      `粘贴导入完成：新增 ${added} / 覆盖 ${overridden} 条定价`,
    );
    setPasteText("");
  };

  const editableCellProps = (modelId: string, field: EditableField, num = false) => ({
    value: drafts[modelId]?.[field] ?? "",
    editing: editing?.modelId === modelId && editing.field === field,
    num,
    ariaLabel: `${modelId} ${field}`,
    onStartEdit: () => setEditing({ modelId, field }),
    onEndEdit: () => setEditing(null),
    onChange: (next: string) => update(modelId, { [field]: next } as Partial<PricingDraft>),
    onRevert: () => revertCell(modelId, field),
  });

  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">下发 / 参考</p>
          <h1>模型定价库</h1>
        </div>
        <span className="readout">{entries.length} 条 · 每 1M token 价格（USD 口径）</span>
      </header>
      <p className="readout" style={{ margin: 0, marginBottom: 18 }}>
        参考 cc-switch / models.dev 的模型定价（入价 / 出价 / 缓存读 / 缓存写，每 1M token），
        与平台 credit 价格同口径。「模型目录」手动添加与「从上游导入」时按模型 ID 自动带价；可粘贴
        cc-switch 的 model-pricing.json 合并更新。单元格默认只读展示，点击进入编辑，回车/失焦收起。
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
          <span className="readout">命中模型 ID 时目录/导入自动带价</span>
        </div>
        <div className="pane-b flush table-scroll vscroll">
          <table className="table table-fixed">
            <colgroup>
              <col style={{ width: "18%" }} />
              <col style={{ width: "20%" }} />
              <col style={{ width: "12%" }} />
              <col style={{ width: "12%" }} />
              <col style={{ width: "12%" }} />
              <col style={{ width: "12%" }} />
              <col style={{ width: "14%" }} />
            </colgroup>
            <thead>
              <tr>
                <th scope="col">模型 ID</th>
                <th scope="col">显示名</th>
                <th scope="col" className="num">入价</th>
                <th scope="col" className="num">出价</th>
                <th scope="col" className="num">缓存读</th>
                <th scope="col" className="num">缓存写</th>
                <th scope="col" aria-label="操作" />
              </tr>
            </thead>
            <tbody>
              {entries.map((entry) => (
                <tr key={entry.modelId}>
                  <td>{entry.modelId}</td>
                  <td>
                    <EditableCell {...editableCellProps(entry.modelId, "displayName")} />
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
              ))}
              {entries.length === 0 ? (
                <tr>
                  <td colSpan={7} className="readout">
                    定价库为空，先添加条目或粘贴 cc-switch JSON。
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
