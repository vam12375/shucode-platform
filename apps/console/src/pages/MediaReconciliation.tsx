import {
  MICRO_CREDITS_PER_CREDIT,
  formatCreditsDisplay,
} from "@shucode-platform/contracts/credits";
import {
  useCallback,
  useEffect,
  useState,
  type FormEvent,
} from "react";
import { api } from "../api.js";
import { toastOk } from "../components/Feedback.js";
import { Select } from "../components/Select.js";
import { Chip, Notice } from "../components/StatusChip.js";
import { formatShanghaiTime } from "../time.js";

interface PendingShot {
  status: string;
  stage?: string;
  attempt?: number;
  reservedMicro?: number;
}

interface PendingProject {
  id: string;
  teamId: string;
  version: number;
  status: string;
  brief: {
    product: string;
    videoModel: string;
    imageModel?: string;
    resolution: string;
    duration: number;
  };
  shots: PendingShot[];
  updatedAt: string;
  quoteMicro: number;
}

interface PendingRow {
  project: PendingProject;
  index: number;
  shot: PendingShot;
}

type Outcome = "video_task" | "image_result" | "not_accepted";

const OUTCOME_OPTIONS = [
  { value: "video_task", label: "视频已受理，补录 task ID" },
  { value: "image_result", label: "图片已生成，补录结果 URL" },
  { value: "not_accepted", label: "确认未受理，释放预留" },
];

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "操作失败";
}

function stageLabel(stage?: string): string {
  if (stage === "video") return "视频提交";
  if (stage === "image") return "图片生成";
  return "提交结果未知";
}

function credits(micro?: number): string {
  if (typeof micro !== "number" || micro <= 0) return "—";
  return `${formatCreditsDisplay(micro / MICRO_CREDITS_PER_CREDIT)} credit`;
}

function pendingRowsOf(projects: PendingProject[]): PendingRow[] {
  return projects
    .flatMap((project) =>
      project.shots
        .map((shot, index) => ({ project, index, shot }))
        .filter((row) => row.shot.status === "reconciliation_required"),
    )
    .sort(
      (left, right) =>
        Date.parse(right.project.updatedAt) - Date.parse(left.project.updatedAt),
    );
}

