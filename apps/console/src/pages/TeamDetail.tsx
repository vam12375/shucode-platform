import {
  MICRO_CREDITS_PER_CREDIT,
  formatCreditsDisplay,
} from "@shucode-platform/contracts/credits";
import { useCallback, useEffect, useRef, useState } from "react";
import { useAuth } from "../auth.js";
import { Select } from "../components/Select.js";
import { api, navigate } from "../api.js";
import { confirmDialog, toastErr, toastOk } from "../components/Feedback.js";
import { Gauge } from "../components/Gauge.js";
import { Chip, Notice, type ChipTone } from "../components/StatusChip.js";
import { formatShanghaiTime } from "../time.js";
import { UsageModel } from "../components/UsageModel.js";
import type { UsageReasoningParameters } from "../usage-reasoning.js";

interface TeamDetail {
  team: {
    id: string;
    name: string;
    quotaCredits: string;
    usedCredits: string;
    reservedCredits: string;
    unlimited: boolean;
    rpmLimit: number;
    tpmLimit: number;
    concurrencyLimit: number;
  };
  members: Array<{
    user: { id: string; email: string; displayName: string };
    role: "owner" | "admin" | "member";
    joinedAt: string;
  }>;
  aggregates: Array<{
    model: string;
    requests: number;
    inputTokens: number;
    outputTokens: number;
    creditsMicro: number;
  }>;
  recentUsage: Array<UsageReasoningParameters & {
    id: string;
    model: string;
    status: string;
    inputTokens: number;
    outputTokens: number;
    creditsMicro: number;
    createdAt: string;
  }>;
  recentUsageNextCursor: string | null;
  quotaAdjustments: Array<{
    id: string;
    action: string;
    oldQuotaCredits: string;
    newQuotaCredits: string;
    oldUnlimited: boolean;
    newUnlimited: boolean;
    createdAt: string;
    actorEmail: string | null;
  }>;
}

function credits(micro: number): string {
  return formatCreditsDisplay(micro / MICRO_CREDITS_PER_CREDIT);
}

function statusChip(status: string): { tone?: ChipTone; label: string } {
  if (status === "ok") return { tone: "ok", label: "成功" };
  if (status === "aborted") return { tone: "hot", label: "中断" };
  return { tone: "stop", label: "错误" };
}

