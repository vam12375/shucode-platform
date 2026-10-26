import { useEffect, useState } from "react";
import { api } from "../api.js";
import { confirmDialog, toastErr, toastOk } from "./Feedback.js";
import { Select } from "./Select.js";
import { Chip, Notice } from "./StatusChip.js";

interface Provider { id: string; name: string; enabled: boolean }
interface Route {
  id: string; modelId: string; providerId: string; providerName: string;
  providerEnabled?: boolean; upstreamModel: string; priority: number; weight: number; enabled: boolean;
}
interface Draft { upstreamModel: string; priority: string; weight: string }

export function routeInteger(value: string, min: number, max: number): number | null {
  if (!/^\d+$/.test(value.trim())) return null;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed >= min && parsed <= max ? parsed : null;
}

// 每个模型独立挂载，旧请求只更新旧实例；切换模型后不能用上一模型的路由误操作。
export function ModelRoutePool({ modelId, upstreamModel, providers }: {
  modelId: string; upstreamModel: string; providers: Provider[];
}) {
  const [routes, setRoutes] = useState<Route[]>([]);
  const [drafts, setDrafts] = useState<Record<string, Draft>>({});
  const [providerId, setProviderId] = useState("");
  const [upstream, setUpstream] = useState("");
  const [priority, setPriority] = useState("100");
  const [weight, setWeight] = useState("1");
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [revision, setRevision] = useState(0);

  useEffect(() => {
    let current = true;
    setLoading(true);
    void api<{ routes: Route[] }>(`/models/${encodeURIComponent(modelId)}/routes`).then((data) => {
      if (!current) return;
      setRoutes(data.routes);
      setDrafts(Object.fromEntries(data.routes.map((route) => [route.id, {
        upstreamModel: route.upstreamModel, priority: String(route.priority), weight: String(route.weight),
      }])));
      setError(null);
    }).catch((cause: unknown) => {
      if (current) setError(cause instanceof Error ? cause.message : "路由加载失败");
    }).finally(() => { if (current) setLoading(false); });
    return () => { current = false; };
  }, [modelId, revision]);

  const write = async (path: string, method: string, body?: unknown): Promise<void> => {
    setBusy(true);
    try {
      await api(path, { method, ...(body !== undefined ? { body: JSON.stringify(body) } : {}) });
      setRevision((value) => value + 1);
      toastOk("路由配置已保存", "已保存");
    } catch (cause) {
      toastErr(cause instanceof Error ? cause.message : "路由保存失败");
    } finally { setBusy(false); }
  };
  const numbers = (draft: Pick<Draft, "priority" | "weight">) => {
    const parsedPriority = routeInteger(draft.priority, 0, 1_000_000);
    const parsedWeight = routeInteger(draft.weight, 1, 10_000);
    if (parsedPriority === null || parsedWeight === null) {
      toastErr("优先级必须是 0–1000000 的整数，权重必须是 1–10000 的整数。");
      return null;
    }
    return { priority: parsedPriority, weight: parsedWeight };
  };
  const add = async () => {
    const parsed = numbers({ priority, weight });
    if (!providerId || !parsed) return;
    await write(`/models/${encodeURIComponent(modelId)}/routes`, "POST", {
      providerId, upstreamModel: upstream.trim(), ...parsed,
    });
  };
  const available = providers.filter((provider) => !routes.some((route) => route.providerId === provider.id));
  const unavailable = routes.length > 0 && !routes.some((route) => route.enabled &&
    route.providerEnabled !== false && providers.some((provider) => provider.id === route.providerId && provider.enabled));
  const disabled = loading || busy || error !== null;
  const patchDraft = (id: string, patch: Partial<Draft>) =>
    setDrafts((previous) => ({ ...previous, [id]: { ...previous[id], ...patch } }));

  return <>
    <p className="readout">优先级越小越先调用；同优先级按权重轮转（1:1 均分，2:1 约为两倍请求）。主组失败时再尝试备用组。</p>
    <p className="readout">配置路由池后仅使用池内供应商；全部停用时拒绝新请求。上游模型留空继承模型绑定：{upstreamModel || modelId}。</p>
    {unavailable && !loading ? <Notice k="没有可用路由">请启用至少一条路由及其供应商；当前不会回退到模型的单一供应商绑定。</Notice> : null}
    {error ? <Notice tone="stop" k="路由加载失败">{error} <button className="btn btn-ghost btn-sm" type="button" onClick={() => setRevision((value) => value + 1)}>重新加载路由</button></Notice> : null}
    <fieldset disabled={disabled} style={{ border: 0, padding: 0, margin: "12px 0" }}>
      <div className="form-row">
        <label className="field"><span className="field-label">供应商</span><Select
          ariaLabel="路由供应商" disabled={disabled} value={providerId} onChange={setProviderId}
          options={[{ value: "", label: "选择未绑定的供应商…" }, ...available.map((provider) => ({ value: provider.id, label: provider.enabled ? provider.name : `${provider.name}（已停用）` }))]}
        /></label>
        <label className="field"><span className="field-label">上游模型</span><input className="input" aria-label="路由上游模型" placeholder={`留空继承 ${upstreamModel || modelId}`} value={upstream} onChange={(event) => setUpstream(event.target.value)} maxLength={200} /></label>
        <label className="field"><span className="field-label">优先级 · 小者优先</span><input className="input num" style={{ width: 130 }} inputMode="numeric" aria-label="路由优先级" value={priority} onChange={(event) => setPriority(event.target.value)} /></label>
        <label className="field"><span className="field-label">权重 · 同级分流</span><input className="input num" style={{ width: 120 }} inputMode="numeric" aria-label="路由权重" value={weight} onChange={(event) => setWeight(event.target.value)} /></label>
        <button className="btn" type="button" disabled={disabled || !available.some((provider) => provider.id === providerId)} onClick={() => void add()}>添加路由</button>
      </div>
    </fieldset>
    {loading ? <p className="readout" role="status">正在加载路由…</p> : !error ? <div className="table-scroll"><table className="table" style={{ minWidth: 1000, whiteSpace: "nowrap" }}><thead><tr>
      <th scope="col">供应商</th><th scope="col">上游模型</th><th scope="col">优先级</th><th scope="col">权重</th><th scope="col">状态</th><th scope="col">操作</th>
    </tr></thead><tbody>{routes.map((route) => {
      const draft = drafts[route.id];
      if (!draft) return null;
      const providerEnabled = route.providerEnabled ?? providers.find((provider) => provider.id === route.providerId)?.enabled ?? false;
      return <tr key={route.id}>
        <td>{route.providerName}</td>
        <td><input className="input" aria-label={`${route.providerName} 上游模型`} value={draft.upstreamModel} disabled={disabled} placeholder={upstreamModel || modelId} maxLength={200} onChange={(event) => patchDraft(route.id, { upstreamModel: event.target.value })} /></td>
        <td><input className="input num" style={{ width: 110 }} inputMode="numeric" aria-label={`${route.providerName} 优先级`} value={draft.priority} disabled={disabled} onChange={(event) => patchDraft(route.id, { priority: event.target.value })} /></td>
        <td><input className="input num" style={{ width: 90 }} inputMode="numeric" aria-label={`${route.providerName} 权重`} value={draft.weight} disabled={disabled} onChange={(event) => patchDraft(route.id, { weight: event.target.value })} /></td>
        <td><Chip tone={!providerEnabled ? "stop" : route.enabled ? "ok" : "mute"}>{!providerEnabled ? "供应商已停用" : route.enabled ? "参与轮转" : "路由已停用"}</Chip></td>
        <td><div className="form-row" style={{ gap: 6 }}>
          <button className="btn btn-ghost btn-sm" type="button" disabled={disabled} onClick={() => {
            const parsed = numbers(draft);
            if (parsed) void write(`/routes/${route.id}`, "PATCH", { upstreamModel: draft.upstreamModel.trim(), ...parsed });
          }}>保存路由</button>
          <button className="btn btn-ghost btn-sm" type="button" disabled={disabled} onClick={() => void write(`/routes/${route.id}`, "PATCH", { enabled: !route.enabled })}>{route.enabled ? "停用" : "启用"}</button>
          <button className="btn btn-ghost btn-sm" type="button" disabled={disabled} onClick={() => void (async () => {
            const answer = await confirmDialog({ k: "删除路由", title: `删除 ${route.providerName} 的路由？`, message: routes.length === 1 ? "删除最后一条路由后恢复模型的单一供应商绑定。" : "删除后该供应商不再参与此模型的调度。", confirmText: "删除", danger: true });
            if (answer.confirmed) await write(`/routes/${route.id}`, "DELETE");
          })()}>删除</button>
        </div></td>
      </tr>;
    })}{routes.length === 0 ? <tr><td colSpan={6} className="readout">尚未配置路由池，当前使用模型的单一供应商绑定。</td></tr> : null}</tbody></table></div> : null}
  </>;
}
