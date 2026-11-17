import { useCallback, useEffect, useRef, useState } from "react";
import { ChevronLeft, ChevronRight, RefreshCw, X } from "lucide-react";
import { api } from "../api.js";
import { Glyph } from "../components/Icon.js";
import { Select } from "../components/Select.js";
import { Chip, Notice } from "../components/StatusChip.js";
import { formatShanghaiTime } from "../time.js";

interface FailureShot {
  status: string; attempt: number; error?: string;
  providerError?: { code: string; category?: string };
}
interface FailureRow {
  id: string; teamId: string; kind: string; product: string; status: string;
  error: string; updatedAt: string; chargedMicro: number; diagnosticsAvailable: boolean;
  shots: FailureShot[];
}
interface Diagnostic {
  at: string; shot: number; attempt: number; stage: string; kind: string;
  modelId?: string; upstreamModel?: string; providerId?: string; taskId?: string;
  httpStatus?: number; requestId?: string; code?: string; message?: string; response?: string;
}
interface Detail { project: FailureRow; diagnostics: Diagnostic[] }
const KINDS = [{ value: "", label: "全部媒体" }, { value: "image", label: "图片" }, { value: "video", label: "视频" }];
const CATEGORIES = [
  { value: "", label: "全部原因" }, { value: "content_policy", label: "内容审核" },
  { value: "invalid_input", label: "参数 / 素材" }, { value: "authentication", label: "上游授权" },
  { value: "quota", label: "上游额度" }, { value: "rate_limit", label: "请求限流" },
  { value: "service_unavailable", label: "上游服务" }, { value: "unclassified", label: "未分类" },
];
const STATUSES = [
  { value: "", label: "全部失败状态" }, { value: "failed", label: "生成失败" },
  { value: "analysis_failed", label: "分析失败" }, { value: "reconciliation_required", label: "待核对" },
  { value: "cancelled", label: "已取消" }, { value: "completed", label: "重试后已完成" },
  { value: "queued", label: "重试排队中" }, { value: "running", label: "恢复生成中" }, { value: "composing", label: "合成中" },
];
const statusLabel = (value: string) => STATUSES.find((item) => item.value === value)?.label ?? value;
const categoryLabel = (row: FailureRow) => [...new Set(row.shots.map((shot) => CATEGORIES.find((item) => item.value !== "" && item.value === shot.providerError?.category)?.label).filter(Boolean))].join("、") || "未分类";