function ReconcileDrawer({
  row,
  onClose,
  onDone,
}: {
  row: PendingRow;
  onClose: () => void;
  onDone: () => void;
}) {
  const [outcome, setOutcome] = useState<Outcome>("video_task");
  const [value, setValue] = useState("");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const { project, shot } = row;

  useEffect(() => {
    const onKey = (event: KeyboardEvent): void => {
      if (event.key === "Escape" && !busy) onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [busy, onClose]);

  const submit = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api(`/media/projects/${project.teamId}/${project.id}/reconcile`, {
        method: "POST",
        body: JSON.stringify({
          version: project.version,
          shot: row.index,
          outcome,
          value,
          reason,
        }),
      });
      toastOk(`已提交镜头 ${row.index + 1} 的核对结果`, "已提交");
      onDone();
    } catch (submitError) {
      setError(errorMessage(submitError));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <div className="drawer-backdrop" onClick={onClose} aria-hidden="true" />
      <aside
        className="drawer"
        role="dialog"
        aria-modal="true"
        aria-labelledby="reconcile-title"
      >
        <header className="drawer-h">
          <div className="drawer-title">
            <Chip tone="hot">待核对</Chip>
            <h3 id="reconcile-title">核对镜头 {row.index + 1}</h3>
            <p className="readout">
              {project.brief.product} · {project.id}
            </p>
          </div>
          <div className="drawer-actions">
            <button
              className="btn btn-ghost btn-sm"
              type="button"
              disabled={busy}
              onClick={onClose}
            >
              关闭
            </button>
          </div>
        </header>

        <div className="drawer-b">
          <section className="drawer-sec">
            <h4 className="drawer-sec-title">制作单信息</h4>
            <p className="readout drawer-note">
              商品 <b>{project.brief.product}</b> · 版本 <b>v{project.version}</b> · 阶段{" "}
              <b>{stageLabel(shot.stage)}</b>
            </p>
            <p className="readout drawer-note">
              尝试 <b>{shot.attempt ?? 1}</b> 次 · 本镜头预留{" "}
              <b>{credits(shot.reservedMicro)}</b> · 制作单报价{" "}
              <b>{credits(project.quoteMicro)}</b>
            </p>
            <p className="readout drawer-note">
              视频模型 <b>{project.brief.videoModel}</b> · {project.brief.resolution} ·{" "}
              {project.brief.duration}s
            </p>
          </section>

          <form className="form-stack" onSubmit={(event) => void submit(event)}>
            <Notice>
              请先在供应商后台核实受理记录，再提交结果。提交会写入平台审计记录，不会自动重发请求。
            </Notice>
            {error ? (
              <Notice tone="stop" k="提交失败">
                {error}
              </Notice>
            ) : null}

            <label className="field">
              <span className="field-label">供应商核实结果</span>
              <Select
                ariaLabel="供应商核实结果"
                value={outcome}
                onChange={(next) => setOutcome(next as Outcome)}
                options={OUTCOME_OPTIONS}
              />
            </label>

            {outcome === "not_accepted" ? (
              <Notice tone="stop">
                确认未受理会释放本镜头的预留额度。请确保供应商后台确实没有对应的受理记录。
              </Notice>
            ) : (
              <label className="field">
                <span className="field-label">
                  {outcome === "video_task" ? "视频 Task ID" : "图片结果 URL"}
                </span>
                <input
                  className="input"
                  required
                  autoFocus
                  value={value}
                  placeholder={
                    outcome === "video_task"
                      ? "供应商返回的视频任务 ID"
                      : "可直接下载的图片结果地址"
                  }
                  onChange={(event) => setValue(event.target.value)}
                />
              </label>
            )}

            <label className="field">
              <span className="field-label">
                核对依据（至少 5 字，进入审计记录，请勿填写密钥）
              </span>
              <textarea
                className="reconcile-reason"
                required
                minLength={5}
                maxLength={1000}
                value={reason}
                placeholder="如：已在供应商控制台查到任务并确认受理"
                onChange={(event) => setReason(event.target.value)}
              />
            </label>

            <div className="form-row">
              <button className="btn" type="submit" disabled={busy}>
                {busy ? "提交中…" : "提交核对结果"}
              </button>
              <button
                className="btn btn-ghost"
                type="button"
                disabled={busy}
                onClick={onClose}
              >
                取消
              </button>
            </div>
          </form>
        </div>
      </aside>
    </>
  );
}

export function MediaReconciliation() {
  const [projects, setProjects] = useState<PendingProject[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState<PendingRow | null>(null);

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      setProjects(await api<PendingProject[]>("/media/projects"));
      setError("");
    } catch (loadError) {
      setError(errorMessage(loadError));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const rows = pendingRowsOf(projects);

  return (
    <>
      <section className="pane d2">
        <div className="pane-h">
          <h2>供应商提交核对</h2>
          <div className="form-row">
            <span className="readout">{rows.length} 个镜头待核对</span>
            <button
              className="btn btn-ghost btn-sm"
              type="button"
              disabled={loading}
              onClick={() => void refresh()}
            >
              刷新
            </button>
          </div>
        </div>

        <div className="pane-b flush">
          {error ? (
            <div className="pane-b">
              <Notice tone="stop" k="核对列表加载失败">
                {error}
              </Notice>
            </div>
          ) : loading ? (
            <div className="pane-b">
              <p className="readout">正在加载待核对镜头…</p>
            </div>
          ) : rows.length === 0 ? (
            <div className="pane-b">
              <div className="empty" style={{ margin: "8px auto" }}>
                <h2>暂无待核对任务</h2>
                <p>
                  只有上游响应丢失、需要人工核实受理结果的付费镜头会出现在这里。
                  <br />
                  正常制作与已结算镜头不会进入本列表。
                </p>
              </div>
            </div>
          ) : (
            <div className="table-scroll">
              <table className="table">
                <thead>
                  <tr>
                    <th scope="col">制作单</th>
                    <th scope="col">镜头</th>
                    <th scope="col">阶段</th>
                    <th scope="col">预留</th>
                    <th scope="col">更新时间</th>
                    <th scope="col" aria-label="操作" />
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row) => (
                    <tr key={`${row.project.id}:${row.index}`}>
                      <td>
                        <b>{row.project.brief.product}</b>
                        <div className="readout">{row.project.id}</div>
                      </td>
                      <td className="num">
                        镜头 {row.index + 1} · v{row.project.version}
                      </td>
                      <td>{stageLabel(row.shot.stage)}</td>
                      <td className="num">{credits(row.shot.reservedMicro)}</td>
                      <td className="readout">
                        {formatShanghaiTime(row.project.updatedAt).slice(5, 16)}
                      </td>
                      <td style={{ textAlign: "right" }}>
                        <button
                          className="btn btn-ghost btn-sm"
                          type="button"
                          onClick={() =>
                            setSelected({
                              project: row.project,
                              index: row.index,
                              shot: row.shot,
                            })
                          }
                        >
                          核对
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </section>

      {selected ? (
        <ReconcileDrawer
          row={selected}
          onClose={() => setSelected(null)}
          onDone={() => {
            setSelected(null);
            void refresh();
          }}
        />
      ) : null}
    </>
  );
}
