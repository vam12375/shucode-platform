import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { api } from "../api.js";
import { useAuth } from "../auth.js";
import { confirmDialog, toastOk } from "../components/Feedback.js";
import { Select } from "../components/Select.js";
import { Chip, Notice } from "../components/StatusChip.js";
import { formatShanghaiTime } from "../time.js";

interface VideoTemplate {
  id: string;
  version: string;
  family: string;
  name: { zh: string; en: string };
  description: { zh: string; en: string };
  industries: string[];
  requiredFacts: string[];
  assetRequirements: string[];
  avoidWhen: string[];
  minAssets: number;
  aspectRatios: string[];
  durationRange: { min: number; max: number };
  // v2 配方才有的可选字段：标称整片秒数、整体拍摄风格，以及节拍的秒数/取舍层级/幕名/声画。
  nominalSeconds?: number;
  style?: { look: string; lens: string; motion: string; sound: string; narration?: string };
  beats: { purpose: string; framing: string; action: string; camera: string; seconds?: number; tier?: string; act?: string; sound?: string }[];
  qualityChecks: string[];
  verifiedModelIds: string[];
}
interface TemplateReviewInput {
  templateId: string;
  templateVersion: string;
  projectTeamId: string;
  projectId: string;
  score: number;
  approved: boolean;
  note: string;
}
interface TemplateReview extends TemplateReviewInput {
  modelId: string;
  outputAssetId: string;
  reviewerId: string;
  reviewedAt: string;
}
const identity = (item: VideoTemplate) => JSON.stringify([item.id, item.version]);
const reviewIdentity = (item: TemplateReview) => JSON.stringify([item.templateId, item.templateVersion, item.modelId]);
const decisionOptions = [{ value: "approved", label: "通过（至少85分）" }, { value: "rejected", label: "未通过" }];
const message = (error: unknown) => error instanceof Error ? error.message : "操作失败，请重试";
// 压缩时 core 永不删，先删 extra 再删 support；评审时据此判断缩短后的片子是否还成立。
const tierLabels: Record<string, string> = { core: "核心", support: "补充", extra: "可删" };
const beatMeta = (beat: VideoTemplate["beats"][number]) => beat.seconds ? `【${beat.act ?? ""} · ${beat.seconds}秒 · ${tierLabels[beat.tier ?? ""] ?? beat.tier ?? ""}】` : "";

export function MediaTemplateReviews() {
  const { isRoot, user } = useAuth();
  return isRoot ? <TemplateReviewsContent key={user.id} /> : <Notice>仅超级管理员可审核模板。</Notice>;
}

