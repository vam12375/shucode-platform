import { useEffect, useState } from "react";
import { api } from "../api.js";
import { Notice } from "../components/StatusChip.js";

type Pending = {
  id: string;
  teamId: string;
  version: number;
  brief: { product: string };
  shots: { status: string; stage?: string }[];
};
export function MediaReconciliation() {
  const [projects, setProjects] = useState<Pending[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [selected, setSelected] = useState("");
  const [outcome, setOutcome] = useState("video_task");
  const [value, setValue] = useState("");
  const [reason, setReason] = useState("");
  const refresh = async () => setProjects(await api<Pending[]>("/media/projects"));
  useEffect(() => {
    void refresh().catch((e: Error) => setError(e.message));
  }, []);
  const [id, index] = selected.split(":");
  const project = projects.find((p) => p.id === id);
  return (
    <section className="panel">
      <div className="panel-head">
        <h2>供应商提交核对</h2>
      </div>
      <p>
        响应丢失的付费任务不会自动重发。请先在供应商后台核实受理记录，再补录任务或确认未受理。已受理的任务会继续查询和结算。
      </p>
      {error && (
        <Notice tone="stop" k="核对失败">
          {error}
        </Notice>
      )}
      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (!project) return;
          setBusy(true);
          setError("");
          void api(`/media/projects/${project.teamId}/${project.id}/reconcile`, {
            method: "POST",
            body: JSON.stringify({
              version: project.version,
              shot: Number(index),
              outcome,
              value,
              reason,
            }),
          })
            .then(async () => {
              setSelected("");
              setValue("");
              setReason("");
              await refresh();
            })
            .catch((e: Error) => setError(e.message))
            .finally(() => setBusy(false));
        }}
      >
        <label className="field">
          <span>待核对镜头</span>
          <select required value={selected} onChange={(event) => setSelected(event.target.value)}>
            <option value="">请选择</option>
            {projects.flatMap((p) =>
              p.shots.map((s, i) =>
                s.status === "reconciliation_required" ? (
                  <option key={`${p.id}:${i}`} value={`${p.id}:${i}`}>
                    {p.brief.product} · {p.id} · 镜头 {i + 1} · {s.stage}
                  </option>
                ) : null,
              ),
            )}
          </select>
        </label>
        <label className="field">
          <span>供应商核实结果</span>
          <select value={outcome} onChange={(event) => setOutcome(event.target.value)}>
            <option value="video_task">视频已受理，补录 task ID</option>
            <option value="image_result">图片已生成，补录结果 URL</option>
            <option value="not_accepted">确认未受理，释放预留</option>
          </select>
        </label>
        {outcome !== "not_accepted" && (
          <label className="field">
            <span>Task ID / 图片结果 URL</span>
            <input required value={value} onChange={(event) => setValue(event.target.value)} />
          </label>
        )}
        <label className="field">
          <span>核对依据（至少 5 字，进入审计记录，请勿填写密钥）</span>
          <textarea
            required
            minLength={5}
            maxLength={1000}
            value={reason}
            onChange={(event) => setReason(event.target.value)}
          />
        </label>
        <button className="btn" type="submit" disabled={busy || !selected}>
          提交核对结果
        </button>
      </form>
    </section>
  );
}
