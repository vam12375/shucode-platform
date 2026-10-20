import { useCallback, useEffect, useState } from "react";
import { api } from "../api.js";
import { confirmDialog, toastErr, toastOk } from "../components/Feedback.js";
import { EditableCell } from "../components/EditableCell.js";
import {
  ModelConfigDrawer,
  type ModelCapabilityConfig,
  type ModelConfigMode,
  type ModelConfigTarget,
} from "../components/ModelConfigDrawer.js";
import { Select } from "../components/Select.js";
import { Chip, Notice } from "../components/StatusChip.js";

interface ModelRow {
  modelId: string;
  displayName: string;
  upstreamModel: string | null;
  upstreamProviderId: string | null;
  inputPricePerMtok: string;
  outputPricePerMtok: string;
  cacheReadPricePerMtok: string;
  cacheWritePricePerMtok: string;
  offPeakMultiplier: string;
  configMode?: ModelConfigMode;
  config?: ModelCapabilityConfig | null;
  enabled: boolean;
  sortOrder: number;
}

interface ProviderOption {
  id: string;
  name: string;
  enabled: boolean;
}

interface RouteRow {
  id: string;
  modelId: string;
  providerId: string;
  providerName: string;
  upstreamModel: string;
  priority: number;
  weight: number;
  enabled: boolean;
}

/** 定价库条目（/console/api/model-pricing）：命中模型 ID 时自动带价。 */
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

interface DraftState {
  displayName: string;
  upstreamModel: string;
  upstreamProviderId: string;
  inputPricePerMtok: string;
  outputPricePerMtok: string;
  cacheReadPricePerMtok: string;
  cacheWritePricePerMtok: string;
  offPeakMultiplier: string;
  enabled: boolean;
  sortOrder: string;
}

function toDraft(model: ModelRow): DraftState {
  return {
    displayName: model.displayName,
    upstreamModel: model.upstreamModel ?? "",
    upstreamProviderId: model.upstreamProviderId ?? "",
    inputPricePerMtok: model.inputPricePerMtok,
    outputPricePerMtok: model.outputPricePerMtok,
    cacheReadPricePerMtok: model.cacheReadPricePerMtok,
    cacheWritePricePerMtok: model.cacheWritePricePerMtok,
    offPeakMultiplier: model.offPeakMultiplier || "1.000000",
    enabled: model.enabled,
    sortOrder: String(model.sortOrder),
  };
}

/** 成本价展示：空闲价 = 高峰价 × 倍率。 */
function formatCost(value: string, multiplier: string | undefined): string {
  const price = Number.parseFloat(value) || 0;
  const factor = Number.parseFloat(multiplier ?? "") || 1;
  return factor === 1
    ? price.toLocaleString("en-US", { maximumFractionDigits: 6 })
    : `${price.toLocaleString("en-US", { maximumFractionDigits: 6 })} / ${(price * factor).toLocaleString("en-US", { maximumFractionDigits: 6 })}`;
}

function formatToken(value: number): string {
  if (value >= 1_000_000) {
    return `${(value / 1_000_000).toFixed(value % 1_000_000 === 0 ? 0 : 1)}M`;
  }
  if (value >= 1000) return `${Math.round(value / 1000)}k`;
  return String(value);
}

