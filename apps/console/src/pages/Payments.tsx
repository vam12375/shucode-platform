import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ExternalLink, HandCoins, RefreshCw, Settings2 } from "lucide-react";
import { api } from "../api.js";
import { useAuth } from "../auth.js";
import { toastOk } from "../components/Feedback.js";
import { Glyph, PageTitle } from "../components/Icon.js";
import { Select } from "../components/Select.js";
import { Chip, Notice } from "../components/StatusChip.js";
import { alipayURL, paymentAmount, rechargeTeams, restorePaymentAttempt, sameAttempt, type PaymentAttempt } from "../payments.js";
import { formatShanghaiTime } from "../time.js";

type AccountMode = "personal" | "team";
type TargetType = AccountMode;
type PaymentStatus = "pending" | "paid" | "closed";
interface AccountData {
  mode: AccountMode;
  personal: { id: string; name: string; quotaCredits: string; usedCredits: string; balanceCredits: string } | null;
  team?: { id: string; name: string; quotaCredits: string; usedCredits: string; balanceCredits: string };
  enabled: boolean;
}
interface PaymentOrder {
  id: string;
  outTradeNo: string;
  targetType: TargetType;
  targetId: string;
  teamId?: string;
  userId: string;
  amountYuan: string;
  creditAmount: string;
  status: PaymentStatus;
  testPayment: boolean;
  tradeNo?: string;
  createdAt: string;
  expiresAt: string;
  paidAt: string | null;
}
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

const statusText: Record<PaymentStatus, { label: string; tone: "info" | "ok" | "mute" }> = {
  pending: { label: "待付款 / 待确认", tone: "info" },
  paid: { label: "已支付并入账", tone: "ok" },
  closed: { label: "已关闭", tone: "mute" },
};

function OrderRow({ order, onReconcile, disabled }: { order: PaymentOrder; onReconcile: (order: PaymentOrder) => void; disabled: boolean }) {
  const status = statusText[order.status];
  return (
    <tr>
      <td className="readout payments-order-id">{order.outTradeNo}{order.testPayment ? <div><Chip tone="info">0.01 元测试</Chip></div> : null}</td>
      <td className="num">{order.amountYuan} 元</td>
      <td className="num">+{order.creditAmount} credit</td>
      <td>{order.targetType === "personal" ? "个人账户" : "团队共享"}</td>
      <td><Chip tone={status.tone}>{status.label}</Chip></td>
      <td className="readout">{formatShanghaiTime(order.createdAt)}</td>
      <td>
        {order.status === "pending" ? (
          <button className="btn btn-ghost btn-sm" type="button" disabled={disabled} onClick={() => onReconcile(order)}>
            <Glyph icon={RefreshCw} size={13} /> 查询
          </button>
        ) : order.tradeNo ? <span className="readout">{order.tradeNo}</span> : "—"}
      </td>
    </tr>
  );
}

