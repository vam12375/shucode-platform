import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { PackageCheck, Plus, RefreshCw, Save, Trash2 } from "lucide-react";
import { api, type ApiError } from "../api.js";
import { useAuth } from "../auth.js";
import { toastOk } from "../components/Feedback.js";
import { Glyph, PageTitle } from "../components/Icon.js";
import { Chip, Notice } from "../components/StatusChip.js";
import {
  buildPackageUpdate,
  mergePackageDraft,
  packageAmount,
  publishedPackages,
  resolvePackageConflict,
  validatePackages,
  type CreditPackage,
  type CreditPackageStatus,
  type CreditPackageTarget,
} from "../creditPackages.js";

interface CatalogResponse {
  revision: string;
  packages: CreditPackage[];
}

const statusLabel: Record<CreditPackageStatus, string> = {
  draft: "草稿",
  published: "已上架",
  archived: "已归档",
};

function packageError(reason: unknown): string {
  return reason instanceof Error ? reason.message : "套餐目录操作失败。";
}

function isConflict(reason: unknown): boolean {
  return (reason as ApiError | null)?.code === 409;
}

export function CreditPackagesPage() {
  const { isRoot } = useAuth();
  const [packages, setPackages] = useState<CreditPackage[]>([]);
  const [serverPackages, setServerPackages] = useState<CreditPackage[]>([]);
  const [revision, setRevision] = useState<string | null>(null);
  const [previewTarget, setPreviewTarget] = useState<CreditPackageTarget>("personal");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [needsRefresh, setNeedsRefresh] = useState(false);
  const [conflicts, setConflicts] = useState<{ sku: string; field: keyof CreditPackage }[]>([]);
  const [rebased, setRebased] = useState(false);
  const pending = useRef(false);
  const mounted = useRef(true);
  const packagesRef = useRef(packages);
  const baselineRef = useRef<CreditPackage[]>([]);
  packagesRef.current = packages;

  const load = useCallback(async (preserveDrafts = false, signal?: AbortSignal): Promise<void> => {
    setLoading(true);
    try {
      const response = await api<CatalogResponse>("/payments/packages", { signal });
      if (!mounted.current || signal?.aborted) return;
      if (!response || typeof response.revision !== "string" || !Array.isArray(response.packages)) {
        throw new Error("套餐目录响应格式无效。");
      }
      const nextServer = response.packages;
      const local = packagesRef.current;
      const merge = preserveDrafts
        ? mergePackageDraft(baselineRef.current, local, nextServer)
        : { packages: nextServer, conflicts: [] as { sku: string; field: keyof CreditPackage }[] };
      baselineRef.current = nextServer;
      setServerPackages(nextServer);
      setPackages(merge.packages);
      setRevision(response.revision);
      setConflicts(merge.conflicts);
      setNeedsRefresh(false);
      setRebased(preserveDrafts);
      setError("");
    } catch (reason) {
      if (mounted.current && !signal?.aborted) setError(packageError(reason));
    } finally {
      if (mounted.current && !signal?.aborted) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    if (!isRoot) {
      setLoading(false);
      return () => { mounted.current = false; };
    }
    const controller = new AbortController();
    void load(false, controller.signal);
    return () => {
      mounted.current = false;
      controller.abort();
    };
  }, [isRoot, load]);

  const updatePackage = (sku: string, patch: Partial<CreditPackage>): void => {
    setPackages((current) => {
      let next = current.map((item) => item.sku === sku
        ? { ...item, ...patch, ...(patch.amountYuan !== undefined ? { creditAmount: packageAmount(patch.amountYuan) ?? "" } : {}) }
        : item);
      const changed = next.find((item) => item.sku === (patch.sku ?? sku));
      if (patch.recommended && changed) {
        next = next.map((item) => item.sku !== changed.sku && item.targetType === changed.targetType
          ? { ...item, recommended: false }
          : item);
      }
      return next;
    });
    setConflicts((current) => current.filter((item) => item.sku !== sku));
    setRebased(false);
  };

  const addPackage = (): void => {
    const target = previewTarget;
    const sku = `${target}-new-${crypto.randomUUID().replaceAll("-", "").slice(0, 8)}`;
    const sortOrder = packages.reduce((maximum, item) => Math.max(maximum, item.sortOrder), 0) + 10;
    setPackages((current) => [...current, {
      sku,
      name: "新套餐",
      description: "一次性 credit 充值",
      targetType: target,
      amountYuan: "1.00",
      creditAmount: "1.00",
      status: "draft",
      recommended: false,
      sortOrder,
    }]);
  };

  const removeUnpublishedDraft = (sku: string): void => {
    if (serverPackages.some((item) => item.sku === sku)) return;
    setPackages((current) => current.filter((item) => item.sku !== sku));
  };

  const save = async (): Promise<void> => {
    if (!isRoot || pending.current || loading || needsRefresh || conflicts.length || revision === null) return;
    const validationError = validatePackages(packages);
    if (validationError) {
      setError(validationError);
      return;
    }
    pending.current = true;
    setBusy(true);
    setError("");
    setRebased(false);
    try {
      const result = await api<CatalogResponse>("/payments/packages", {
        method: "PUT",
        body: JSON.stringify(buildPackageUpdate(packages, revision)),
      });
      if (!mounted.current) return;
      baselineRef.current = result.packages;
      setServerPackages(result.packages);
      setPackages(result.packages);
      setRevision(result.revision);
      setNeedsRefresh(false);
      setConflicts([]);
      toastOk("充值套餐目录已保存");
    } catch (reason) {
      if (!mounted.current) return;
      setError(isConflict(reason)
        ? "目录版本已变化。刷新最新目录并保留草稿，检查合并结果后再保存。"
        : packageError(reason));
      if (isConflict(reason)) setNeedsRefresh(true);
    } finally {
      pending.current = false;
      if (mounted.current) setBusy(false);
    }
  };

  const preview = useMemo(
    () => packages
      .filter((item) => item.targetType === previewTarget && item.status !== "archived")
      .sort((a, b) => a.sortOrder - b.sortOrder || a.sku.localeCompare(b.sku)),
    [packages, previewTarget],
  );
  const publishable = useMemo(() => publishedPackages(packages), [packages]);

  if (!isRoot) return <Notice tone="stop" k="无权访问">充值套餐目录仅限 root 管理员。</Notice>;

  return (
    <div className="credit-packages-page">
      <header className="page-head">
        <div>
          <p className="eyebrow">计量 / 客户端商品目录</p>
          <PageTitle icon={PackageCheck}>充值套餐</PageTitle>
        </div>
        <div className="credit-packages-toolbar">
          <button className="btn btn-ghost btn-sm" type="button" disabled={loading || busy} onClick={() => void load(false)}>
            <Glyph icon={RefreshCw} size={14} />刷新目录
          </button>
          <button className="btn btn-ghost btn-sm" type="button" disabled={loading || busy} onClick={addPackage}>
            <Glyph icon={Plus} size={14} />新增草稿
          </button>
          <button className="btn btn-sm" type="button" disabled={loading || busy || needsRefresh || conflicts.length > 0 || revision === null} onClick={() => void save()}>
            <Glyph icon={Save} size={14} />{busy ? "保存中…" : "保存目录"}
          </button>
        </div>
      </header>
      <p className="readout">客户端只展示已上架套餐。编辑、排序与推荐仅写入后台目录；新套餐默认为草稿，不会自动上架。余额按 1 元 = 1 credit 计算，已保存套餐通过归档下架。</p>
      {error ? <Notice tone="stop" k={needsRefresh ? "版本冲突" : "操作未完成"}>{error}</Notice> : null}
      {needsRefresh ? <button className="btn btn-sm" type="button" disabled={loading || busy} onClick={() => void load(true)}><Glyph icon={RefreshCw} size={14} />刷新并保留草稿</button> : null}
      {rebased ? <Notice k="草稿已合并">已读取最新目录并保留本地修改。保存前请检查下方并发修改提示和目录内容。</Notice> : null}
      {conflicts.length ? (
        <section className="pane credit-packages-conflicts" aria-label="并发修改冲突">
          <div className="pane-h"><h2>需要确认的并发修改</h2><Chip tone="hot">{conflicts.length} 项</Chip></div>
          <div className="pane-b">
            <p className="readout">同一字段被其他 root 管理员同时修改。逐项选择保留本地草稿或采用当前服务器值，完成后才能保存。</p>
            {conflicts.map((conflict) => {
              const local = packages.find((item) => item.sku === conflict.sku);
              const remote = serverPackages.find((item) => item.sku === conflict.sku);
              return <div className="credit-packages-conflict" key={`${conflict.sku}:${conflict.field}`}>
                <strong>{conflict.sku} · {conflict.field}</strong>
                <span className="readout">本地：{String(local?.[conflict.field] ?? "已删除")} · 服务器：{String(remote?.[conflict.field] ?? "已删除")}</span>
                <button className="btn btn-ghost btn-sm" type="button" onClick={() => {
                  setPackages((current) => resolvePackageConflict(current, serverPackages, conflict, "remote"));
                  setConflicts((current) => current.filter((item) => item.sku !== conflict.sku || item.field !== conflict.field));
                }}>采用服务器值</button>
                <button className="btn btn-sm" type="button" onClick={() => setConflicts((current) => current.filter((item) => item.sku !== conflict.sku || item.field !== conflict.field))}>保留本地值</button>
              </div>;
            })}
          </div>
        </section>
      ) : null}
      {loading && revision === null ? <Notice k="套餐目录">正在读取套餐目录…</Notice> : null}
      {!loading && error && revision === null ? <button className="btn btn-sm" type="button" onClick={() => void load(false)}><Glyph icon={RefreshCw} size={14} />重试读取</button> : null}

      <section className="pane d1" aria-label="充值套餐目录编辑">
        <div className="pane-h"><h2>套餐目录</h2><span className="readout">{packages.length} 档 · 已上架 {publishable.length} 档 · revision {revision ?? "—"}</span></div>
        <div className="pane-b flush table-scroll">
          <table className="table credit-packages-table">
            <thead><tr><th>稳定 SKU</th><th>名称 / 说明</th><th>充值对象</th><th className="num">售价（元）</th><th className="num">到账 credit</th><th>状态</th><th>推荐</th><th className="num">排序</th><th>操作</th></tr></thead>
            <tbody>{packages.map((item) => {
              const saved = serverPackages.some((entry) => entry.sku === item.sku);
              return <tr key={item.sku}>
                <td><input className="input credit-packages-sku" aria-label={`${item.sku} SKU`} value={item.sku} disabled={busy || saved} onChange={(event) => updatePackage(item.sku, { sku: event.target.value.trim().toLowerCase() })} /></td>
                <td><div className="credit-packages-name-fields">
                  <input className="input" aria-label={`${item.sku} 套餐名称`} value={item.name} disabled={busy} maxLength={24} onChange={(event) => updatePackage(item.sku, { name: event.target.value })} />
                  <input className="input" aria-label={`${item.sku} 套餐说明`} value={item.description} disabled={busy} maxLength={48} onChange={(event) => updatePackage(item.sku, { description: event.target.value })} />
                </div></td>
                <td><select className="input" aria-label={`${item.sku} 充值对象`} value={item.targetType} disabled={busy || saved} onChange={(event) => updatePackage(item.sku, { targetType: event.target.value as CreditPackageTarget })}><option value="personal">个人</option><option value="team">团队</option></select></td>
                <td><input className="input num" aria-label={`${item.sku} 售价`} inputMode="decimal" value={item.amountYuan} disabled={busy} onChange={(event) => updatePackage(item.sku, { amountYuan: event.target.value })} /></td>
                <td className="num">{packageAmount(item.amountYuan) ?? "—"}</td>
                <td><select className="input" aria-label={`${item.sku} 状态`} value={item.status} disabled={busy} onChange={(event) => updatePackage(item.sku, { status: event.target.value as CreditPackageStatus })}>{Object.entries(statusLabel).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></td>
                <td><label className="credit-packages-recommended"><input type="checkbox" checked={item.recommended} disabled={busy} onChange={(event) => updatePackage(item.sku, { recommended: event.target.checked })} />推荐</label></td>
                <td><input className="input num credit-packages-sort" aria-label={`${item.sku} 排序`} type="number" min={0} max={9999} step={1} value={item.sortOrder} disabled={busy} onChange={(event) => updatePackage(item.sku, { sortOrder: Number(event.target.value) })} /></td>
                <td>{saved ? <Chip tone={item.status === "published" ? "ok" : item.status === "archived" ? "mute" : "info"}>{statusLabel[item.status]}</Chip> : <button className="btn btn-ghost btn-sm" type="button" disabled={busy} aria-label={`移除未保存套餐 ${item.sku}`} onClick={() => removeUnpublishedDraft(item.sku)}><Glyph icon={Trash2} size={13} />移除草稿</button>}</td>
              </tr>;
            })}</tbody>
          </table>
          {!loading && packages.length === 0 ? <p className="credit-packages-empty readout">目录为空。新增套餐会以草稿状态加入，只有显式上架后才会出现在客户端。</p> : null}
        </div>
      </section>

      <section className="pane d1" aria-label="客户端套餐卡片预览">
        <div className="pane-h"><h2>客户端卡片预览</h2><span className="readout">预览草稿；实际客户端只展示已上架条目</span></div>
        <div className="pane-b">
          <div className="credit-packages-preview-switch" role="group" aria-label="预览充值对象">
            <button className={previewTarget === "personal" ? "btn btn-sm" : "btn btn-ghost btn-sm"} type="button" aria-pressed={previewTarget === "personal"} onClick={() => setPreviewTarget("personal")}>个人套餐</button>
            <button className={previewTarget === "team" ? "btn btn-sm" : "btn btn-ghost btn-sm"} type="button" aria-pressed={previewTarget === "team"} onClick={() => setPreviewTarget("team")}>团队套餐</button>
          </div>
          <div className="credit-packages-preview-grid">
            {preview.map((item) => <article className={`credit-package-preview ${item.status}`} key={item.sku}>
              <div className="credit-package-preview-head"><Chip tone={item.status === "published" ? "ok" : item.status === "archived" ? "mute" : "info"}>{statusLabel[item.status]}</Chip>{item.recommended ? <Chip tone="hot">推荐</Chip> : null}</div>
              <h3>{item.name || "套餐名称"}</h3>
              <p className="readout">{item.description || "一次性 credit 充值"}</p>
              <strong className="credit-package-preview-amount">¥{packageAmount(item.amountYuan) ?? "—"}</strong>
              <span className="credit-package-preview-credit">到账 {packageAmount(item.amountYuan) ?? "—"} credit</span>
              <span className="readout">一次性付款 · 无自动续费</span>
            </article>)}
            {preview.length === 0 ? <div className="credit-packages-empty readout">此充值对象暂无套餐。新增套餐后可在此预览。</div> : null}
          </div>
        </div>
      </section>
    </div>
  );
}