export function ModelsPage() {
  const [models, setModels] = useState<ModelRow[]>([]);
  const [providers, setProviders] = useState<ProviderOption[]>([]);
  const [pricingMap, setPricingMap] = useState<Map<string, PricingEntry>>(new Map());
  const [drafts, setDrafts] = useState<Record<string, DraftState>>({});
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [newModelId, setNewModelId] = useState("");
  // 从上游导入：选供应商 → 拉取模型列表 → 勾选 → 批量建档。
  const [importProviderId, setImportProviderId] = useState("");
  const [importModels, setImportModels] = useState<string[] | null>(null);
  const [importSelected, setImportSelected] = useState<Set<string>>(new Set());
  const [importBusy, setImportBusy] = useState(false);
  // 渠道池：按模型维护多条上游路由（priority 越小越优先，同优先级按 weight 加权）。
  const [routeModelId, setRouteModelId] = useState("");
  const [routes, setRoutes] = useState<RouteRow[]>([]);
  const [routeProviderId, setRouteProviderId] = useState("");
  const [routeUpstreamModel, setRouteUpstreamModel] = useState("");
  const [routePriority, setRoutePriority] = useState("100");
  const [routeWeight, setRouteWeight] = useState("1");
  const [routeBusy, setRouteBusy] = useState(false);
  // 点编辑单元格：同一时刻只编辑一格（Enter/失焦收起，Esc 还原草稿）。
  type ModelsEditableField = "displayName" | "upstreamModel" | "inputPricePerMtok" | "outputPricePerMtok" | "cacheReadPricePerMtok" | "cacheWritePricePerMtok" | "offPeakMultiplier" | "sortOrder";
  const [editingCell, setEditingCell] = useState<{ modelId: string; field: ModelsEditableField } | null>(null);
  const [configTarget, setConfigTarget] = useState<ModelRow | null>(null);

  const load = useCallback(async () => {
    try {
      const [modelData, providerData, pricingData] = await Promise.all([
        api<{ models: ModelRow[] }>("/models?includeDisabled=1"),
        api<{ providers: ProviderOption[] }>("/providers"),
        api<{ entries: PricingEntry[] }>("/model-pricing"),
      ]);
      setModels(modelData.models);
      setProviders(providerData.providers);
      setPricingMap(new Map(pricingData.entries.map((entry) => [entry.modelId, entry])));
      setDrafts(
        Object.fromEntries(modelData.models.map((model) => [model.modelId, toDraft(model)])),
      );
      setError(null);
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载失败");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const loadRoutes = useCallback(async (modelId: string): Promise<void> => {
    if (!modelId) {
      setRoutes([]);
      return;
    }
    try {
      const data = await api<{ routes: RouteRow[] }>(`/models/${encodeURIComponent(modelId)}/routes`);
      setRoutes(data.routes);
    } catch (loadError) {
      toastErr(loadError instanceof Error ? loadError.message : "路由加载失败");
    }
  }, []);

  useEffect(() => {
    void loadRoutes(routeModelId);
  }, [routeModelId, loadRoutes]);

  const addRoute = async (): Promise<void> => {
    if (!routeModelId || !routeProviderId) return;
    setRouteBusy(true);
    try {
      await api(`/models/${encodeURIComponent(routeModelId)}/routes`, {
        method: "POST",
        body: JSON.stringify({
          providerId: routeProviderId,
          upstreamModel: routeUpstreamModel.trim(),
          priority: Number.parseInt(routePriority, 10) || 0,
          weight: Number.parseInt(routeWeight, 10) || 1,
        }),
      });
      setRouteProviderId("");
      setRouteUpstreamModel("");
      await loadRoutes(routeModelId);
      toastOk("已添加路由", "已添加");
    } catch (actionError) {
      toastErr(actionError instanceof Error ? actionError.message : "添加路由失败");
    } finally {
      setRouteBusy(false);
    }
  };

  const toggleRoute = async (route: RouteRow): Promise<void> => {
    setRouteBusy(true);
    try {
      await api(`/routes/${route.id}`, {
        method: "PATCH",
        body: JSON.stringify({ enabled: !route.enabled }),
      });
      await loadRoutes(routeModelId);
    } catch (actionError) {
      toastErr(actionError instanceof Error ? actionError.message : "更新路由失败");
    } finally {
      setRouteBusy(false);
    }
  };

  const deleteRoute = async (route: RouteRow): Promise<void> => {
    const answer = await confirmDialog({
      k: "删除路由",
      title: `删除 ${route.providerName} 的路由？`,
      message: "删除后该供应商不再参与此模型的调度；至少保留一条路由或模型自身的供应商绑定。",
      confirmText: "删除",
      danger: true,
    });
    if (!answer.confirmed) return;
    setRouteBusy(true);
    try {
      await api(`/routes/${route.id}`, { method: "DELETE" });
      await loadRoutes(routeModelId);
      toastOk("已删除路由", "已删除");
    } catch (actionError) {
      toastErr(actionError instanceof Error ? actionError.message : "删除路由失败");
    } finally {
      setRouteBusy(false);
    }
  };

  const save = async (modelId: string): Promise<void> => {
    const draft = drafts[modelId];
    if (!draft) return;
    setBusy(true);
    try {
      await api(`/models/${encodeURIComponent(modelId)}`, {
        method: "PUT",
        body: JSON.stringify({
          displayName: draft.displayName,
          upstreamModel: draft.upstreamModel.trim() || null,
          upstreamProviderId: draft.upstreamProviderId || null,
          inputPricePerMtok: draft.inputPricePerMtok,
          outputPricePerMtok: draft.outputPricePerMtok,
          cacheReadPricePerMtok: draft.cacheReadPricePerMtok,
          cacheWritePricePerMtok: draft.cacheWritePricePerMtok,
          offPeakMultiplier: draft.offPeakMultiplier || "1",
          enabled: draft.enabled,
          sortOrder: Number.parseInt(draft.sortOrder, 10) || 0,
        }),
      });
      await load();
      toastOk(`已保存模型 ${modelId}`, "已保存");
    } catch (saveError) {
      toastErr(saveError instanceof Error ? saveError.message : "保存失败", "保存失败");
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

  // Esc 放弃当前格子：回退到服务端原始值。
  const editableCellProps = (modelId: string, field: ModelsEditableField, num = false) => ({
    value: drafts[modelId]?.[field] ?? "",
    editing: editingCell?.modelId === modelId && editingCell.field === field,
    num,
    ariaLabel: `${modelId} ${field}`,
    onStartEdit: () => setEditingCell({ modelId, field }),
    onEndEdit: () => setEditingCell(null),
    onChange: (next: string) => update(modelId, { [field]: next } as Partial<DraftState>),
    onRevert: () => {
      const original = models.find((model) => model.modelId === modelId);
      if (original) update(modelId, { [field]: toDraft(original)[field] });
    },
  });

  const missingModelIds = (ids: string[]): Set<string> =>
    new Set(ids.filter((id) => !models.some((model) => model.modelId === id)));

  const fetchUpstreamModels = async (): Promise<void> => {
    if (!importProviderId) return;
    setImportBusy(true);
    try {
      const data = await api<{ models: string[] }>(`/providers/${importProviderId}/models`);
      setImportModels(data.models);
      setImportSelected(missingModelIds(data.models));
      if (data.models.length === 0) {
        toastErr("上游未返回任何模型", "没有可导入的模型");
      }
    } catch (actionError) {
      toastErr(actionError instanceof Error ? actionError.message : "拉取模型列表失败");
    } finally {
      setImportBusy(false);
    }
  };

  const toggleImport = (modelId: string): void => {
    setImportSelected((current) => {
      const next = new Set(current);
      if (next.has(modelId)) next.delete(modelId);
      else next.add(modelId);
      return next;
    });
  };

  const runImport = async (): Promise<void> => {
    if (!importProviderId || importSelected.size === 0) return;
    setImportBusy(true);
    try {
      const result = await api<{ created: string[]; skipped: string[] }>("/models/import", {
        method: "POST",
        body: JSON.stringify({ providerId: importProviderId, modelIds: [...importSelected] }),
      });
      await load();
      toastOk(
        `已导入 ${result.created.length} 个模型（跳过 ${result.skipped.length} 个已存在）。默认停用，填好价格后再启用。`,
        "导入完成",
      );
      setImportModels(null);
    } catch (actionError) {
      toastErr(actionError instanceof Error ? actionError.message : "导入失败", "导入失败");
    } finally {
      setImportBusy(false);
    }
  };

  const remove = async (modelId: string): Promise<void> => {
    const answer = await confirmDialog({
      k: "删除模型",
      title: `删除 ${modelId}？`,
      message: "价格配置将一并移除，仍缓存旧配置的客户端请求会得到 404。",
      confirmText: "删除",
      danger: true,
    });
    if (!answer.confirmed) return;
    setBusy(true);
    try {
      await api(`/models/${encodeURIComponent(modelId)}`, { method: "DELETE" });
      await load();
      toastOk(`已删除模型 ${modelId}`, "已删除");
    } catch (deleteError) {
      toastErr(deleteError instanceof Error ? deleteError.message : "删除失败", "删除失败");
    } finally {
      setBusy(false);
    }
  };

  const addModel = async (): Promise<void> => {
    const modelId = newModelId.trim();
    setBusy(true);
    try {
      // 命中定价库时自动带价（显示名/入价/出价/缓存读写），避免免费上线。
      const pricing = pricingMap.get(modelId);
      await api(`/models/${encodeURIComponent(modelId)}`, {
        method: "PUT",
        body: JSON.stringify({
          displayName: pricing?.displayName ?? modelId,
          inputPricePerMtok: pricing?.inputCostPerMillion ?? "0",
          outputPricePerMtok: pricing?.outputCostPerMillion ?? "0",
          offPeakMultiplier: pricing?.offPeakMultiplier ?? "1",
          ...(pricing
            ? {
                cacheReadPricePerMtok: pricing.cacheReadCostPerMillion,
                cacheWritePricePerMtok: pricing.cacheCreationCostPerMillion,
              }
            : {}),
          enabled: false,
        }),
      });
      setNewModelId("");
      await load();
      toastOk(
        pricing
          ? `已添加模型 ${modelId}（已按定价库带价，默认停用）`
          : `已添加模型 ${modelId}（未命中定价库，价格为 0，请先填价再启用）`,
        "已添加",
      );
    } catch (createError) {
      toastErr(createError instanceof Error ? createError.message : "创建失败", "创建失败");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">下发 / 价目</p>
          <h1>模型目录</h1>
        </div>
      </header>

      {error ? (
        <Notice tone="stop" k="操作未完成">
          {error}
        </Notice>
      ) : null}

      <section className="pane">
        <div className="pane-b">
          <div className="form-row">
            <input
              className="input grow"
              placeholder="新模型 ID（对应上游模型名，如 claude-sonnet-4-5）"
              aria-label="新模型 ID"
              value={newModelId}
              onChange={(event) => setNewModelId(event.target.value)}
            />
            <button className="btn btn-ghost" type="button" disabled={busy || !newModelId.trim()} onClick={() => void addModel()}>
              添加模型
            </button>
          </div>
          <p className="readout" style={{ margin: 0, marginTop: 10 }}>
            售价单位：credit / 1M token（1 credit = ¥1）· 空闲倍率用于按北京时间高峰窗口自动打折 ·
            上游模型留空按模型 ID 转发。模型卡片同时展示定价库里的成本价（只读），便于核对毛利。
            DeepSeek 已按官方人民币价核对；其他模型若原为 USD 参考价，请按供应商人民币价目更新后再启用。
          </p>
          <div className="form-row" style={{ marginTop: 12 }}>
            <span className="readout">从上游导入：</span>
            <Select
              ariaLabel="导入来源供应商"
              style={{ width: 200 }}
              value={importProviderId}
              onChange={(value) => {
                setImportProviderId(value);
                setImportModels(null);
              }}
              options={[
                { value: "", label: "选择供应商…" },
                ...providers.map((provider) => ({
                  value: provider.id,
                  label: provider.enabled ? provider.name : `${provider.name}（已停用）`,
                })),
              ]}
            />
            <button
              className="btn btn-ghost btn-sm"
              type="button"
              disabled={importBusy || !importProviderId}
              onClick={() => void fetchUpstreamModels()}
            >
              {importBusy ? "拉取中…" : "拉取模型列表"}
            </button>
            {importModels ? (
              <>
                <button
                  className="btn btn-ghost btn-sm"
                  type="button"
                  disabled={importBusy}
                  onClick={() => setImportSelected(missingModelIds(importModels))}
                >
                  全选未导入
                </button>
                <button
                  className="btn btn-ghost btn-sm"
                  type="button"
                  disabled={importBusy}
                  onClick={() => setImportSelected(new Set())}
                >
                  清空选择
                </button>
                <button
                  className="btn btn-sm"
                  type="button"
                  disabled={importBusy || importSelected.size === 0}
                  onClick={() => void runImport()}
                >
                  导入所选（{importSelected.size}）
                </button>
              </>
            ) : null}
          </div>
          {importModels ? (
            <div className="form-row" style={{ marginTop: 10, flexWrap: "wrap" }}>
              {importModels.map((modelId) => {
                const existing = models.some((model) => model.modelId === modelId);
                const selected = importSelected.has(modelId);
                return (
                  <button
                    key={modelId}
                    type="button"
                    aria-pressed={selected}
                    disabled={existing || importBusy}
                    className={
                      "import-chip" + (existing ? " existing" : selected ? " selected" : "")
                    }
                    onClick={() => toggleImport(modelId)}
                  >
                    {modelId}
                    {existing ? " · 已在目录" : ""}
                  </button>
                );
              })}
            </div>
          ) : null}
        </div>
      </section>

      <section className="pane d1">
        <div className="pane-h">
          <h2>模型清单</h2>
          <span className="readout">{models.length} 个 · 保存后随下一轮配置下发</span>
        </div>
        <div className="pane-b">
          <div className="model-grid">
            {models.map((model) => {
              const draft = drafts[model.modelId] ?? toDraft(model);
              const cost = pricingMap.get(model.modelId);
              return (
                <article
                  key={model.modelId}
                  className={draft.enabled ? "model-card" : "model-card disabled"}
                >
                  <div className="model-card-head">
                    <span className="model-card-id" title={model.modelId}>
                      {model.modelId}
                    </span>
                    <EditableCell {...editableCellProps(model.modelId, "displayName")} />
                    <label className="switch" title={`启用 ${model.modelId}`}>
                      <input
                        type="checkbox"
                        aria-label={`启用 ${model.modelId}`}
                        checked={draft.enabled}
                        onChange={(event) => update(model.modelId, { enabled: event.target.checked })}
                      />
                      <i />
                    </label>
                    <button className="btn btn-ghost btn-sm" type="button" disabled={busy} onClick={() => void save(model.modelId)}>
                      保存
                    </button>
                    <button
                      className="btn btn-danger btn-sm"
                      type="button"
                      disabled={busy}
                      onClick={() => void remove(model.modelId)}
                    >
                      删除
                    </button>
                  </div>
                  <div className="model-card-row">
                    <label className="field" style={{ flex: "1.2 1 150px" }}>
                      <span className="field-label">供应商</span>
                      <Select
                        ariaLabel={`供应商 ${model.modelId}`}
                        style={{ width: "100%" }}
                        value={draft.upstreamProviderId}
                        onChange={(value) => update(model.modelId, { upstreamProviderId: value })}
                        options={[
                          { value: "", label: "默认（env 上游）" },
                          ...providers.map((provider) => ({
                            value: provider.id,
                            label: provider.enabled ? provider.name : `${provider.name}（已停用）`,
                          })),
                        ]}
                      />
                    </label>
                    <label className="field" style={{ flex: "1.4 1 170px" }}>
                      <span className="field-label">上游模型（留空按模型 ID 转发）</span>
                      <EditableCell {...editableCellProps(model.modelId, "upstreamModel")} />
                    </label>
                  </div>
                  <div className="model-card-row">
                    <label className="field">
                      <span className="field-label">售价·入（credit/1M）</span>
                      <EditableCell {...editableCellProps(model.modelId, "inputPricePerMtok", true)} />
                    </label>
                    <label className="field">
                      <span className="field-label">售价·出（credit/1M）</span>
                      <EditableCell {...editableCellProps(model.modelId, "outputPricePerMtok", true)} />
                    </label>
                    <label className="field">
                      <span className="field-label">售价·缓存读</span>
                      <EditableCell {...editableCellProps(model.modelId, "cacheReadPricePerMtok", true)} />
                    </label>
                    <label className="field">
                      <span className="field-label">售价·缓存写</span>
                      <EditableCell {...editableCellProps(model.modelId, "cacheWritePricePerMtok", true)} />
                    </label>
                    <label className="field" style={{ flex: "0.8 1 80px" }}>
                      <span className="field-label">空闲倍率</span>
                      <EditableCell {...editableCellProps(model.modelId, "offPeakMultiplier", true)} />
                    </label>
                    <label className="field" style={{ flex: "0.8 1 64px" }}>
                      <span className="field-label">排序</span>
                      <EditableCell {...editableCellProps(model.modelId, "sortOrder", true)} />
                    </label>
                  </div>
                  <div className="model-card-row">
                    <span className="field-label" style={{ flex: "0 0 auto" }}>
                      成本价（{cost?.currency || "USD（历史）"}，高峰 / 空闲）
                    </span>
                    <span className="readout" style={{ margin: 0 }}>
                      {cost
                        ? `入 ${formatCost(cost.inputCostPerMillion, cost.offPeakMultiplier)} · 出 ${formatCost(cost.outputCostPerMillion, cost.offPeakMultiplier)} · 缓存读 ${formatCost(cost.cacheReadCostPerMillion, cost.offPeakMultiplier)}`
                        : "未在定价库中配置成本价"}
                    </span>
                  </div>
                  <div className="model-card-row" style={{ alignItems: "center" }}>
                    <span className="field-label" style={{ flex: "0 0 auto" }}>
                      客户端能力
                    </span>
                    <span className="readout" style={{ margin: 0 }}>
                      {model.configMode === "manual" && model.config
                        ? `手动 · 上下文 ${formatToken(model.config.properties.contextWindow)} · 最大输出 ${formatToken(model.config.optionSpecs.maxOutputTokens.max)}`
                        : "智能配置 · 跟随官方规则"}
                    </span>
                    <button
                      className="btn btn-ghost btn-sm"
                      type="button"
                      style={{ marginLeft: "auto" }}
                      disabled={busy}
                      onClick={() => setConfigTarget(model)}
                    >
                      编辑模型配置
                    </button>
                  </div>
                </article>
              );
            })}
            {models.length === 0 ? (
              <div className="readout">还没有模型，先添加一个或从上方「从上游导入」批量建档。</div>
            ) : null}
          </div>
        </div>
      </section>

      <section className="pane d2">
        <div className="pane-h">
          <h2>路由池</h2>
          <span className="readout">同一模型多供应商容灾 · priority 小者优先</span>
        </div>
        <div className="pane-b">
          <div className="form-row" style={{ marginBottom: 12 }}>
            <Select
              ariaLabel="选择模型"
              style={{ width: 240 }}
              value={routeModelId}
              onChange={setRouteModelId}
              options={[
                { value: "", label: "选择模型…" },
                ...models.map((model) => ({ value: model.modelId, label: model.displayName })),
              ]}
            />
            <Select
              ariaLabel="选择供应商"
              style={{ width: 190 }}
              value={routeProviderId}
              onChange={setRouteProviderId}
              options={[
                { value: "", label: "选择供应商…" },
                ...providers.map((provider) => ({
                  value: provider.id,
                  label: provider.enabled ? provider.name : `${provider.name}（已停用）`,
                })),
              ]}
            />
            <input
              className="input"
              style={{ width: 190 }}
              value={routeUpstreamModel}
              onChange={(event) => setRouteUpstreamModel(event.target.value)}
              placeholder="上游模型（留空按模型 ID）"
              aria-label="路由上游模型"
            />
            <input
              className="input num"
              style={{ width: 90 }}
              value={routePriority}
              onChange={(event) => setRoutePriority(event.target.value)}
              placeholder="priority"
              aria-label="路由优先级"
            />
            <input
              className="input num"
              style={{ width: 80 }}
              value={routeWeight}
              onChange={(event) => setRouteWeight(event.target.value)}
              placeholder="weight"
              aria-label="路由权重"
            />
            <button
              className="btn"
              type="button"
              disabled={routeBusy || !routeModelId || !routeProviderId}
              onClick={() => void addRoute()}
            >
              添加路由
            </button>
          </div>
          {routeModelId ? (
            <div className="table-scroll">
              <table className="table">
                <thead>
                  <tr>
                    <th scope="col">供应商</th>
                    <th scope="col">上游模型</th>
                    <th scope="col" className="num">优先级</th>
                    <th scope="col" className="num">权重</th>
                    <th scope="col">状态</th>
                    <th scope="col" aria-label="操作" />
                  </tr>
                </thead>
                <tbody>
                  {routes.map((route) => (
                    <tr key={route.id}>
                      <td>{route.providerName}</td>
                      <td>{route.upstreamModel || "（按模型 ID）"}</td>
                      <td className="num">{route.priority}</td>
                      <td className="num">{route.weight}</td>
                      <td>
                        <Chip tone={route.enabled ? "ok" : "mute"}>{route.enabled ? "启用" : "停用"}</Chip>
                      </td>
                      <td>
                        <div className="form-row" style={{ gap: 6 }}>
                          <button
                            className="btn btn-ghost btn-sm"
                            type="button"
                            disabled={routeBusy}
                            onClick={() => void toggleRoute(route)}
                          >
                            {route.enabled ? "停用" : "启用"}
                          </button>
                          <button
                            className="btn btn-ghost btn-sm"
                            type="button"
                            disabled={routeBusy}
                            onClick={() => void deleteRoute(route)}
                          >
                            删除
                          </button>
                        </div>
                      </td>
                    </tr>
                  ))}
                  {routes.length === 0 ? (
                    <tr>
                      <td colSpan={6} className="readout">
                        该模型还没有路由池，网关会回退到「模型清单」里绑定的单一供应商。
                      </td>
                    </tr>
                  ) : null}
                </tbody>
              </table>
            </div>
          ) : (
            <p className="readout" style={{ margin: 0 }}>选择模型后管理其上游路由池。</p>
          )}
        </div>
      </section>

      {configTarget ? (
        <ModelConfigDrawer
          model={configTarget}
          onClose={() => setConfigTarget(null)}
          onSaved={(saved: ModelConfigTarget) => {
            setModels((previous) =>
              previous.map((model) =>
                model.modelId === saved.modelId
                  ? {
                      ...model,
                      configMode: saved.configMode,
                      config: saved.config ?? null,
                    }
                  : model,
              ),
            );
            setConfigTarget(null);
          }}
        />
      ) : null}
    </div>
  );
}
