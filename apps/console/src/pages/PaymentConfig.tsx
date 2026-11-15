import { useCallback, useEffect, useRef, useState } from "react";
import { Settings2 } from "lucide-react";
import { api } from "../api.js";
import { toastOk } from "../components/Feedback.js";
import { Glyph } from "../components/Icon.js";
import { Select } from "../components/Select.js";
import { Chip, Notice } from "../components/StatusChip.js";

interface ConfigData {
  enabled: boolean;
  appId: string;
  sellerId: string;
  gateway: "production" | "sandbox";
  privateKeyConfigured: boolean;
  publicKeyConfigured: boolean;
  notifyUrl: string;
  returnUrl: string;
}
export default function PaymentConfigPanel({ onSaved }: { onSaved?: () => void } = {}) {
  const [config, setConfig] = useState<ConfigData | null>(null);
  const [appId, setAppId] = useState("");
  const [sellerId, setSellerId] = useState("");
  const [gateway, setGateway] = useState<ConfigData["gateway"]>("production");
  const [enabled, setEnabled] = useState(false);
  const [privateKey, setPrivateKey] = useState("");
  const [publicKey, setPublicKey] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const pending = useRef(false);
  const active = useRef(true);
  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const next = await api<ConfigData>("/payments/config", { signal });
      if (!active.current || signal?.aborted) return;
      setConfig(next); setAppId(next.appId); setSellerId(next.sellerId); setGateway(next.gateway); setEnabled(next.enabled); setError("");
    } catch (reason) { if (active.current && !signal?.aborted) setError(reason instanceof Error ? reason.message : "加载支付宝配置失败"); }
  }, []);
  useEffect(() => { active.current = true; const controller = new AbortController(); void load(controller.signal); return () => { active.current = false; controller.abort(); }; }, [load]);
  const save = async (): Promise<void> => {
    if (!config || pending.current) return;
    pending.current = true; setBusy(true); setError("");
    try {
      const next = await api<ConfigData>("/payments/config", { method: "PUT", body: JSON.stringify({ enabled, appId: appId.trim(), sellerId: sellerId.trim(), gateway, ...(privateKey.trim() ? { privateKey: privateKey.trim() } : {}), ...(publicKey.trim() ? { publicKey: publicKey.trim() } : {}) }) });
      if (!active.current) return;
      setConfig(next); setAppId(next.appId); setSellerId(next.sellerId); setGateway(next.gateway); setEnabled(next.enabled);
      setPrivateKey(""); setPublicKey(""); toastOk("支付宝配置已保存"); onSaved?.();
    } catch (reason) { if (active.current) setError(reason instanceof Error ? reason.message : "保存支付宝配置失败"); }
    finally { pending.current = false; if (active.current) setBusy(false); }
  };
  return (
    <section className="pane d1">
      <div className="pane-h"><h2><Glyph icon={Settings2} size={16} /> 支付宝接入</h2>{config ? <Chip tone={config.enabled ? "ok" : "mute"}>{config.enabled ? "已启用" : "已停用"}</Chip> : null}</div>
      <div className="pane-b">
        {error ? <Notice tone="stop" k="支付宝配置">{error}</Notice> : null}
        {config ? <>
          <div className="form-row">
            <label className="field grow"><span className="field-label">App ID</span><input className="input" value={appId} onChange={(event) => setAppId(event.target.value)} disabled={busy} /></label>
            <label className="field grow"><span className="field-label">商户号 Seller ID</span><input className="input" value={sellerId} onChange={(event) => setSellerId(event.target.value)} disabled={busy} /></label>
            <label className="form-row"><input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} disabled={busy} />允许新充值</label>
          </div>
          <div className="form-row">
            <label className="field"><span className="field-label">网关环境</span><Select ariaLabel="支付宝网关环境" value={gateway} onChange={(value) => setGateway(value as ConfigData["gateway"])} options={[{ value: "production", label: "正式环境" }, { value: "sandbox", label: "沙箱环境" }]} disabled={busy} /></label>
            <span className="readout">私钥：{config.privateKeyConfigured ? "已配置（不会回显）" : "未配置"}</span>
            <span className="readout">支付宝公钥：{config.publicKeyConfigured ? "已配置（不会回显）" : "未配置"}</span>
          </div>
          <div className="form-row">
            <label className="field grow"><span className="field-label">重新写入商户私钥（可留空保留）</span><textarea className="input" rows={3} value={privateKey} onChange={(event) => setPrivateKey(event.target.value)} disabled={busy} placeholder="留空表示保留已保存密钥" /></label>
            <label className="field grow"><span className="field-label">重新写入支付宝公钥（可留空保留）</span><textarea className="input" rows={3} value={publicKey} onChange={(event) => setPublicKey(event.target.value)} disabled={busy} placeholder="留空表示保留已保存密钥" /></label>
          </div>
          <p className="readout">通知地址：{config.notifyUrl || "由服务端配置"}<br />返回地址：{config.returnUrl || "由服务端配置"}</p>
          <div className="form-row"><button className="btn" type="button" disabled={busy} onClick={() => void save()}>{busy ? "保存中…" : "保存支付宝配置"}</button><button className="btn btn-ghost" type="button" disabled={busy} onClick={() => void load()}>重新读取配置</button></div>
        </> : error ? <button className="btn btn-ghost" type="button" onClick={() => void load()}>重试读取支付配置</button> : <Notice k="支付宝配置">正在读取配置…</Notice>}
      </div>
    </section>
  );
}