export function TeamDetailPage({ teamId }: { teamId: string }) {
  const { selectTeam, isRoot } = useAuth();
  const [detail, setDetail] = useState<TeamDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [quota, setQuota] = useState("");
  const [unlimited, setUnlimited] = useState(false);
  const [rpmLimit, setRpmLimit] = useState("0");
  const [tpmLimit, setTpmLimit] = useState("0");
  const [concurrencyLimit, setConcurrencyLimit] = useState("0");
  const [memberEmail, setMemberEmail] = useState("");
  const [busy, setBusy] = useState(false);
  const [addingCredit, setAddingCredit] = useState(false);
  const [creditAmount, setCreditAmount] = useState("");
  const [creditError, setCreditError] = useState<string | null>(null);
  const creditAttempt = useRef<{ amountCredits: string; requestId: string } | null>(null);
  const creditInFlight = useRef(false);

  const load = useCallback(async () => {
    try {
      const data = await api<TeamDetail>(`/teams/${teamId}`);
      setDetail(data);
      setQuota(data.team.quotaCredits);
      setUnlimited(data.team.unlimited);
      setRpmLimit(String(data.team.rpmLimit ?? 0));
      setTpmLimit(String(data.team.tpmLimit ?? 0));
      setConcurrencyLimit(String(data.team.concurrencyLimit ?? 0));
      setError(null);
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载失败");
    }
  }, [teamId]);

  useEffect(() => {
    void load();
  }, [load]);

  const loadMoreUsage = async (): Promise<void> => {
    const cursor = detail?.recentUsageNextCursor;
    if (!cursor || busy) return;
    setBusy(true);
    setError(null);
    try {
      const data = await api<TeamDetail>(
        `/teams/${teamId}?usageCursor=${encodeURIComponent(cursor)}`,
      );
      setDetail((previous) =>
        previous
          ? {
              ...previous,
              recentUsage: [...previous.recentUsage, ...data.recentUsage],
              recentUsageNextCursor: data.recentUsageNextCursor,
            }
          : data,
      );
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载失败");
    } finally {
      setBusy(false);
    }
  };

  // 操作结果走全局 Toast；内联 error 仅保留给页面加载失败。
  const run = async (action: () => Promise<unknown>, success?: string): Promise<void> => {
    setBusy(true);
    try {
      await action();
      await load();
      if (success) toastOk(success);
    } catch (actionError) {
      toastErr(actionError instanceof Error ? actionError.message : "操作失败");
    } finally {
      setBusy(false);
    }
  };

  const addCredit = async (): Promise<void> => {
    if (busy || creditInFlight.current) return;
    const amountCredits = creditAmount.trim();
    if (!/^\d{1,12}(?:\.\d{1,6})?$/.test(amountCredits) || Number(amountCredits) <= 0) {
      setCreditError("请输入大于 0 的 credit 金额，最多 6 位小数");
      return;
    }
    // 网络结果不明时保留同一操作 ID，重试不能重复增加余额。
    if (creditAttempt.current?.amountCredits !== amountCredits) {
      creditAttempt.current = { amountCredits, requestId: crypto.randomUUID() };
    }
    creditInFlight.current = true;
    try {
      await run(
        async () => {
          await api(`/teams/${teamId}/credits`, {
            method: "POST",
            body: JSON.stringify(creditAttempt.current),
          });
          creditAttempt.current = null;
          setCreditAmount("");
          setAddingCredit(false);
          setCreditError(null);
        },
        `已添加 ${formatCreditsDisplay(amountCredits)} credit`,
      );
    } finally {
      creditInFlight.current = false;
    }
  };

  if (!detail) {
    return (
      <div>
        <p className="eyebrow">
          <a href="#/org">← 返回团队与账号</a>
        </p>
        {error ? (
          <Notice tone="stop" k="加载失败">
            {error}
          </Notice>
        ) : (
          <p className="readout">加载中…</p>
        )}
      </div>
    );
  }

  const { team, members, aggregates, recentUsage, quotaAdjustments } = detail;
  const used = Number.parseFloat(team.usedCredits) || 0;
  const quotaValue = Number.parseFloat(team.quotaCredits) || 0;
  const totalCredits = aggregates.reduce((sum, row) => sum + row.creditsMicro, 0);
  const totalRequests = aggregates.reduce((sum, row) => sum + row.requests, 0);

  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">
            <a href="#/org">← 返回团队与账号</a>
          </p>
          <h1>{team.name}</h1>
        </div>
        <span className="readout m0">
          共 {totalRequests.toLocaleString("en-US")} 次请求 · {credits(totalCredits)} credit
        </span>
      </header>

      {error ? (
        <Notice tone="stop" k="操作未完成">
          {error}
        </Notice>
      ) : null}

      <section className="pane">
        <div className="pane-h">
          <h2>额度</h2>
          <span className="readout">实时 · 网关计量</span>
        </div>
        <div className="pane-b">
          <div style={{ marginBottom: 14 }}>
            <Gauge used={used} quota={team.unlimited ? null : quotaValue} />
          </div>
          <div className="form-row">
            <input
              className="input num"
              style={{ width: 160 }}
              value={quota}
              onChange={(event) => setQuota(event.target.value)}
              placeholder="额度 credit"
              aria-label="额度 credit"
              disabled={unlimited}
            />
            <label className="form-row" style={{ gap: 8 }}>
              <span className="switch">
                <input
                  type="checkbox"
                  aria-label="不限额度"
                  checked={unlimited}
                  onChange={(event) => setUnlimited(event.target.checked)}
                />
                <i />
              </span>
              <span style={{ fontSize: 13 }}>不限额度</span>
            </label>
            <button
              className="btn"
              disabled={busy}
              onClick={() =>
                void run(
                  () =>
                    api(`/teams/${teamId}`, {
                      method: "PATCH",
                      body: JSON.stringify({ quotaCredits: quota, unlimited }),
                    }),
                  unlimited ? "已保存额度设置（不限额度）" : "已保存额度设置",
                )
              }
            >
              保存
            </button>
            {isRoot ? (
              <button
                className="btn btn-ghost"
                type="button"
                disabled={busy}
                onClick={() => setAddingCredit((open) => !open)}
                aria-expanded={addingCredit}
              >
                添加余额
              </button>
            ) : null}
          </div>
          {addingCredit && isRoot ? (
            <form
              onSubmit={(event) => {
                event.preventDefault();
                void addCredit();
              }}
              style={{ marginTop: 14 }}
            >
              <div className="form-row">
                <label htmlFor="team-credit-amount" className="readout">
                  添加金额
                </label>
                <input
                  id="team-credit-amount"
                  className="input num"
                  inputMode="decimal"
                  autoFocus
                  placeholder="例如 10"
                  value={creditAmount}
                  disabled={busy}
                  onChange={(event) => {
                    setCreditAmount(event.target.value);
                    setCreditError(null);
                  }}
                  aria-describedby="team-credit-help"
                  aria-invalid={Boolean(creditError)}
                />
                <span className="readout">credit</span>
                <button className="btn" type="submit" disabled={busy || !creditAmount.trim()}>
                  确认添加
                </button>
                <button
                  className="btn btn-ghost"
                  type="button"
                  disabled={busy}
                  onClick={() => setAddingCredit(false)}
                >
                  取消
                </button>
              </div>
              <p id="team-credit-help" className="readout" style={{ marginTop: 8 }}>
                增加团队总额度，保留已用金额；操作将记入额度流水。
              </p>
              {creditError ? (
                <p role="alert" className="dialog-err">
                  {creditError}
                </p>
              ) : null}
            </form>
          ) : null}
          <p className="readout" style={{ margin: 0, marginTop: 10 }}>
            可用余额{" "}
            {team.unlimited
              ? "不限"
              : `${formatCreditsDisplay(quotaValue - used)} credit`}{" "}
            · 已用 {formatCreditsDisplay(used)} credit。按已结算用量扣减，页面展示为实时汇总。
          </p>
        </div>
      </section>

      <section className="pane d1">
        <div className="pane-h">
          <h2>网关流控</h2>
          <span className="readout">0 = 不限制 · 由网关执行</span>
        </div>
        <div className="pane-b">
          <div className="form-row">
            <label className="form-row" style={{ gap: 6 }}>
              <span className="readout">RPM</span>
              <input
                className="input num"
                style={{ width: 120 }}
                value={rpmLimit}
                onChange={(event) => setRpmLimit(event.target.value)}
                aria-label="每分钟请求数上限"
              />
            </label>
            <label className="form-row" style={{ gap: 6 }}>
              <span className="readout">TPM</span>
              <input
                className="input num"
                style={{ width: 140 }}
                value={tpmLimit}
                onChange={(event) => setTpmLimit(event.target.value)}
                aria-label="每分钟 Token 上限"
              />
            </label>
            <label className="form-row" style={{ gap: 6 }}>
              <span className="readout">并发</span>
              <input
                className="input num"
                style={{ width: 110 }}
                value={concurrencyLimit}
                onChange={(event) => setConcurrencyLimit(event.target.value)}
                aria-label="并发请求上限"
              />
            </label>
            <button
              className="btn"
              disabled={busy}
              onClick={() =>
                void run(
                  () =>
                    api(`/teams/${teamId}`, {
                      method: "PATCH",
                      body: JSON.stringify({
                        rpmLimit: Number.parseInt(rpmLimit, 10) || 0,
                        tpmLimit: Number.parseInt(tpmLimit, 10) || 0,
                        concurrencyLimit: Number.parseInt(concurrencyLimit, 10) || 0,
                      }),
                    }),
                  "已保存流控设置",
                )
              }
            >
              保存流控
            </button>
          </div>
          <p className="readout" style={{ margin: 0, marginTop: 10 }}>
            RPM/TPM 按分钟窗口计数，并发按在途请求数计数；超限返回 429。TPM 使用请求体估算
            token（正文长度 / 4 + max_tokens），用于准入保护。
          </p>
        </div>
      </section>

      <section className="pane d1">
        <div className="pane-h">
          <h2>额度变更记录</h2>
          <span className="readout">最近 {quotaAdjustments.length} 条 · 含前后快照</span>
        </div>
        <div className="pane-b flush table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">时间</th>
                <th scope="col">操作者</th>
                <th scope="col">额度变化</th>
                <th scope="col">不限额度</th>
              </tr>
            </thead>
            <tbody>
              {quotaAdjustments.map((row) => (
                <tr key={row.id}>
                  <td className="num">{formatShanghaiTime(row.createdAt)}</td>
                  <td>{row.actorEmail ?? "—"}</td>
                  <td className="num">
                    {row.oldQuotaCredits} → {row.newQuotaCredits}
                  </td>
                  <td>
                    {row.oldUnlimited === row.newUnlimited ? (
                      <span className="readout">{row.newUnlimited ? "是" : "否"}</span>
                    ) : (
                      <Chip tone={row.newUnlimited ? "hot" : undefined}>
                        {row.oldUnlimited ? "是 → 否" : "否 → 是"}
                      </Chip>
                    )}
                  </td>
                </tr>
              ))}
              {quotaAdjustments.length === 0 ? (
                <tr>
                  <td colSpan={4} className="readout">
                    暂无调整记录。
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </section>

      <section className="pane d1">
        <div className="pane-h">
          <h2>成员</h2>
          <button
            className="btn btn-ghost btn-sm"
            onClick={() => {
              selectTeam(teamId);
              navigate("/members");
            }}
          >
            团队工作台 · 邀请与预算
          </button>
          <span className="readout">{members.length} 人</span>
        </div>
        <div className="pane-b">
          <div className="form-row" style={{ marginBottom: 10 }}>
            <input
              className="input grow"
              placeholder="按邮箱添加成员（需已创建用户）"
              value={memberEmail}
              onChange={(event) => setMemberEmail(event.target.value)}
            />
            <button
              className="btn btn-ghost"
              disabled={busy || !memberEmail.trim()}
              onClick={() =>
                void run(async () => {
                  await api(`/teams/${teamId}/members`, {
                    method: "POST",
                    body: JSON.stringify({ email: memberEmail.trim() }),
                  });
                  setMemberEmail("");
                }, `已添加成员 ${memberEmail.trim()}`)
              }
            >
              添加
            </button>
          </div>
        </div>
        <div className="table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">成员</th>
                <th scope="col">角色</th>
                <th scope="col" aria-label="操作" />
              </tr>
            </thead>
            <tbody>
              {members.map((member) => (
                <tr key={member.user.id}>
                  <td>
                    {member.user.displayName} <span className="readout">({member.user.email})</span>
                  </td>
                  <td>
                    <Select
                      ariaLabel={`${member.user.email}的团队身份`}
                      value={member.role}
                      disabled={busy}
                      options={[
                        { value: "owner", label: "所有者" },
                        { value: "admin", label: "团队管理员" },
                        { value: "member", label: "成员" },
                      ]}
                      onChange={(role) =>
                        void run(
                          () =>
                            api(`/teams/${teamId}/members/${member.user.id}`, {
                              method: "PATCH",
                              body: JSON.stringify({ role }),
                            }),
                          "团队身份已更新",
                        )
                      }
                    />
                  </td>
                  <td>
                    <div className="form-row" style={{ justifyContent: "flex-end" }}>
                      <button
                        className="btn btn-danger btn-sm"
                        disabled={busy}
                        onClick={() => {
                          void confirmDialog({
                            k: "移除成员",
                            title: `将 ${member.user.displayName} 移出「${team.name}」？`,
                            message:
                              "移除后该用户不再共享本团队额度；其设备凭据与账号本身不受影响。",
                            confirmText: "移除",
                            danger: true,
                          }).then((answer) => {
                            if (!answer.confirmed) return;
                            return run(
                              () =>
                                api(`/teams/${teamId}/members/${member.user.id}`, {
                                  method: "DELETE",
                                }),
                              `已移除 ${member.user.displayName}`,
                            );
                          });
                        }}
                      >
                        移除
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
              {members.length === 0 ? (
                <tr>
                  <td colSpan={3} className="readout">
                    还没有成员。
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </section>

      <section className="pane d2">
        <div className="pane-h">
          <h2>模型用量汇总</h2>
          <span className="readout">网关计量 · 团队账单口径</span>
        </div>
        <div className="pane-b flush table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">模型</th>
                <th scope="col" className="num">
                  请求数
                </th>
                <th scope="col" className="num">
                  输入 Token
                </th>
                <th scope="col" className="num">
                  输出 Token
                </th>
                <th scope="col" className="num">
                  积分（credit）
                </th>
              </tr>
            </thead>
            <tbody>
              {aggregates.map((row) => (
                <tr key={row.model}>
                  <td>{row.model}</td>
                  <td className="num">{row.requests.toLocaleString("en-US")}</td>
                  <td className="num">{row.inputTokens.toLocaleString("en-US")}</td>
                  <td className="num">{row.outputTokens.toLocaleString("en-US")}</td>
                  <td className="num">{credits(row.creditsMicro)}</td>
                </tr>
              ))}
              {aggregates.length === 0 ? (
                <tr>
                  <td colSpan={5} className="readout">
                    暂无用量。
                  </td>
                </tr>
              ) : null}
            </tbody>
            {aggregates.length > 0 ? (
              <tfoot>
                <tr>
                  <td>合计 · {aggregates.length} 个模型</td>
                  <td className="num">{totalRequests.toLocaleString("en-US")}</td>
                  <td className="num">
                    {aggregates
                      .reduce((sum, row) => sum + row.inputTokens, 0)
                      .toLocaleString("en-US")}
                  </td>
                  <td className="num">
                    {aggregates
                      .reduce((sum, row) => sum + row.outputTokens, 0)
                      .toLocaleString("en-US")}
                  </td>
                  <td className="num">{credits(totalCredits)}</td>
                </tr>
              </tfoot>
            ) : null}
          </table>
        </div>
      </section>

      <section className="pane d2">
        <div className="pane-h">
          <h2>最近请求</h2>
          <span className="readout">每页 50 条</span>
        </div>
        <div className="pane-b flush table-scroll vscroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">时间</th>
                <th scope="col">模型</th>
                <th scope="col">状态</th>
                <th scope="col" className="num">
                  Token（入 / 出）
                </th>
                <th scope="col" className="num">
                  积分（credit）
                </th>
              </tr>
            </thead>
            <tbody>
              {recentUsage.map((record) => {
                const status = statusChip(record.status);
                return (
                  <tr key={record.id}>
                    <td className="num">{formatShanghaiTime(record.createdAt)}</td>
                    <td><UsageModel record={record} /></td>
                    <td>
                      <Chip tone={status.tone}>{status.label}</Chip>
                    </td>
                    <td className="num">
                      {record.inputTokens.toLocaleString("en-US")} /{" "}
                      {record.outputTokens.toLocaleString("en-US")}
                    </td>
                    <td className="num">{credits(record.creditsMicro)}</td>
                  </tr>
                );
              })}
              {recentUsage.length === 0 ? (
                <tr>
                  <td colSpan={5} className="readout">
                    暂无请求记录。
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
        {detail.recentUsageNextCursor ? (
          <div className="pane-b">
            <button
              className="btn btn-ghost"
              type="button"
              disabled={busy}
              onClick={() => void loadMoreUsage()}
            >
              加载更多
            </button>
          </div>
        ) : null}
      </section>
    </div>
  );
}