function TemplateReviewsContent() {
  const [templates, setTemplates] = useState<VideoTemplate[]>([]);
  const [reviews, setReviews] = useState<TemplateReview[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [selection, setSelection] = useState("");
  const [teamId, setTeamId] = useState("");
  const [projectId, setProjectId] = useState("");
  const [score, setScore] = useState("");
  const [decision, setDecision] = useState("approved");
  const [note, setNote] = useState("");
  const [watched, setWatched] = useState(false);
  const active = useRef(false);
  const request = useRef(0);
  const busyRef = useRef(false);
  const load = useCallback(async () => {
    const current = ++request.current;
    setLoading(true);
    setError("");
    try {
      const [catalog, history] = await Promise.all([
        api<VideoTemplate[]>("/media/templates"),
        api<TemplateReview[]>("/media/templates/reviews"),
      ]);
      if (!active.current || current !== request.current) return;
      setTemplates(catalog);
      setReviews(history);
    } catch (failure) {
      if (active.current && current === request.current) setError(message(failure));
    } finally {
      if (active.current && current === request.current) setLoading(false);
    }
  }, []);
  useEffect(() => {
    active.current = true;
    void load();
    return () => {
      // 权限变化会卸载此 owner；晚到响应不能把跨团队审核证据写回新页面。
      active.current = false;
      request.current++;
    };
  }, [load]);

  const selected = templates.find((item) => identity(item) === selection) ?? templates[0];
  const latest = new Map<string, number>();
  reviews.forEach((item, index) => latest.set(reviewIdentity(item), index));
  const search = query.trim().toLocaleLowerCase();
  const filtered = templates.filter((item) => !search ||
    [item.id, item.name.zh, item.name.en, item.family, ...item.industries].join(" ").toLocaleLowerCase().includes(search));

  const writeReview = async (input: TemplateReviewInput): Promise<void> => {
    await api<TemplateReview>("/media/templates/reviews", { method: "POST", body: JSON.stringify(input) });
    if (!active.current) return;
    setWatched(false);
    toastOk(input.approved ? "审核已记录，验证状态由平台重新计算" : "未通过记录已保存");
    await load();
  };
  const submit = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    if (busyRef.current || loading) return;
    const numeric = Number(score);
    let problem = "";
    if (!selected || !teamId.trim() || !projectId.trim() || !note.trim()) problem = "请完整填写真实成片证据与审核说明。";
    else if (!Number.isInteger(numeric) || numeric < 1 || numeric > 100) problem = "评分须为1至100的整数。";
    else if (decision === "approved" && numeric < 85) problem = "通过审核至少需要85分。";
    else if (!watched) problem = "请先实际观看成片并确认。";
    if (problem || !selected) { setError(problem); return; }
    busyRef.current = true;
    setBusy(true);
    setError("");
    try {
      await writeReview({ templateId: selected.id, templateVersion: selected.version, projectTeamId: teamId.trim(), projectId: projectId.trim(), score: numeric, approved: decision === "approved", note: note.trim() });
    } catch (failure) {
      if (active.current) setError(message(failure));
    } finally {
      busyRef.current = false;
      if (active.current) setBusy(false);
    }
  };
  const revoke = async (review: TemplateReview): Promise<void> => {
    if (busyRef.current || loading) return;
    busyRef.current = true;
    setBusy(true);
    setError("");
    try {
      const result = await confirmDialog({
        title: "撤销模板验证", danger: true, confirmText: "撤销验证",
        message: "追加未通过记录后，该模板版本与模型不再自动采用。",
        input: { label: "撤销原因", minLength: 1 },
      });
      if (!active.current || !result.confirmed) return;
      const reason = result.value.trim();
      if (!reason || reason.length > 2000) { setError("撤销原因须为1至2000字。"); return; }
      await writeReview({ templateId: review.templateId, templateVersion: review.templateVersion, projectTeamId: review.projectTeamId, projectId: review.projectId, score: review.score, approved: false, note: reason });
    } catch (failure) {
      if (active.current) setError(message(failure));
    } finally {
      busyRef.current = false;
      if (active.current) setBusy(false);
    }
  };

  return <div>
    {/* 窄屏将前缀与正文放在同一换行项，避免标签被压成逐字竖排。 */}
    <Notice>真实成片审核：配方草案不等于效果已验证。请在客户端实际生成并观看成片，核验商品一致、叙事、音画、字幕与卖点；审核通过才可自动采用。模型配置变化需要重新验证。</Notice>
    {error ? <Notice tone="stop" k="操作未完成">{error}</Notice> : null}
    <section className="pane">
      <div className="pane-h"><h2>模板目录 · {templates.length}</h2><button className="btn btn-ghost btn-sm" type="button" disabled={loading || busy} onClick={() => void load()}>刷新</button></div>
      <div className="pane-b form-stack">
        {loading ? <p role="status">正在加载模板与审核记录…</p> : null}
        <label className="field"><span className="field-label">搜索模板</span><input className="input" aria-label="搜索模板" placeholder="名称、品类、家族或 ID" value={query} onChange={(event) => setQuery(event.target.value)} /></label>
        {!loading && !filtered.length ? <p>没有符合筛选的模板。</p> : null}
        {filtered.map((item) => <details key={identity(item)}>
          <summary>{item.name.zh} <code>{item.id} · {item.version}</code> <Chip tone={item.verifiedModelIds.length ? "ok" : "mute"}>{item.verifiedModelIds.length ? "已验证模型：" + item.verifiedModelIds.join("、") : "未验证"}</Chip></summary>
          <div className="pane-b form-stack">
            <p>{item.description.zh}</p><p>{item.industries.join("、")} · {item.aspectRatios.join(" / ")} · {item.durationRange.min}–{item.durationRange.max}秒 · 至少{item.minAssets}张素材</p>
            {item.nominalSeconds ? <p>标称 {item.nominalSeconds} 秒，可压缩到 {item.durationRange.min} 秒、拉伸到 {item.durationRange.max} 秒</p> : null}
            {item.style ? <p>整体风格：{[item.style.look, item.style.lens, item.style.motion, item.style.sound, item.style.narration].filter(Boolean).join("；")}</p> : null}
            <p>商品事实：{item.requiredFacts.join("；")}</p><p>素材要求：{item.assetRequirements.join("；")}</p>
            <ol>{item.beats.map((beat, index) => <li key={index}>{beatMeta(beat)}{beat.purpose} · {beat.framing} · {beat.action} · {beat.camera}{beat.sound ? " · 声画：" + beat.sound : ""}</li>)}</ol>
            <p>质量检查：{item.qualityChecks.join("；")}</p><p>不适用：{item.avoidWhen.join("；")}</p>
          </div>
        </details>)}
      </div>
    </section>
    <section className="pane">
      <div className="pane-h"><h2>记录真实样本评审</h2></div>
      <form className="pane-b form-stack" onSubmit={(event) => void submit(event)}>
        <label className="field"><span className="field-label">模板版本</span><Select ariaLabel="模板版本" value={selected ? identity(selected) : ""} onChange={(value) => { setSelection(value); setWatched(false); }} options={templates.map((item) => ({ value: identity(item), label: item.name.zh + " · " + item.version }))} disabled={loading || busy} /></label>
        <div className="form-row">
          <label className="field grow"><span className="field-label">证据团队 ID</span><input className="input" aria-label="证据团队 ID" value={teamId} required disabled={busy} onChange={(event) => { setTeamId(event.target.value); setWatched(false); }} /></label>
          <label className="field grow"><span className="field-label">证据制作单 ID</span><input className="input" aria-label="证据制作单 ID" value={projectId} required disabled={busy} onChange={(event) => { setProjectId(event.target.value); setWatched(false); }} /></label>
        </div>
        <div className="form-row">
          <label className="field grow"><span className="field-label">审核评分</span><input className="input num" aria-label="审核评分" type="number" min={1} max={100} step={1} value={score} required disabled={busy} onChange={(event) => setScore(event.target.value)} /></label>
          <label className="field grow"><span className="field-label">审核结论</span><Select ariaLabel="审核结论" value={decision} options={decisionOptions} onChange={setDecision} disabled={busy} /></label>
        </div>
        <label className="field"><span className="field-label">审核说明</span><textarea aria-label="审核说明" value={note} maxLength={2000} required disabled={busy} onChange={(event) => setNote(event.target.value)} /></label>
        <label className="form-row" style={{ flexWrap: "nowrap", alignItems: "flex-start" }}><input type="checkbox" aria-label="已观看成片" style={{ flexShrink: 0 }} checked={watched} disabled={busy} onChange={(event) => setWatched(event.target.checked)} /><span>我已实际观看该制作单成片，并核验商品与镜头质量</span></label>
        <p>服务器核对已完成制作单、模板版本及实际成片文件，并派生模型和审核身份。</p>
        <div><button className="btn" type="submit" disabled={loading || busy || !selected}>{busy ? "正在记录…" : "提交审核"}</button></div>
      </form>
    </section>
    <section className="pane">
      <div className="pane-h"><h2>审核记录 · {reviews.length}</h2></div>
      <div className="table-scroll vscroll"><table className="table" style={{ minWidth: 900 }}>
        <thead><tr><th>模板 / 模型</th><th>结论 / 评分</th><th>真实成片证据</th><th>说明 / 审核人 / 上海时间</th><th>操作</th></tr></thead>
        <tbody>{reviews.map((row, index) => ({ row, index })).reverse().map(({ row, index }) => <tr key={index}>
          <td><code>{row.templateId} · {row.templateVersion}</code><br /><code>{row.modelId}</code></td>
          <td><Chip tone={row.approved ? "ok" : "stop"}>{row.approved ? "通过" : "未通过"}</Chip> <span className="num">{row.score}</span>{latest.get(reviewIdentity(row)) !== index ? <small> · 历史记录</small> : null}</td>
          <td><code>{row.projectTeamId}</code><br /><code>{row.projectId}</code><br /><code>{row.outputAssetId}</code></td>
          <td>{row.note}<br /><code>{row.reviewerId}</code><br />{formatShanghaiTime(row.reviewedAt)}</td>
          <td>{row.approved && latest.get(reviewIdentity(row)) === index ? <button className="btn btn-ghost btn-sm" type="button" disabled={busy || loading} onClick={() => void revoke(row)}>撤销</button> : "—"}</td>
        </tr>)}</tbody>
      </table>{!loading && !reviews.length ? <p className="pane-b">暂无真实样本评审记录。</p> : null}</div>
    </section>
  </div>;
}
