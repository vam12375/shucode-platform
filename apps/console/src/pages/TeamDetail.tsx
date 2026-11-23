import {
  MICRO_CREDITS_PER_CREDIT,
  formatCreditsDisplay,
} from "@shucode-platform/contracts/credits";
import { useCallback, useEffect, useRef, useState } from "react";
import { ArrowLeft, Building2, Users } from "lucide-react";
import { useAuth } from "../auth.js";
import { Select } from "../components/Select.js";
import { api, navigate } from "../api.js";
import { confirmDialog, toastErr, toastOk } from "../components/Feedback.js";
import { Glyph, PageTitle } from "../components/Icon.js";
import { Chip, Notice, type ChipTone } from "../components/StatusChip.js";
import { formatShanghaiTime } from "../time.js";
import { UsageModel } from "../components/UsageModel.js";
import type { UsageReasoningParameters } from "../usage-reasoning.js";
import { resolveUsageChartRange } from "../usage-range.js";
import { CreditBarChart, CreditComposition, CreditRing, MemberCreditRanking, type CreditSample } from "./organization-charts.js";
import "./polar-organization.css";

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
    modelDisplayName?: string;
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

export function TeamDetailPage({ teamId, drawer = false, onUpdated }: { teamId: string; drawer?: boolean; onUpdated?: () => Promise<void> }) {
  const { selectTeam, canManagePlatform } = useAuth();
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
  const [editingQuota, setEditingQuota] = useState(!drawer);
  const [creditAmount, setCreditAmount] = useState("");
  const [creditError, setCreditError] = useState<string | null>(null);
  const creditAttempt = useRef<{ amountCredits: string; requestId: string } | null>(null);
  const creditInFlight = useRef(false);
  const detailRequest = useRef(0);
  const [spendRange] = useState(() => resolveUsageChartRange("30d"));
  const [spend, setSpend] = useState<CreditSample[] | null>(null);
  const [spendError, setSpendError] = useState<string | null>(null);
  const [deviceCount, setDeviceCount] = useState<number | null>(null);
  const [memberRanking, setMemberRanking] = useState<Array<{ id: string; name: string; credits: number; requests: number }> | null>(null);
  const [memberRankingError, setMemberRankingError] = useState<string | null>(null);
  const memberScope = detail?.team.id === teamId ? detail.members.map((member) => `${member.user.id}:${member.user.displayName}:${member.user.email}`).join("|") : "";

  const load = useCallback(async () => {
    const request = ++detailRequest.current;
    try {
      const data = await api<TeamDetail>(`/teams/${teamId}`);
      // 快速切换团队或刷新时，旧请求不能把另一团队的额度与成员带回详情面板。
      if (request !== detailRequest.current) return;
      setDetail(data);
      setQuota(data.team.quotaCredits);
      setUnlimited(data.team.unlimited);
      setRpmLimit(String(data.team.rpmLimit ?? 0));
      setTpmLimit(String(data.team.tpmLimit ?? 0));
      setConcurrencyLimit(String(data.team.concurrencyLimit ?? 0));
      setError(null);
    } catch (loadError) {
      if (request === detailRequest.current) setError(loadError instanceof Error ? loadError.message : "加载失败");
    }
  }, [teamId]);

  useEffect(() => {
    setDetail(null);
    setError(null);
    void load();
    return () => { detailRequest.current += 1; };
  }, [load]);

  useEffect(() => {
    let current = true;
    setSpend(null);
    setSpendError(null);
    const query = new URLSearchParams({ teamId, from: spendRange.queryFrom, to: spendRange.queryTo, granularity: "day" });
    void api<{ series: Array<{ bucketStart: string; creditsMicro: number }> }>(`/usage/series?${query.toString()}`).then((data) => {
      if (!current) return;
      const byDate = new Map(data.series.map((row) => [row.bucketStart, row.creditsMicro / MICRO_CREDITS_PER_CREDIT]));
      setSpend(spendRange.bucketKeys.map((date) => ({ label: date.slice(5, 10), value: byDate.get(date) ?? 0 })));
    }).catch((loadError) => { if (current) setSpendError(loadError instanceof Error ? loadError.message : "趋势加载失败"); });
    return () => { current = false; };
  }, [teamId, spendRange]);

  useEffect(() => {
    let current = true;
    setDeviceCount(null);
    void api<{ devices: Array<{ revokedAt: string | null }> }>(`/devices?teamId=${encodeURIComponent(teamId)}`).then((data) => { if (current) setDeviceCount(data.devices.filter((device) => !device.revokedAt).length); }).catch(() => { if (current) setDeviceCount(null); });
    return () => { current = false; };
  }, [teamId]);

  useEffect(() => {
    let current = true, next = 0;
    setMemberRanking(null);
    setMemberRankingError(null);
    if (!detail || detail.team.id !== teamId) return;
    const members = detail.members;
    const rows: Array<{ id: string; name: string; credits: number; requests: number }> = [];
    const worker = async () => {
      while (current && next < members.length) {
        const member = members[next++]!;
        // limit 只限制明细页；完整聚合用于排行，不把一页明细误当成员总用量。
        const query = new URLSearchParams({ teamId, userId: member.user.id, from: spendRange.queryFrom, to: spendRange.queryTo, limit: "1" });
        const data = await api<{ aggregates: Array<{ requests: number; creditsMicro: number }> }>(`/usage?${query}`);
        rows.push({ id: member.user.id, name: member.user.displayName || member.user.email, credits: data.aggregates.reduce((sum, row) => sum + row.creditsMicro, 0) / MICRO_CREDITS_PER_CREDIT, requests: data.aggregates.reduce((sum, row) => sum + row.requests, 0) });
      }
    };
    void Promise.all(Array.from({ length: Math.min(4, members.length) }, () => worker())).then(() => { if (current) setMemberRanking(rows.sort((a, b) => b.credits - a.credits).slice(0, 7)); }).catch((failure) => { if (current) setMemberRankingError(failure instanceof Error ? failure.message : "成员用量加载失败"); current = false; });
    return () => { current = false; };
  }, [memberScope, teamId, spendRange]);

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
      await onUpdated?.();
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
      <div className="organization-team-detail-loading">
        {!drawer ? <p className="eyebrow">
          <a href="#/org">
            <Glyph icon={ArrowLeft} size={12} strokeWidth={2} />
            返回团队与账号
          </a>
        </p> : null}
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
    <div className={drawer ? "organization-team-detail organization-team-detail-drawer" : "organization-team-detail"}>
      <header className="page-head">
        <div>
          {!drawer ? <p className="eyebrow">
            <a href="#/org">
              <Glyph icon={ArrowLeft} size={12} strokeWidth={2} />
              返回团队与账号
            </a>
          </p> : null}
          <PageTitle icon={Building2}>{team.name}</PageTitle>
          <div className="organization-team-meta"><Chip tone={team.unlimited ? "mute" : quotaValue - used <= 0 ? "stop" : used / quotaValue > 0.85 ? "hot" : "ok"}>{team.unlimited ? "不限" : quotaValue - used <= 0 ? "已停" : used / quotaValue > 0.85 ? "近额" : "生效"}</Chip><span>{members.length} 名成员 · {deviceCount === null ? "—" : deviceCount} 台设备</span></div>
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

      <section className="pane organization-team-quota">
        <div className="pane-h">
          <h2>额度</h2>
          <span className="readout">实时 · 网关计量</span>
        </div>
        <div className="pane-b">
          <div className="organization-team-credit">
            <CreditRing used={used} quota={team.unlimited ? null : quotaValue} />
            <div><p className="organization-panel-description">可用余额</p><p className="organization-big">{team.unlimited ? "不限" : (quotaValue - used).toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 })}</p><p className="organization-panel-description">已用 {formatCreditsDisplay(used)}{team.unlimited ? "" : ` / 额度 ${formatCreditsDisplay(quotaValue)}`}</p><div className="form-row organization-credit-actions"><button type="button" className="btn btn-sm" onClick={() => setEditingQuota((open) => !open)} aria-expanded={editingQuota}>调整额度</button>{canManagePlatform ? <button type="button" className="btn btn-ghost btn-sm" onClick={() => setAddingCredit((open) => !open)} aria-expanded={addingCredit}>添加余额</button> : null}</div></div>
          </div>
          {editingQuota ? <>
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
          </div>
          <p className="readout" style={{ margin: 0, marginTop: 10 }}>可用余额 {team.unlimited ? "不限" : `${formatCreditsDisplay(quotaValue - used)} credit`} · 按已结算用量扣减。</p>
          </> : null}
          {addingCredit && canManagePlatform ? (
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
        </div>
      </section>

      <section className="pane organization-team-spend">
        <div className="pane-h"><div><h2>近 30 天消耗</h2><p className="organization-panel-description">credit · 按上海时间分桶</p></div></div>
        <div className="pane-b">{spendError ? <Notice tone="stop">{spendError}</Notice> : spend === null ? <div className="organization-chart-empty">趋势加载中…</div> : <CreditBarChart rows={spend} />}</div>
      </section>

      <section className="pane organization-team-composition">
        <div className="pane-h"><div><h2>模型消费构成</h2><p className="organization-panel-description">团队累计已结算消费</p></div></div>
        <div className="pane-b"><CreditComposition rows={aggregates.map((row) => ({ id: row.model, label: row.modelDisplayName || row.model, value: row.creditsMicro / MICRO_CREDITS_PER_CREDIT }))} /></div>
      </section>

      <section className="pane organization-team-ranking">
        <div className="pane-h"><div><h2>成员用量排行</h2><p className="organization-panel-description">当前成员 · 近 30 天 credit 消耗前 7 名</p></div></div>
        <div className="pane-b">{memberRankingError ? <Notice tone="stop">{memberRankingError}</Notice> : memberRanking === null ? <div className="organization-chart-empty">成员用量加载中…</div> : <MemberCreditRanking rows={memberRanking} />}</div>
      </section>

      <section className="pane organization-team-timeline">
        <div className="pane-h"><h2>额度变更记录</h2></div>
        <div className="pane-b">{quotaAdjustments.length ? <div className="organization-quota-timeline">{quotaAdjustments.map((row) => { const delta = Number(row.newQuotaCredits) - Number(row.oldQuotaCredits); return <div key={row.id}><b>{row.oldUnlimited !== row.newUnlimited ? row.newUnlimited ? "取消额度上限" : "恢复额度上限" : row.action === "approve_quota_request" ? "额度申请已批准" : "调整团队额度"}</b> <span className="organization-timeline-delta">{delta > 0 ? "+" : ""}{formatCreditsDisplay(delta)}</span><small>{formatShanghaiTime(row.createdAt).slice(5, 16)} · {row.actorEmail || "系统"} · {formatCreditsDisplay(row.oldQuotaCredits)} → {formatCreditsDisplay(row.newQuotaCredits)}</small></div>; })}</div> : <p className="readout">暂无额度变更记录。</p>}</div>
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
            <Glyph icon={Users} size={14} strokeWidth={2} />
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
                  <td>{row.modelDisplayName || row.model}</td>
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
