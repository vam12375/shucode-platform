import { useCallback, useEffect, useState } from "react";
import { api } from "../api.js";

interface ModelRow {
  modelId: string;
  displayName: string;
  upstreamModel: string | null;
  inputPricePerMtok: string;
  outputPricePerMtok: string;
  cacheReadPricePerMtok: string;
  cacheWritePricePerMtok: string;
  enabled: boolean;
  sortOrder: number;
}

interface DraftState {
  displayName: string;
  upstreamModel: string;
  inputPricePerMtok: string;
  outputPricePerMtok: string;
  cacheReadPricePerMtok: string;
  cacheWritePricePerMtok: string;
  enabled: boolean;
  sortOrder: string;
}

function toDraft(model: ModelRow): DraftState {
  return {
    displayName: model.displayName,
    upstreamModel: model.upstreamModel ?? "",
    inputPricePerMtok: model.inputPricePerMtok,
    outputPricePerMtok: model.outputPricePerMtok,
    cacheReadPricePerMtok: model.cacheReadPricePerMtok,
    cacheWritePricePerMtok: model.cacheWritePricePerMtok,
    enabled: model.enabled,
    sortOrder: String(model.sortOrder),
  };
}

export function ModelsPage() {
  const [models, setModels] = useState<ModelRow[]>([]);
  const [drafts, setDrafts] = useState<Record<string, DraftState>>({});
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [newModelId, setNewModelId] = useState("");

  const load = useCallback(async () => {
    try {
      const data = await api<{ models: ModelRow[] }>("/models?includeDisabled=1");
      setModels(data.models);
      setDrafts(Object.fromEntries(data.models.map((model) => [model.modelId, toDraft(model)])));
      setError(null);
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载失败");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const save = async (modelId: string): Promise<void> => {
    const draft = drafts[modelId];
    if (!draft) return;
    setBusy(true);
    setError(null);
    try {
      await api(`/models/${encodeURIComponent(modelId)}`, {
        method: "PUT",
        body: JSON.stringify({
          displayName: draft.displayName,
          upstreamModel: draft.upstreamModel.trim() || null,
          inputPricePerMtok: draft.inputPricePerMtok,
          outputPricePerMtok: draft.outputPricePerMtok,
          cacheReadPricePerMtok: draft.cacheReadPricePerMtok,
          cacheWritePricePerMtok: draft.cacheWritePricePerMtok,
          enabled: draft.enabled,
          sortOrder: Number.parseInt(draft.sortOrder, 10) || 0,
        }),
      });
      await load();
    } catch (saveError) {
      setError(saveError instanceof Error ? saveError.message : "保存失败");
    } finally {
      setBusy(false);
    }
  };

  const update = (modelId: string, patch: Partial<DraftState>): void => {
    setDrafts((current) => ({
      ...current,
      [modelId]: { ...(current[modelId] as DraftState), ...patch },
    }));
  };

  return (
    <div>
      <h1>模型目录</h1>
      <p className="muted">
        这里定义的模型会随用户级配置下发到客户端（作为网关 Provider 的模型清单）；
        价格用于额度计量（每 1M token 的 credit 数）。上游模型名留空时按模型 ID 转发。
      </p>
      {error ? <p className="error">{error}</p> : null}

      <div className="card">
        <div className="row">
          <input
            className="grow"
            placeholder="新模型 ID（对应上游模型名，如 claude-sonnet-4-5）"
            value={newModelId}
            onChange={(event) => setNewModelId(event.target.value)}
          />
          <button
            className="secondary"
            disabled={busy || !newModelId.trim()}
            onClick={() =>
              void (async () => {
                const modelId = newModelId.trim();
                setBusy(true);
                try {
                  await api(`/models/${encodeURIComponent(modelId)}`, {
                    method: "PUT",
                    body: JSON.stringify({
                      displayName: modelId,
                      inputPricePerMtok: "0",
                      outputPricePerMtok: "0",
                      enabled: false,
                    }),
                  });
                  setNewModelId("");
                  await load();
                } catch (createError) {
                  setError(createError instanceof Error ? createError.message : "创建失败");
                } finally {
                  setBusy(false);
                }
              })()
            }
          >
            添加模型
          </button>
        </div>
      </div>

      <div className="card">
        <table>
          <thead>
            <tr>
              <th>模型 ID</th>
              <th>显示名</th>
              <th>上游模型</th>
              <th>入价</th>
              <th>出价</th>
              <th>缓存读</th>
              <th>缓存写</th>
              <th>排序</th>
              <th>启用</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {models.map((model) => {
              const draft = drafts[model.modelId] ?? toDraft(model);
              return (
                <tr key={model.modelId}>
                  <td className="mono">{model.modelId}</td>
                  <td>
                    <input
                      style={{ width: 130 }}
                      value={draft.displayName}
                      onChange={(event) => update(model.modelId, { displayName: event.target.value })}
                    />
                  </td>
                  <td>
                    <input
                      style={{ width: 120 }}
                      value={draft.upstreamModel}
                      placeholder="同模型 ID"
                      onChange={(event) =>
                        update(model.modelId, { upstreamModel: event.target.value })
                      }
                    />
                  </td>
                  <td>
                    <input
                      style={{ width: 64 }}
                      value={draft.inputPricePerMtok}
                      onChange={(event) =>
                        update(model.modelId, { inputPricePerMtok: event.target.value })
                      }
                    />
                  </td>
                  <td>
                    <input
                      style={{ width: 64 }}
                      value={draft.outputPricePerMtok}
                      onChange={(event) =>
                        update(model.modelId, { outputPricePerMtok: event.target.value })
                      }
                    />
                  </td>
                  <td>
                    <input
                      style={{ width: 64 }}
                      value={draft.cacheReadPricePerMtok}
                      onChange={(event) =>
                        update(model.modelId, { cacheReadPricePerMtok: event.target.value })
                      }
                    />
                  </td>
                  <td>
                    <input
                      style={{ width: 64 }}
                      value={draft.cacheWritePricePerMtok}
                      onChange={(event) =>
                        update(model.modelId, { cacheWritePricePerMtok: event.target.value })
                      }
                    />
                  </td>
                  <td>
                    <input
                      style={{ width: 48 }}
                      value={draft.sortOrder}
                      onChange={(event) => update(model.modelId, { sortOrder: event.target.value })}
                    />
                  </td>
                  <td>
                    <input
                      type="checkbox"
                      checked={draft.enabled}
                      onChange={(event) => update(model.modelId, { enabled: event.target.checked })}
                    />
                  </td>
                  <td style={{ textAlign: "right" }}>
                    <button className="secondary" disabled={busy} onClick={() => void save(model.modelId)}>
                      保存
                    </button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}