export function MediaFailures({ hours }: { hours: string }) {
  const [kind, setKind] = useState("");
  const [category, setCategory] = useState("");
  const [status, setStatus] = useState("");
  const [query, setQuery] = useState("");
  const [search, setSearch] = useState("");
  const [offset, setOffset] = useState(0);
  const [rows, setRows] = useState<FailureRow[]>([]);
  const [total, setTotal] = useState(0);
  const [error, setError] = useState("");
  const [detailError, setDetailError] = useState("");
  const [detail, setDetail] = useState<Detail | null>(null);
  const [busy, setBusy] = useState(false);
  const [detailBusy, setDetailBusy] = useState(false);
  const detailRequest = useRef(0);
  const listRequest = useRef(0);
  const load = useCallback(async () => {
    const request = ++listRequest.current;
    setBusy(true);
    try {
      const params = new URLSearchParams({ hours, kind, category, status, q: search, limit: "20", offset: String(offset) });
      const data = await api<{ records: FailureRow[]; total: number }>(`/ops/media-failures?${params}`);
      if (request !== listRequest.current) return;
      setRows(data.records); setTotal(data.total); setError("");
    } catch (cause) { if (request === listRequest.current) setError(cause instanceof Error ? cause.message : "加载失败"); }
    finally { if (request === listRequest.current) setBusy(false); }
  }, [hours, kind, category, status, search, offset]);
  useEffect(() => { void load(); return () => { listRequest.current++; }; }, [load]);
  useEffect(() => () => { detailRequest.current++; }, []);
  const inspect = async (row: FailureRow) => {
    const request = ++detailRequest.current;
    setDetail({ project: row, diagnostics: [] }); setDetailError(""); setDetailBusy(true);
    try {
      const data = await api<Detail>(`/ops/media-failures/${encodeURIComponent(row.teamId)}/${encodeURIComponent(row.id)}`);
      if (request === detailRequest.current) setDetail(data);
    } catch (cause) { if (request === detailRequest.current) setDetailError(cause instanceof Error ? cause.message : "加载失败"); }
    finally { if (request === detailRequest.current) setDetailBusy(false); }
  };
  const close = () => { detailRequest.current++; setDetail(null); setDetailBusy(false); };
  return <section className="pane d1" aria-label="媒体生成失败">
    <div className="pane-h"><h2>媒体生成失败</h2><span className="readout">{total} 个制作单 · root 诊断</span></div>
    <div className="pane-b">
      <form className="form-row" style={{ gap: 8, flexWrap: "wrap" }} onSubmit={(event) => { event.preventDefault(); setSearch(query.trim()); setOffset(0); }}>
        <Select ariaLabel="媒体类型" value={kind} onChange={(value) => { setKind(value); setOffset(0); }} options={KINDS} />
        <Select ariaLabel="失败原因" value={category} onChange={(value) => { setCategory(value); setOffset(0); }} options={CATEGORIES} />
        <Select ariaLabel="失败状态" value={status} onChange={(value) => { setStatus(value); setOffset(0); }} options={STATUSES} />
        <input className="input" aria-label="搜索媒体失败" value={query} maxLength={200} onChange={(event) => setQuery(event.target.value)} placeholder="制作单 / 模型 / task ID / 错误码" />
        <button className="btn btn-ghost" type="submit">查询</button>
        <button className="btn btn-ghost" type="button" disabled={busy} onClick={() => void load()}><Glyph icon={RefreshCw} size={14} />刷新</button>
      </form>
      {error ? <Notice tone="stop" k="媒体失败列表加载失败">{error}</Notice> : null}
    </div>
    <div className="pane-b flush table-scroll" aria-busy={busy}>
      <table className="table"><thead><tr><th scope="col">制作单 / 更新时间</th><th scope="col">类型 / 状态</th><th scope="col">失败原因</th><th scope="col" className="num">已结算</th><th scope="col">诊断</th></tr></thead><tbody>
        {rows.map((row) => <tr key={`${row.teamId}:${row.id}`}>
          <td>{row.product || row.id}<div className="readout">{row.id}</div><div className="readout">{formatShanghaiTime(row.updatedAt)}</div></td>
          <td>{row.kind === "image" ? "图片" : "视频"} <Chip tone={row.status === "reconciliation_required" ? "hot" : row.status === "cancelled" ? "mute" : row.status === "completed" ? "ok" : ["queued", "running", "composing"].includes(row.status) ? "info" : "stop"}>{statusLabel(row.status)}</Chip></td>
          <td>{categoryLabel(row)}<div className="readout">{row.error || row.shots.find((shot) => shot.error)?.error || "未提供错误码"}</div></td>
          <td className="num">{(row.chargedMicro / 1_000_000).toLocaleString("en-US", { maximumFractionDigits: 4 })} credit</td>
          <td><button className="btn btn-ghost btn-sm" type="button" onClick={() => void inspect(row)}>查看详情</button>{!row.diagnosticsAvailable ? <div className="readout">历史未采集</div> : null}</td>
        </tr>)}
        {!busy && rows.length === 0 ? <tr><td colSpan={5} className="readout">当前窗口没有匹配的媒体失败。</td></tr> : null}
      </tbody></table>
    </div>
    <div className="pane-b form-row" style={{ gap: 8 }}>
      <button className="btn btn-ghost btn-sm" disabled={busy || offset === 0} onClick={() => setOffset(Math.max(0, offset - 20))}><Glyph icon={ChevronLeft} size={14} />上一页</button>
      <span className="readout">{total ? offset + 1 : 0}–{Math.min(offset + rows.length, total)} / {total}</span>
      <button className="btn btn-ghost btn-sm" disabled={busy || offset + rows.length >= total} onClick={() => setOffset(offset + 20)}>下一页<Glyph icon={ChevronRight} size={14} /></button>
    </div>
    {detail ? <div className="pane-b" role="region" aria-label="媒体失败诊断详情" aria-busy={detailBusy}>
      <div className="pane-h"><h3>诊断详情 · {detail.project.id}</h3><button className="btn btn-ghost btn-sm" onClick={close}><Glyph icon={X} size={14} />关闭详情</button></div>
      {detailError ? <Notice tone="stop" k="诊断读取失败">{detailError}</Notice> : null}
      {detailBusy ? <p className="readout">正在读取诊断…</p> : !detail.diagnostics.length ? <Notice k="未采集供应商原文">旧制作单的供应商原文未保存，无法补回。请结合镜头错误码及供应商任务记录核查。</Notice> : null}
      {detail.project.shots.map((shot, index) => <p className="readout" key={index}>镜头 {index + 1} · 尝试 {shot.attempt} · {shot.error || shot.status} · {shot.providerError?.code || "无供应商错误码"}</p>)}
      {detail.diagnostics.map((diagnostic, index) => <details key={index} open={index === detail.diagnostics.length - 1}>
        <summary>{formatShanghaiTime(diagnostic.at)} · {diagnostic.shot >= 0 ? `镜头 ${diagnostic.shot + 1}` : "合成 / 系统"} · 尝试 {diagnostic.attempt} · {diagnostic.stage} · {diagnostic.code || diagnostic.httpStatus || "内部错误"}</summary>
        <dl><dt>模型 / 上游型号</dt><dd>{diagnostic.modelId || "—"} / {diagnostic.upstreamModel || "—"}</dd><dt>供应商 / HTTP</dt><dd>{diagnostic.providerId || "默认供应商"} / {diagnostic.httpStatus || "—"}</dd><dt>Request ID / Task ID</dt><dd>{diagnostic.requestId || "—"} / {diagnostic.taskId || "—"}</dd><dt>错误消息</dt><dd style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>{diagnostic.message || "供应商未提供消息"}</dd></dl>
        {diagnostic.response ? <pre style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>{diagnostic.response}</pre> : null}
      </details>)}
    </div> : null}
  </section>;
}
