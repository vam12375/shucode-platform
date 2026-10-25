import { useCallback, useEffect, useRef, useState } from "react";
import { useAuth } from "../auth.js";
import { api } from "../api.js";
import { toastOk } from "../components/Feedback.js";
import { Chip, Notice } from "../components/StatusChip.js";

type DisplayKind = "text" | "image" | "video";
interface DisplayEntry {
  id: string;
  kind: DisplayKind;
  displayName: string;
  modelIds: string[];
  resolutions: string[];
  expectedVersion: string;
}

const kindLabel: Record<DisplayKind, string> = { text: "文字", image: "图片", video: "视频" };

export function ModelDisplayPage() {
  const { isRoot } = useAuth();
  const [entries, setEntries] = useState<DisplayEntry[]>([]);
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true);
  const [busyId, setBusyId] = useState("");
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const actionPending = useRef(false);

  const load = useCallback(async (preserveDrafts = false) => {
    setLoading(true);
    try {
      const result = await api<{ entries: DisplayEntry[] }>("/model-display");
      setEntries(result.entries);
      setDrafts((previous) => preserveDrafts
        ? Object.fromEntries(Object.entries(previous).filter(([id]) => result.entries.some((entry) => entry.id === id)))
        : {});
      setError("");
      setConflict(false);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "模型展示目录加载失败");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (isRoot) void load();
  }, [isRoot, load]);

  const save = async (entry: DisplayEntry): Promise<void> => {
    if (!isRoot || actionPending.current || loading || conflict) return;
    const displayName = (drafts[entry.id] ?? entry.displayName).trim();
    if (!displayName || [...displayName].length > 120) {
      setError("显示名称需为 1–120 个字符。");
      return;
    }
    if (displayName === entry.displayName) return;
    actionPending.current = true;
    setBusyId(entry.id);
    setError("");
    try {
      const result = await api<{ entry: DisplayEntry }>("/model-display", {
        method: "PUT",
        body: JSON.stringify({ id: entry.id, displayName, expectedVersion: entry.expectedVersion }),
      });
      setEntries((previous) => previous.map((item) => item.id === entry.id
        ? result.entry
        : entry.kind !== "text" && item.kind !== "text"
          ? { ...item, expectedVersion: result.entry.expectedVersion }
          : item));
      setDrafts((previous) => {
        const next = { ...previous };
        delete next[entry.id];
        return next;
      });
      setConflict(false);
      toastOk("模型显示名称已更新", "已保存");
    } catch (reason) {
      const message = reason instanceof Error ? reason.message : "模型显示名称保存失败";
      if ((reason as { code?: number } | null)?.code === 409) {
        // CAS 冲突保留输入草稿；用户刷新目录确认新版本后再提交，不覆盖并发修改。
        setConflict(true);
        setError("目录或显示名称已被其他管理员修改。刷新目录后会保留你的草稿，再确认并重新保存。");
      } else {
        setError(message);
      }
    } finally {
      actionPending.current = false;
      setBusyId("");
    }
  };

  if (!isRoot) return <Notice tone="stop" k="无权访问">模型显示设置仅限 root 管理员。</Notice>;

  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">下发 / 客户端目录</p>
          <h1>模型展示</h1>
        </div>
        <button className="btn btn-ghost btn-sm" type="button" disabled={loading || Boolean(busyId)} onClick={() => void load(true)}>
          {loading ? "读取中…" : "刷新目录"}
        </button>
      </header>
      <p className="readout">设置 admin / user 在模型目录、创作工具和用量页面看到的名称。上游 ID、价格及已接受任务保持原样。</p>
      {error ? <Notice tone="stop" k={conflict ? "版本冲突" : "操作未完成"}>{error}</Notice> : null}
      {conflict ? <button className="btn btn-sm" type="button" disabled={loading} onClick={() => void load(true)}>刷新目录并保留草稿</button> : null}
      {loading && entries.length === 0 ? <Notice k="模型展示">正在读取目录…</Notice> : null}
      {!loading && entries.length === 0 ? <Notice k="模型展示">目录中暂无可配置模型。</Notice> : null}
      <section className="pane d1" aria-label="模型显示字段">
        <div className="pane-h"><h2>对外显示字段</h2><span className="readout">{entries.length} 个逻辑模型</span></div>
        <div className="pane-b flush table-scroll">
          <table className="table model-display-table">
            <thead><tr><th>模型显示名称</th><th>类型</th><th>分辨率</th><th>实际档位</th><th>操作</th></tr></thead>
            <tbody>{entries.map((entry) => {
              const draft = drafts[entry.id] ?? entry.displayName;
              const unchanged = draft.trim() === entry.displayName;
              return <tr key={entry.id}>
                <td><form className="form-row" onSubmit={(event) => { event.preventDefault(); void save(entry); }}>
                  <input className="input" aria-label={`${entry.displayName} 显示名称`} value={draft} disabled={Boolean(busyId)} onChange={(event) => setDrafts((previous) => ({ ...previous, [entry.id]: event.target.value }))} />
                </form></td>
                <td><Chip tone={entry.kind === "text" ? "mute" : "info"}>{kindLabel[entry.kind]}</Chip></td>
                <td className="readout">{entry.resolutions.length ? entry.resolutions.join(" · ") : "—"}</td>
                <td><details><summary>{entry.modelIds.length} 个配置项</summary><ul>{entry.modelIds.map((modelId) => <li key={modelId}><code>{modelId}</code></li>)}</ul></details></td>
                <td><button className="btn btn-sm" type="button" disabled={Boolean(busyId) || loading || conflict || unchanged} onClick={() => void save(entry)}>{busyId === entry.id ? "保存中…" : "保存名称"}</button></td>
              </tr>;
            })}</tbody>
          </table>
        </div>
      </section>
      <section className="pane d1" aria-label="admin / user 预览">
        <div className="pane-h"><h2>admin / user 预览</h2><span className="readout">不会显示真实模型 ID</span></div>
        <div className="pane-b"><ul className="model-display-preview">{entries.map((entry) => <li key={entry.id} className="form-row"><strong>{drafts[entry.id]?.trim() || entry.displayName}</strong><Chip tone={entry.kind === "text" ? "mute" : "info"}>{kindLabel[entry.kind]}</Chip>{entry.resolutions.length ? <span className="readout">{entry.resolutions.join(" · ")}</span> : null}</li>)}</ul></div>
      </section>
    </div>
  );
}
