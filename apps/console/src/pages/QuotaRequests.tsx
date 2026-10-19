import { formatCreditsDisplay } from "@shucode-platform/contracts/credits";
import { useCallback, useEffect, useState } from "react";
import { api } from "../api.js";
import { useAuth } from "../auth.js";
import { confirmDialog, toastErr, toastOk } from "../components/Feedback.js";
import { Chip, Notice } from "../components/StatusChip.js";
import { formatShanghaiTime } from "../time.js";

interface QuotaRequest {
  id: string;
  teamId: string;
  requestedBy: string;
  amountCredits: string;
  reason: string;
  status: "pending" | "approved" | "rejected";
  reviewedBy?: string;
  reviewNote: string;
  createdAt: string;
  reviewedAt?: string;
}
export function QuotaRequestsPage() {
  const { isRoot, teamId, teams, refreshSession } = useAuth();
  const [requests, setRequests] = useState<QuotaRequest[]>([]);
  const [amountCredits, setAmountCredits] = useState("");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    try {
      const data = await api<{ requests: QuotaRequest[] }>("/quota-requests");
      setRequests(data.requests);
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : "加载失败");
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);
  const review = async (
    request: QuotaRequest,
    decision: "approve" | "reject",
  ) => {
    const answer = await confirmDialog({
      title:
        decision === "approve"
          ? `批准增加 ¥${formatCreditsDisplay(Number(request.amountCredits))} 额度？`
          : "拒绝本次额度申请？",
      message:
        decision === "approve"
          ? "批准后将一次性增加团队额度。请确认线下审批依据已齐备。"
          : "拒绝后不会增加额度，团队管理员可查看处理说明。",
      confirmText: decision === "approve" ? "批准" : "拒绝",
      danger: decision === "reject",
      input: { label: "审批说明", placeholder: "处理依据或说明（可选）" },
    });
    if (!answer.confirmed) return;
    setBusy(true);
    try {
      await api(`/quota-requests/${request.id}/review`, {
        method: "POST",
        body: JSON.stringify({ decision, note: answer.value }),
      });
      await load();
      await refreshSession();
      toastOk(
        decision === "approve"
          ? "额度申请已批准并增加团队额度"
          : "额度申请已拒绝",
      );
    } catch (e) {
      toastErr(e instanceof Error ? e.message : "审批失败");
    } finally {
      setBusy(false);
    }
  };
  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">额度 / 人工审批</p>
          <h1>{isRoot ? "额度申请审批" : "额度申请"}</h1>
        </div>
        <span className="readout">
          {requests.filter((item) => item.status === "pending").length} 项待处理
        </span>
      </header>
      {error ? (
        <Notice tone="stop" k="加载失败">
          {error}
        </Notice>
      ) : null}
      <Notice k="人工额度申请">
        提交申请供平台管理员审核。申请记录不代表付款或充值凭证；批准后才会增加团队可用总额度。
      </Notice>
      {!isRoot && teamId ? (
        <section className="pane">
          <div className="pane-h">
            <h2>申请增加团队额度</h2>
          </div>
          <form
            className="pane-b tenant-form"
            onSubmit={(event) => {
              event.preventDefault();
              setBusy(true);
              void api(`/teams/${teamId}/quota-requests`, {
                method: "POST",
                body: JSON.stringify({ amountCredits, reason }),
              })
                .then(async () => {
                  setAmountCredits("");
                  setReason("");
                  await load();
                  toastOk("额度申请已提交，等待平台审批");
                })
                .catch((e) =>
                  toastErr(e instanceof Error ? e.message : "提交失败"),
                )
                .finally(() => setBusy(false));
            }}
          >
            <label>
              申请增加金额（¥）
              <input
                className="input"
                type="number"
                min="0.000001"
                step="0.000001"
                required
                value={amountCredits}
                onChange={(e) => setAmountCredits(e.target.value)}
              />
            </label>
            <label>
              申请原因
              <textarea
                className="input"
                required
                maxLength={500}
                rows={3}
                value={reason}
                onChange={(e) => setReason(e.target.value)}
              />
            </label>
            <button className="btn" disabled={busy}>
              {busy ? "提交中…" : "提交人工额度申请"}
            </button>
          </form>
        </section>
      ) : null}
      <section className="pane">
        <div className="pane-h">
          <h2>{isRoot ? "申请记录" : "团队申请历史"}</h2>
          <button
            className="btn btn-ghost btn-sm"
            disabled={busy}
            onClick={() => void load()}
          >
            刷新
          </button>
        </div>
        <div className="table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th>团队</th>
                <th>申请金额</th>
                <th>申请原因</th>
                <th>申请时间</th>
                <th>状态</th>
                <th>处理说明</th>
                {isRoot ? <th>审批</th> : null}
              </tr>
            </thead>
            <tbody>
              {requests.map((request) => (
                <tr key={request.id}>
                  <td>
                    {teams.find((team) => team.id === request.teamId)?.name ??
                      request.teamId}
                  </td>
                  <td className="num">
                    ¥{formatCreditsDisplay(Number(request.amountCredits))}
                  </td>
                  <td
                    style={{
                      minWidth: 160,
                      maxWidth: 320,
                      whiteSpace: "pre-wrap",
                      overflowWrap: "anywhere",
                    }}
                  >
                    {request.reason}
                  </td>
                  <td className="num">
                    {formatShanghaiTime(request.createdAt)}
                  </td>
                  <td>
                    <Chip
                      tone={
                        request.status === "approved"
                          ? "ok"
                          : request.status === "rejected"
                            ? "mute"
                            : "info"
                      }
                    >
                      {request.status === "approved"
                        ? "已批准"
                        : request.status === "rejected"
                          ? "已拒绝"
                          : "待审批"}
                    </Chip>
                  </td>
                  <td>
                    {request.reviewNote || "—"}
                    {request.reviewedAt ? (
                      <div className="readout">
                        {formatShanghaiTime(request.reviewedAt)}
                      </div>
                    ) : null}
                  </td>
                  {isRoot ? (
                    <td>
                      {request.status === "pending" ? (
                        <div className="form-row">
                          <button
                            className="btn btn-sm"
                            disabled={busy}
                            onClick={() => void review(request, "approve")}
                          >
                            批准
                          </button>
                          <button
                            className="btn btn-danger btn-sm"
                            disabled={busy}
                            onClick={() => void review(request, "reject")}
                          >
                            拒绝
                          </button>
                        </div>
                      ) : (
                        "—"
                      )}
                    </td>
                  ) : null}
                </tr>
              ))}
              {requests.length === 0 ? (
                <tr>
                  <td colSpan={isRoot ? 7 : 6} className="readout">
                    暂无额度申请。
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}