export function PaymentConfigPanel({ onSaved }: { onSaved?: () => void } = {}) {
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

function sessionRead(key: string): string | null {
  try { return sessionStorage.getItem(key); } catch { return null; }
}
function sessionWrite(key: string, value: string | null): void {
  try { if (value === null) sessionStorage.removeItem(key); else sessionStorage.setItem(key, value); } catch { /* 受限浏览器仍可从服务端订单列表恢复。 */ }
}

export function PaymentsPage() {
  const { user, teams, team, memberships, isRoot, refreshSession } = useAuth();
  const manageableTeams = useMemo(() => rechargeTeams(teams, memberships, isRoot), [isRoot, memberships, teams]);
  const attemptKey = "mendao:payments:v1:" + user.id + ":attempt";
  const orderKey = "mendao:payments:v1:" + user.id + ":order";
  const [restoredAttempt] = useState(() => restorePaymentAttempt(sessionRead(attemptKey)));
  const attemptRef = useRef<PaymentAttempt | null>(restoredAttempt);
  const [account, setAccount] = useState<AccountData | null>(null);
  const [targetType, setTargetType] = useState<TargetType>(() => restoredAttempt?.targetType ?? "personal");
  const [targetTeamId, setTargetTeamId] = useState(() => restoredAttempt?.teamId ?? manageableTeams[0]?.id ?? "");
  const [amountYuan, setAmountYuan] = useState(() => restoredAttempt?.amountYuan ?? "10.00");
  const [mobile, setMobile] = useState(() => restoredAttempt?.mobile ?? window.matchMedia("(max-width: 640px)").matches);
  const [orders, setOrders] = useState<PaymentOrder[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [selectedOrder, setSelectedOrder] = useState<PaymentOrder | null>(null);
  const [accountError, setAccountError] = useState("");
  const [ordersError, setOrdersError] = useState("");
  const [orderError, setOrderError] = useState("");
  const [draftError, setDraftError] = useState("");
  const [busy, setBusy] = useState<"load" | "order" | "reconcile" | "mode" | "more" | null>(null);
  const [orderLoading, setOrderLoading] = useState(true);
  const pending = useRef(false);
  const mounted = useRef(true);
  const ordersVersion = useRef(0);
  const orderQuery = new URLSearchParams({ targetType });
  if (targetType === "team") orderQuery.set("teamId", targetTeamId);
  const ordersPath = "/payments/orders?" + orderQuery.toString();
  const selectedNo = selectedOrder?.outTradeNo ?? "";
  const selectedStatus = selectedOrder?.status ?? "";

  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);

  const loadAccount = useCallback(async (signal?: AbortSignal): Promise<boolean> => {
    try {
      const data = await api<AccountData>("/payments/account", { signal });
      if (!mounted.current || signal?.aborted) return false;
      setAccount(data); setAccountError(""); return true;
    } catch (reason) {
      if (mounted.current && !signal?.aborted) setAccountError(reason instanceof Error ? reason.message : "读取账户余额失败");
      return false;
    }
  }, []);

  const loadOrders = useCallback(async (signal?: AbortSignal, cursor?: string): Promise<void> => {
    const version = ++ordersVersion.current;
    if (targetType === "team" && !targetTeamId) { setOrders([]); setNextCursor(null); setOrderLoading(false); return; }
    if (!cursor) setOrderLoading(true);
    try {
      const data = await api<{ orders: PaymentOrder[]; nextCursor: string | null }>(ordersPath + (cursor ? "&cursor=" + encodeURIComponent(cursor) : ""), { signal });
      if (!mounted.current || signal?.aborted || version !== ordersVersion.current) return;
      setOrders((old) => {
        const fresh = data.orders.map((item) => old.find((existing) => existing.id === item.id && existing.status === "paid") ?? item);
        return cursor ? [...old, ...fresh.filter((item) => !old.some((existing) => existing.id === item.id))] : fresh;
      });
      setNextCursor(data.nextCursor); setOrdersError("");
    } catch (reason) {
      if (mounted.current && !signal?.aborted && version === ordersVersion.current) setOrdersError(reason instanceof Error ? reason.message : "读取充值记录失败");
    } finally { if (mounted.current && !signal?.aborted && version === ordersVersion.current) setOrderLoading(false); }
  }, [ordersPath, targetTeamId, targetType]);

  const applyOrder = useCallback((next: PaymentOrder): void => {
    if (!mounted.current) return;
    setSelectedOrder((old) => old?.outTradeNo === next.outTradeNo && old.status === "paid" && next.status !== "paid" ? old : next);
    setOrders((old) => old.map((item) => item.outTradeNo === next.outTradeNo && item.status !== "paid" ? next : item));
    sessionWrite(orderKey, next.outTradeNo); setOrderError("");
    if (next.status === "paid" || next.status === "closed") { attemptRef.current = null; sessionWrite(attemptKey, null); }
  }, [attemptKey, orderKey]);

  useEffect(() => {
    const controller = new AbortController();
    void loadAccount(controller.signal);
    return () => controller.abort();
  }, [loadAccount]);
  useEffect(() => {
    const controller = new AbortController();
    setOrders([]); setNextCursor(null); setOrdersError("");
    void loadOrders(controller.signal);
    return () => controller.abort();
  }, [loadOrders]);
  useEffect(() => {
    const orderNo = sessionRead(orderKey);
    if (!orderNo) return;
    const controller = new AbortController();
    void api<{ order: PaymentOrder }>("/payments/orders/" + encodeURIComponent(orderNo), { signal: controller.signal }).then((data) => {
      if (!controller.signal.aborted) applyOrder(data.order);
    }, (reason: unknown) => {
      if (!controller.signal.aborted) setOrderError(reason instanceof Error ? reason.message : "恢复支付订单失败，请从充值记录查询");
    });
    return () => controller.abort();
  }, [applyOrder, orderKey]);

  useEffect(() => {
    if (!selectedNo || selectedStatus !== "pending") return;
    let stopped = false;
    let timer: number | undefined;
    let controller: AbortController | undefined;
    const stopRequest = (): void => { if (timer !== undefined) window.clearTimeout(timer); controller?.abort(); };
    const poll = async (): Promise<void> => {
      if (stopped || document.visibilityState !== "visible") return;
      const requestController = new AbortController();
      controller = requestController;
      try {
        const data = await api<{ order: PaymentOrder }>("/payments/orders/" + encodeURIComponent(selectedNo), { signal: requestController.signal });
        if (stopped || requestController.signal.aborted) return;
        applyOrder(data.order);
        if (data.order.status === "paid") { void loadAccount(); void loadOrders(); void refreshSession().catch(() => setAccountError("已确认入账，但团队余额刷新失败，请重新载入")); }
        if (data.order.status !== "pending") return;
      } catch (reason) {
        if (stopped || requestController.signal.aborted) return;
        setOrderError(reason instanceof Error ? reason.message : "订单状态更新失败，请手动查询");
      }
      if (!stopped && document.visibilityState === "visible") timer = window.setTimeout(() => void poll(), 10000);
    };
    const onVisibility = (): void => { stopRequest(); if (document.visibilityState === "visible") void poll(); };
    document.addEventListener("visibilitychange", onVisibility);
    if (document.visibilityState === "visible") timer = window.setTimeout(() => void poll(), 10000);
    return () => { stopped = true; stopRequest(); document.removeEventListener("visibilitychange", onVisibility); };
  }, [applyOrder, loadAccount, loadOrders, refreshSession, selectedNo, selectedStatus]);

  const refresh = async (): Promise<void> => {
    if (pending.current) return;
    pending.current = true; setBusy("load");
    try { await Promise.all([loadAccount(), loadOrders(), refreshSession().catch(() => setAccountError("团队余额刷新失败，请稍后重试"))]); }
    finally { pending.current = false; if (mounted.current) setBusy(null); }
  };
  const saveMode = async (next: AccountMode): Promise<void> => {
    if (pending.current || !account || next === account.mode) return;
    pending.current = true; setBusy("mode"); setDraftError("");
    try {
      await api("/payments/account", { method: "PUT", body: JSON.stringify({ mode: next }) });
      if (!mounted.current) return;
      await loadAccount(); await refreshSession(); toastOk("已保存消费账户归属");
    } catch (reason) { if (mounted.current) setDraftError(reason instanceof Error ? reason.message : "切换消费账户失败"); }
    finally { pending.current = false; if (mounted.current) setBusy(null); }
  };
  const createOrder = async (testPayment = false): Promise<void> => {
    if (pending.current || !account?.enabled) return;
    const amount = testPayment && isRoot ? "0.01" : paymentAmount(amountYuan);
    if (!amount || (testPayment && !isRoot)) { setDraftError("充值金额为 1–100000 元，最多两位小数"); return; }
    if (targetType === "team" && !manageableTeams.some((item) => item.id === targetTeamId)) { setDraftError("请选择你有管理权限的有效团队"); return; }
    pending.current = true; setBusy("order"); setDraftError("");
    try {
      const draft: Omit<PaymentAttempt, "requestId"> = { targetType, ...(targetType === "team" ? { teamId: targetTeamId } : {}), amountYuan: amount, testPayment, mobile };
      const attempt = attemptRef.current && sameAttempt(attemptRef.current, draft) ? attemptRef.current : { ...draft, requestId: crypto.randomUUID() };
      attemptRef.current = attempt; sessionWrite(attemptKey, JSON.stringify(attempt));
      const data = await api<{ order: PaymentOrder; payUrl: string }>("/payments/orders", { method: "POST", body: JSON.stringify(attempt) });
      if (!mounted.current) return;
      applyOrder(data.order);
      if (data.order.status === "paid") { await loadAccount(); await loadOrders(); return; }
      if (data.order.status !== "pending") { setDraftError("该次订单已关闭。修改充值金额后可创建新订单；历史订单可继续查询。"); return; }
      // 只允许跳转支付宝官方网关，避免服务端或代理返回异常URL将用户带离可信收银台。
      const url = alipayURL(data.payUrl);
      window.location.assign(url);
    } catch (reason) {
      if (mounted.current) setDraftError((reason instanceof Error ? reason.message : "创建支付订单失败") + "；当前草稿的请求编号已保留，重试将核对同一次订单。");
    } finally { pending.current = false; if (mounted.current) setBusy(null); }
  };
  const reconcile = async (order: PaymentOrder): Promise<void> => {
    if (pending.current) return;
    pending.current = true; setBusy("reconcile"); setOrderError("");
    try {
      const data = await api<{ order: PaymentOrder }>("/payments/orders/" + encodeURIComponent(order.outTradeNo) + "/reconcile", { method: "POST" });
      if (!mounted.current) return;
      applyOrder(data.order);
      await Promise.all([loadAccount(), loadOrders(), refreshSession().catch(() => setAccountError("已获取订单状态，但团队余额刷新失败，请重新载入"))]);
      if (data.order.status === "paid") toastOk("支付宝付款已确认，款项已入账");
      else if (data.order.status === "closed") toastOk("支付宝已确认订单关闭");
      else toastOk("尚未确认付款，订单保持待确认状态");
    } catch (reason) { if (mounted.current) setOrderError(reason instanceof Error ? reason.message : "支付宝查单失败，请稍后重试"); }
    finally { pending.current = false; if (mounted.current) setBusy(null); }
  };
  const moreOrders = async (): Promise<void> => {
    if (pending.current || !nextCursor) return;
    pending.current = true; setBusy("more");
    try { await loadOrders(undefined, nextCursor); } finally { pending.current = false; if (mounted.current) setBusy(null); }
  };

  const mode = account?.mode ?? "personal";
  const balance = mode === "personal" ? account?.personal?.balanceCredits ?? "0.000000" : account?.team?.balanceCredits;
  const targetName = targetType === "personal" ? "本人个人账户" : manageableTeams.find((item) => item.id === targetTeamId)?.name ?? "请选择团队";
  const selectedTargetName = selectedOrder?.targetType === "personal" ? "本人个人账户" : teams.find((item) => item.id === selectedOrder?.teamId)?.name ?? "团队 " + (selectedOrder?.teamId ?? "");
  const locked = busy !== null;
  return <div className="payments-page">
    <header className="page-head"><div><p className="eyebrow">账户 / 支付宝充值</p><PageTitle icon={HandCoins}>充值与余额</PageTitle></div><button className="btn btn-ghost btn-sm" type="button" disabled={locked} onClick={() => void refresh()}><Glyph icon={RefreshCw} size={13} />重新载入</button></header>
    {accountError ? <Notice tone="stop" k="余额读取失败">{accountError}。当前余额可能未更新，请重新载入。</Notice> : null}
    {account ? <section className="pane"><div className="pane-h"><h2>消费账户</h2><Chip tone={account.enabled ? "ok" : "mute"}>{account.enabled ? "支付宝充值已开放" : "支付宝充值未开放"}</Chip></div><div className="pane-b">
      <div className="payments-readouts"><div><span className="field-label">本人个人余额</span><strong className="num">{account.personal?.balanceCredits ?? "0.000000"} <small>credit</small></strong></div><div><span className="field-label">当前消费余额 · {mode === "personal" ? "个人" : "团队"}</span><strong className="num">{balance ?? "—"} <small>credit</small></strong></div></div>
      <label className="field"><span className="field-label">消费归属（选择后保存）</span><Select ariaLabel="消费账户" value={mode} onChange={(value) => void saveMode(value as AccountMode)} options={[{ value: "personal", label: "个人 · " + (account.personal?.name ?? "本人余额") }, ...(account.team || mode === "team" ? [{ value: "team", label: "团队 · " + (account.team?.name ?? team?.name ?? "当前团队") }] : [])]} disabled={locked} /></label>
      <p className="readout">充值到个人或团队不会自动切换消费账户；消费余额不足时不会自动借用另一个账户。</p>
    </div></section> : !accountError ? <Notice k="账户">正在读取账户余额…</Notice> : null}

    <section className="pane d1"><div className="pane-h"><h2>支付宝充值</h2><span className="readout">1 元 = 1 credit</span></div><div className="pane-b">
      {draftError ? <Notice tone="stop" k="充值未完成">{draftError}</Notice> : null}
      <form onSubmit={(event) => { event.preventDefault(); void createOrder(); }}>
        <div className="payments-grid">
          <label className="field"><span className="field-label">充值对象</span><Select ariaLabel="充值目标" value={targetType} onChange={(value) => setTargetType(value as TargetType)} options={[{ value: "personal", label: "本人 · " + (user.displayName || user.email) }, ...(manageableTeams.length ? [{ value: "team", label: "团队共享余额" }] : [])]} disabled={locked} /></label>
          {targetType === "team" ? <label className="field"><span className="field-label">到账团队</span><Select ariaLabel="充值团队" value={targetTeamId} onChange={setTargetTeamId} options={[{ value: "", label: "请选择团队…" }, ...manageableTeams.map((item) => ({ value: item.id, label: item.name }))]} disabled={locked} /></label> : null}
          <label className="field"><span className="field-label">充值金额（元）</span><input className="input num" aria-label="充值金额（元）" inputMode="decimal" value={amountYuan} onChange={(event) => setAmountYuan(event.target.value)} placeholder="最低 1.00" disabled={locked} required /></label>
        </div>
        <p className="readout">到账对象：{targetName}。普通充值金额 1–100000 元，最多两位小数。</p>
        <div className="form-row payments-actions"><label className="form-row"><input type="checkbox" checked={mobile} onChange={(event) => setMobile(event.target.checked)} disabled={locked} />使用手机收银台</label><button className="btn" type="submit" disabled={locked || !account?.enabled}><Glyph icon={ExternalLink} size={14} />{busy === "order" ? "创建订单中…" : "前往支付宝付款"}</button></div>
      </form>
      {isRoot ? <div className="payments-test"><h3>root 小额实付测试</h3><p className="readout">向「{targetName}」实际支付 0.01 元，确认后增加 0.01 credit。使用当前配置的支付宝环境。</p><button className="btn btn-ghost" type="button" disabled={locked || !account?.enabled} onClick={() => void createOrder(true)}>0.01 元支付测试</button></div> : null}
    </div></section>

    {orderError ? <Notice tone="stop" k="订单核对失败">{orderError}。状态尚未更新，请重试查询。</Notice> : null}
    {selectedOrder ? <section className="pane d1"><div className="pane-h"><h2>当前支付订单</h2><Chip tone={statusText[selectedOrder.status].tone}>{statusText[selectedOrder.status].label}</Chip></div><div className="pane-b">
      <p className="readout payments-order-id">订单 {selectedOrder.outTradeNo}</p><p>¥{selectedOrder.amountYuan} → {selectedOrder.creditAmount} credit · {selectedTargetName}</p>
      {selectedOrder.status === "pending" ? <><Notice k="等待可信确认">请在支付宝完成付款。跳回本页只会读取订单状态，款项确认以支付宝通知或主动查单结果为准。</Notice><p className="readout">付款截止：{formatShanghaiTime(selectedOrder.expiresAt)}。超过截止时间仍可核对订单；本页不会凭到期时间判定款项未付。</p></> : selectedOrder.status === "paid" ? <Notice k="已确认入账">支付宝付款已确认，该订单已增加 {selectedOrder.creditAmount} credit。</Notice> : <Notice k="已关闭">支付宝已确认该订单关闭。</Notice>}
      <button className="btn btn-ghost" type="button" disabled={locked} onClick={() => void reconcile(selectedOrder)}><Glyph icon={RefreshCw} size={14} />{busy === "reconcile" ? "正在查询…" : "查询支付宝并核对"}</button>
    </div></section> : null}

    <section className="pane d1"><div className="pane-h"><h2>充值记录 · {targetType === "personal" ? "本人" : targetName}</h2><span className="readout">{orders.length} 条</span></div>
      {ordersError ? <Notice tone="stop" k="记录读取失败">{ordersError}。请重新载入。</Notice> : null}
      <div className="table-scroll"><table className="table"><thead><tr><th>商户订单号</th><th>金额</th><th>到账</th><th>目标</th><th>状态</th><th>创建时间</th><th>操作</th></tr></thead><tbody>{orders.map((order) => <OrderRow key={order.id} order={order} disabled={locked} onReconcile={(next) => void reconcile(next)} />)}{orders.length === 0 ? <tr><td colSpan={7} className="readout">{orderLoading ? "正在读取充值记录…" : ordersError ? "充值记录暂不可用。" : "暂无充值记录。"}</td></tr> : null}</tbody></table></div>
      {nextCursor ? <div className="pane-b"><button className="btn btn-ghost" type="button" disabled={locked} onClick={() => void moreOrders()}>{busy === "more" ? "读取中…" : "更多订单"}</button></div> : null}
    </section>
    {isRoot ? <PaymentConfigPanel onSaved={() => void loadAccount()} /> : null}
  </div>;
}
