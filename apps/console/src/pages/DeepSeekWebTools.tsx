import { useEffect, useRef, useState, type FormEvent } from "react";
import { Globe, Play, RefreshCw, Save, Trash2 } from "lucide-react";
import { api } from "../api.js";
import { confirmDialog, toastErr, toastOk } from "../components/Feedback.js";
import { Glyph } from "../components/Icon.js";
import { Select } from "../components/Select.js";
import { Chip, Notice } from "../components/StatusChip.js";

interface ToolState {
  version: string;
  enabled: boolean;
  providerId: string;
  searchEnabled: boolean;
  fetchEnabled: boolean;
  searchSaleMicro: number;
  searchCostMicro: number;
  fetchSaleMicro: number;
  fetchCostMicro: number;
  source: "console" | "none";
  configured: boolean;
  providerName: string;
  providerEnabled: boolean;
  hasKey: boolean;
  canConfigure: boolean;
}
interface Provider { id: string; name: string; enabled: boolean; hasKey: boolean; baseUrl: string }
interface ToolDraft { enabled: boolean; providerId: string; searchEnabled: boolean; fetchEnabled: boolean; searchSale: string; searchCost: string; fetchSale: string; fetchCost: string }
type PriceField = "searchSale" | "searchCost" | "fetchSale" | "fetchCost";

const priceText = (micro: number): string => {
  // 浮点除法会在安全整数上界丢失 micro，金额按定点整数回显。
  const exact = BigInt(micro);
  const absolute = exact < 0n ? -exact : exact;
  const fraction = (absolute % 1_000_000n).toString().padStart(6, "0").replace(/0+$/u, "");
  return `${exact < 0n ? "-" : ""}${absolute / 1_000_000n}${fraction ? `.${fraction}` : ""}`;
};
function parsePrice(value: string): number | undefined {
  const normalized = value.trim();
  if (!/^(?:0|[1-9]\d*)(?:\.\d{1,6})?$/u.test(normalized)) return undefined;
  const [whole, fraction = ""] = normalized.split(".");
  const micro = BigInt(whole) * 1_000_000n + BigInt(fraction.padEnd(6, "0"));
  return micro <= BigInt(Number.MAX_SAFE_INTEGER) ? Number(micro) : undefined;
}
function makeDraft(state: ToolState): ToolDraft {
  return { enabled: state.enabled, providerId: state.providerId, searchEnabled: state.searchEnabled, fetchEnabled: state.fetchEnabled, searchSale: priceText(state.searchSaleMicro), searchCost: priceText(state.searchCostMicro), fetchSale: priceText(state.fetchSaleMicro), fetchCost: priceText(state.fetchCostMicro) };
}

