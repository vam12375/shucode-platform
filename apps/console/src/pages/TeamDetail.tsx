import { formatCreditsDisplay, formatMicroCredits } from "@shucode-platform/contracts/credits";
import { useCallback, useEffect, useState } from "react";
import { api, navigate } from "../api.js";

interface TeamDetail {
  team: {
    id: string;
    name: string;
    quotaCredits: string;
    usedCredits: string;
    reservedCredits: string;
    unlimited: boolean;
  };
  members: Array<{
    user: { id: string; email: string; displayName: string };
    role: "owner" | "member";
    joinedAt: string;
  }>;
  aggregates: Array<{
    model: string;
    requests: number;
    inputTokens: number;
    outputTokens: number;
    creditsMicro: number;
  }>;
  recentUsage: Array<{
    id: string;
    model: string;
    status: string;
    inputTokens: number;
    outputTokens: number;
    creditsMicro: number;
    createdAt: string;
  }>;
}

export function TeamDetailPage({ teamId }: { teamId: string }) {
  const [detail, setDetail] = useState<TeamDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [quota, setQuota] = useState("");
  const [unlimited, setUnlimited] = useState(false);
  const [memberEmail, setMemberEmail] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const data = await api<TeamDetail>(`/teams/${teamId}`);
      setDetail(data);
      setQuota(data.team.quotaCredits);
      setUnlimited(data.team.unlimited);
      setError(null);
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载失败");
    }
  }, [teamId]);

  useEffect(() => {
    void load();
  }, [load]);

  const run = async (action: () => Promise<unknown>): Promise<void> => {
    setBusy(true);
    setError(null);
    try {
      await action();
      await load();
    } catch (actionError) {
      setError(actionError instanceof Error ? actionError.message : "操作失败");
    } finally {
      setBusy(false);
    }
  };

  if (!detail) {
    return (
      <div>
        <button className="secondary" onClick={() => navigate("/teams")}>
          ← 返回团队列表
        </button>
        {error ? <p className="error">{error}</p> : <p className="muted">加载中…</p>}
      </div>
    );
  }

  const { team, members, aggregates, recentUsage } = detail;
  const used = Number.parseFloat(team.usedCredits) || 0;

  return (
    <div>
      <p>
        <a href="#/teams" className="muted">
          ← 返回团队列表
        </a>
      </p>
      <h1>{team.name}</h1>
      {error ? <p className="error">{error}</p> : null}

      <div className="card">
        <h2 style={{ marginTop: 0 }}>额度</h2>
        <div className="row">
          <span className="mono">已用 {formatCreditsDisplay(team.usedCredits)}</span>
          <span className="mono muted">预留 {formatCreditsDisplay(team.reservedCredits)}</span>
          <input
            style={{ width: 160 }}
            value={quota}
            onChange={(event) => setQuota(event.target.value)}
            placeholder="额度 credit"
          />
          <label className="row" style={{ gap: 6 }}>
            <input
              type="checkbox"
              checked={unlimited}
              onChange={(event) => setUnlimited(event.target.checked)}
            />
            不限额度
          </label>
          <button
            disabled={busy}
            onClick={() =>
              void run(() =>
                api(`/teams/${teamId}`, {
                  method: "PATCH",
                  body: JSON.stringify({ quotaCredits: quota, unlimited }),
                }),
              )
            }
          >
            保存
          </button>
        </div>
        <p className="muted" style={{ marginBottom: 0 }}>
          当前用量 {used.toFixed(4)} credit。额度扣减发生在模型网关，页面展示为实时汇总。
        </p>
      </div>

      <div className="card">
        <h2 style={{ marginTop: 0 }}>成员</h2>
        <div className="row" style={{ marginBottom: 10 }}>
          <input
            className="grow"
            placeholder="按邮箱添加成员（需已创建用户）"
            value={memberEmail}
            onChange={(event) => setMemberEmail(event.target.value)}
          />
          <button
            className="secondary"
            disabled={busy || !memberEmail.trim()}
            onClick={() =>
              void run(async () => {
                await api(`/teams/${teamId}/members`, {
                  method: "POST",
                  body: JSON.stringify({ email: memberEmail.trim() }),
                });
                setMemberEmail("");
              })
            }
          >
            添加
          </button>
        </div>
        <table>
          <thead>
            <tr>
              <th>成员</th>
              <th>角色</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {members.map((member) => (
              <tr key={member.user.id}>
                <td>
                  {member.user.displayName} <span className="muted">({member.user.email})</span>
                </td>
                <td>{member.role === "owner" ? "所有者" : "成员"}</td>
                <td style={{ textAlign: "right" }}>
                  <button
                    className="danger"
                    disabled={busy}
                    onClick={() =>
                      void run(() =>
                        api(`/teams/${teamId}/members/${member.user.id}`, { method: "DELETE" }),
                      )
                    }
                  >
                    移除
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="card">
        <h2 style={{ marginTop: 0 }}>模型用量汇总</h2>
        <table>
          <thead>
            <tr>
              <th>模型</th>
              <th>请求数</th>
              <th>输入 Token</th>
              <th>输出 Token</th>
              <th>费用（credit）</th>
            </tr>
          </thead>
          <tbody>
            {aggregates.map((row) => (
              <tr key={row.model}>
                <td className="mono">{row.model}</td>
                <td>{row.requests}</td>
                <td>{row.inputTokens.toLocaleString()}</td>
                <td>{row.outputTokens.toLocaleString()}</td>
                <td className="mono">{formatMicroCredits(row.creditsMicro)}</td>
              </tr>
            ))}
            {aggregates.length === 0 ? (
              <tr>
                <td colSpan={5} className="muted">
                  暂无用量。
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>

      <div className="card">
        <h2 style={{ marginTop: 0 }}>最近请求</h2>
        <table>
          <thead>
            <tr>
              <th>时间</th>
              <th>模型</th>
              <th>状态</th>
              <th>Token（入/出）</th>
              <th>费用</th>
            </tr>
          </thead>
          <tbody>
            {recentUsage.map((record) => (
              <tr key={record.id}>
                <td className="mono">{record.createdAt.replace("T", " ").slice(0, 19)}</td>
                <td className="mono">{record.model}</td>
                <td>{record.status}</td>
                <td className="mono">
                  {record.inputTokens} / {record.outputTokens}
                </td>
                <td className="mono">{formatMicroCredits(record.creditsMicro)}</td>
              </tr>
            ))}
            {recentUsage.length === 0 ? (
              <tr>
                <td colSpan={5} className="muted">
                  暂无请求记录。
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>
    </div>
  );
}