export function DeepSeekWebToolsPanel({ providers }: { providers: readonly Provider[] }) {
  const [state, setState] = useState<ToolState | null>(null);
  const [draft, setDraft] = useState<ToolDraft | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState<"save" | "clear" | "test" | "reload" | null>(null);
  const pending = useRef(false);
  const apply = (next: ToolState): void => { setState(next); setDraft(makeDraft(next)); };
  useEffect(() => {
    let active = true;
    api<ToolState>("/deepseek-web-tools").then((next) => { if (active) apply(next); }, (reason: unknown) => { if (active) setError(reason instanceof Error ? reason.message : "读取网页工具配置失败"); });
    return () => { active = false; };
  }, []);
  const reload = async (): Promise<void> => {
    if (pending.current) return;
    pending.current = true; setBusy("reload");
    try {
      apply(await api<ToolState>("/deepseek-web-tools"));
      setError("");
    } catch (reason) { setError(reason instanceof Error ? reason.message : "读取网页工具配置失败"); }
    finally { pending.current = false; setBusy(null); }
  };

  const save = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    if (!state || !draft || pending.current) return;
    const prices = [draft.searchSale, draft.searchCost, draft.fetchSale, draft.fetchCost].map(parsePrice);
    if (prices.some((price) => price === undefined)) { setError("价格须为非负金额，最多六位小数。"); return; }
    pending.current = true; setBusy("save"); setError("");
    try {
      apply(await api<ToolState>("/deepseek-web-tools", { method: "PUT", body: JSON.stringify({ expectedVersion: state.version, enabled: draft.enabled, providerId: draft.providerId, searchEnabled: draft.searchEnabled, fetchEnabled: draft.fetchEnabled, searchSaleMicro: prices[0], searchCostMicro: prices[1], fetchSaleMicro: prices[2], fetchCostMicro: prices[3] }) }));
      toastOk("新请求使用已保存的工具配置，在途请求保留原价", "已保存");
    } catch (reason) { setError(reason instanceof Error ? reason.message : "保存失败"); }
    finally { pending.current = false; setBusy(null); }
  };
  const clear = async (): Promise<void> => {
    if (!state || pending.current) return;
    pending.current = true; setBusy("clear");
    try {
      const answer = await confirmDialog({ k: "网页工具", title: "停用并清除工具绑定？", message: "新的网页搜索与抓取将停用；供应商及其密钥保留。在途请求仍按原配置结算。", confirmText: "停用并清除", danger: true });
      if (!answer.confirmed) return;
      apply(await api<ToolState>("/deepseek-web-tools", { method: "DELETE", body: JSON.stringify({ expectedVersion: state.version }) }));
      setError(""); toastOk("网页搜索与抓取已停用");
    } catch (reason) { setError(reason instanceof Error ? reason.message : "停用失败"); }
    finally { pending.current = false; setBusy(null); }
  };
  const test = async (): Promise<void> => {
    if (!draft?.providerId || pending.current) return;
    pending.current = true; setBusy("test");
    try {
      const result = await api<{ ok: boolean; status: number; latencyMs: number }>("/deepseek-web-tools/test", { method: "POST", body: JSON.stringify({ providerId: draft.providerId }) });
      if (result.ok) toastOk("连通与鉴权通过；此测试不验证 Search/Fetch 权限", "测试通过");
      else toastErr(result.status ? `供应商测试失败（HTTP ${result.status}）` : "供应商连接失败", "测试失败");
    } catch (reason) { toastErr(reason instanceof Error ? reason.message : "测试失败"); }
    finally { pending.current = false; setBusy(null); }
  };

  if (!state || !draft) return <section className="pane d1"><Notice tone={error ? "stop" : undefined} k="网页搜索与抓取">{error || "正在读取工具配置…"}</Notice>{error ? <button className="btn btn-ghost" type="button" disabled={busy !== null} onClick={() => void reload()}><Glyph icon={RefreshCw} size={14} />重新载入</button> : null}</section>;
  const currentProvider = providers.find((provider) => provider.id === state.providerId);
  const selectedProvider = providers.find((provider) => provider.id === draft.providerId);
  const configured = state.configured && currentProvider?.enabled === true && currentProvider.hasKey;
  const invalidProvider = draft.providerId !== "" && (!selectedProvider || !selectedProvider.enabled || !selectedProvider.hasKey);
  const priceInput = (field: PriceField, label: string) => <label className="field"><span className="field-label">{label}</span><input className="input num" aria-label={label} inputMode="decimal" value={draft[field]} disabled={busy !== null} onChange={(event) => setDraft({ ...draft, [field]: event.target.value })} /></label>;
  const margin = (sale: string, cost: string): string => { const a = parsePrice(sale); const b = parsePrice(cost); return a === undefined || b === undefined ? "—" : `¥${priceText(a - b)} / 次`; };
  const providerOptions = [{ value: "", label: "选择已配置密钥的工具供应商…" }, ...providers.map((provider) => ({ value: provider.id, label: `${provider.name}${!provider.enabled ? "（已停用）" : !provider.hasKey ? "（未配置密钥）" : ""}` }))];
  if (draft.providerId && !providers.some((provider) => provider.id === draft.providerId)) providerOptions.push({ value: draft.providerId, label: state.providerName || "原供应商已删除，请重新选择" });
  return <section className="pane d1" aria-label="DeepSeek 网页搜索与抓取配置">
    <div className="pane-h"><h2><Glyph icon={Globe} size={16} /> 网页搜索与抓取 · DeepSeek</h2><Chip tone={configured ? "ok" : state.enabled ? "stop" : "mute"}>{configured ? "生效" : state.enabled ? "配置不可用" : "已停用"}</Chip></div>
    <div className="pane-b">
      <p className="readout">复用所选上游供应商的加密密钥。客户端仅持有平台设备凭据，由网关调用工具并按成功请求扣点；密钥在供应商表单中维护。</p>
      {state.source === "none" ? <Notice k="尚未保存后台配置">旧部署环境配置在首次保存前继续生效；保存后只使用本面板，停用不会回退环境配置。</Notice> : null}
      {error ? <Notice tone="stop" k="工具配置">{error}</Notice> : null}
      {invalidProvider ? <Notice tone="stop" k="工具供应商不可用">所选供应商已停用、已删除或未配置密钥。请先更新供应商或重新选择，网关不会回退到其它供应商。</Notice> : null}
      {!state.canConfigure ? <Notice k="缺少加密配置">请先为 API 与网关配置相同的 PLATFORM_SECRET_KEY，再绑定供应商密钥。</Notice> : null}
      <form onSubmit={(event) => void save(event)}>
        <div className="form-row" style={{ alignItems: "end", marginBottom: 16 }}>
          <label className="field grow"><span className="field-label">工具供应商</span><Select ariaLabel="网页工具供应商" options={providerOptions} value={draft.providerId} onChange={(providerId) => setDraft({ ...draft, providerId })} disabled={busy !== null} /></label>
          <label className="form-row"><input type="checkbox" checked={draft.enabled} disabled={busy !== null} onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })} />启用网页工具</label>
          <button className="btn btn-ghost" type="button" disabled={!draft.providerId || busy !== null} onClick={() => void test()}><Glyph icon={Play} size={14} />{busy === "test" ? "测试中…" : "测试连通与鉴权"}</button>
        </div>
        <div className="table-scroll"><table className="table"><thead><tr><th scope="col">操作</th><th scope="col">启用</th><th scope="col">用户售价 · credit/次</th><th scope="col">采购成本 · ¥/次</th><th scope="col">单次毛利（估算）</th></tr></thead><tbody>
          <tr><td className="name">网页搜索 Search</td><td><input type="checkbox" aria-label="启用网页搜索" checked={draft.searchEnabled} disabled={busy !== null} onChange={(event) => setDraft({ ...draft, searchEnabled: event.target.checked })} /></td><td>{priceInput("searchSale", "搜索售价")}</td><td>{priceInput("searchCost", "搜索采购成本")}</td><td className="num">{margin(draft.searchSale, draft.searchCost)}</td></tr>
          <tr><td className="name">网页抓取 Fetch</td><td><input type="checkbox" aria-label="启用网页抓取" checked={draft.fetchEnabled} disabled={busy !== null} onChange={(event) => setDraft({ ...draft, fetchEnabled: event.target.checked })} /></td><td>{priceInput("fetchSale", "抓取售价")}</td><td>{priceInput("fetchCost", "抓取采购成本")}</td><td className="num">{margin(draft.fetchSale, draft.fetchCost)}</td></tr>
        </tbody></table></div>
        <p className="readout">1 credit = ¥1。仅上游成功且有结果计费，失败及空结果不扣点；结果条数、正文长度与 Token 数不影响单次价格。后续模型分析另按模型用量结算。保存仅影响新请求，历史价格快照不变。</p>
        <div className="form-row"><button className="btn" type="submit" disabled={busy !== null || invalidProvider || (draft.enabled && (!draft.providerId || !state.canConfigure))}><Glyph icon={Save} size={14} />{busy === "save" ? "保存中…" : "保存工具配置"}</button><button className="btn btn-danger" type="button" disabled={busy !== null} onClick={() => void clear()}><Glyph icon={Trash2} size={14} />停用并清除绑定</button><button className="btn btn-ghost" type="button" disabled={busy !== null} onClick={() => void reload()}><Glyph icon={RefreshCw} size={14} />{busy === "reload" ? "载入中…" : "重新载入（覆盖草稿）"}</button><span className="readout">连通测试不产生 Search/Fetch 费用，不验证付费工具权限。</span></div>
      </form>
    </div>
  </section>;
}
