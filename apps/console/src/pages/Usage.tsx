import {
  MICRO_CREDITS_PER_CREDIT,
  formatCreditsDisplay,
} from "@shucode-platform/contracts/credits";
import { useCallback, useEffect, useMemo, useState, type CSSProperties } from "react";
import { api } from "../api.js";
import { formatPct } from "../components/Gauge.js";
import { Select } from "../components/Select.js";
import { Chip, Notice } from "../components/StatusChip.js";

interface TeamOption {
  id: string;
  name: string;
  quotaCredits: string;
  usedCredits: string;
  unlimited: boolean;
}

interface UserOption {
  id: string;
  email: string;
}

interface AggregateRow {
  model: string;
  requests: number;
  inputTokens: number;
  outputTokens: number;
  creditsMicro: number;
}

interface UsageRecordRow {
  id: string;
  userId: string;
  model: string;
  status: string;
  inputTokens: number;
  outputTokens: number;
  creditsMicro: number;
  createdAt: string;
}

interface DayPoint {
  date: string;
  creditsMicro: number;
}

/** 与 /console/api/usage 响应对齐：records 为当前页，nextCursor 为下一页游标。 */
interface UsagePageData {
  aggregates: AggregateRow[];
  records: UsageRecordRow[];
  nextCursor: string | null;
}

const DAY = 86_400_000;

function localDate(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

function credits(micro: number): string {
  return formatCreditsDisplay(micro / MICRO_CREDITS_PER_CREDIT);
}

function statusChip(status: string): { tone?: "hot" | "stop" | "mute"; label: string } {
  if (status === "ok") return { label: "成功" };
  if (status === "aborted") return { tone: "hot", label: "中断" };
  return { tone: "stop", label: "错误" };
}

export function UsagePage() {
  const [teams, setTeams] = useState<TeamOption[]>([]);
  const [userEmails, setUserEmails] = useState<Map<string, string>>(new Map());
  const [teamId, setTeamId] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [aggregates, setAggregates] = useState<AggregateRow[]>([]);
  const [records, setRecords] = useState<UsageRecordRow[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [moreLoading, setMoreLoading] = useState(false);
  const [days, setDays] = useState<DayPoint[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    void (async () => {
      try {
        const [teamData, userData] = await Promise.all([
          api<{ teams: TeamOption[] }>("/teams"),
          api<{ users: UserOption[] }>("/users"),
        ]);
        setTeams(teamData.teams);
        setUserEmails(new Map(userData.users.map((user) => [user.id, user.email])));
      } catch (loadError) {
        setError(loadError instanceof Error ? loadError.message : "加载失败");
      }
    })();
  }, []);

  // 近 14 天柱图为平台口径，与筛选无关，挂载时取一次。
  useEffect(() => {
    void (async () => {
      const today = new Date();
      const points = await Promise.all(
        Array.from({ length: 14 }, (_, i) => {
          const day = localDate(new Date(today.getTime() - (13 - i) * DAY));
          return api<{ aggregates: AggregateRow[] }>(`/usage?from=${day}T00:00:00&to=${day}T23:59:59`)
            .then((data) => ({
              date: day,
              creditsMicro: data.aggregates.reduce((sum, row) => sum + row.creditsMicro, 0),
            }))
            .catch(() => ({ date: day, creditsMicro: 0 }));
        }),
      );
      setDays(points);
    })();
  }, []);

  const fetchUsage = useCallback(
    (cursor: string | null) => {
      const params = new URLSearchParams();
      if (teamId) params.set("teamId", teamId);
      if (from) params.set("from", `${from}T00:00:00`);
      if (to) params.set("to", `${to}T23:59:59`);
      if (cursor) params.set("cursor", cursor);
      return api<UsagePageData>(`/usage${params.size > 0 ? `?${params.toString()}` : ""}`);
    },
    [teamId, from, to],
  );

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const data = await fetchUsage(null);
      setAggregates(data.aggregates);
      setRecords(data.records);
      setNextCursor(data.nextCursor);
      setError(null);
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载失败");
    } finally {
      setLoading(false);
    }
  }, [fetchUsage]);

  const loadMoreRecords = useCallback(async () => {
    if (!nextCursor || moreLoading) return;
    setMoreLoading(true);
    try {
      const data = await fetchUsage(nextCursor);
      setRecords((previous) => [...previous, ...data.records]);
      setNextCursor(data.nextCursor);
      setError(null);
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载失败");
    } finally {
      setMoreLoading(false);
    }
  }, [fetchUsage, nextCursor, moreLoading]);

  useEffect(() => {
    void load();
  }, [load]);

  const totalCredits = aggregates.reduce((sum, row) => sum + row.creditsMicro, 0);
  const totalRequests = aggregates.reduce((sum, row) => sum + row.requests, 0);

  // 近额提示：额度用量 ≥90% 的团队，取最高者。
  const nearQuotaTeam = useMemo(() => {
    let best: { team: TeamOption; ratio: number } | null = null;
    for (const team of teams) {
      if (team.unlimited) continue;
      const quota = Number.parseFloat(team.quotaCredits) || 0;
      const used = Number.parseFloat(team.usedCredits) || 0;
      const ratio = quota > 0 ? Math.min(used / quota, 1) : 0;
      if (ratio >= 0.9 && (!best || ratio > best.ratio)) best = { team, ratio };
    }
    return best;
  }, [teams]);

  const maxDay = days.reduce((max, day) => Math.max(max, day.creditsMicro), 0);
  const yStops = [1, 0.75, 0.5, 0.25, 0].map((k) =>
    (maxDay * k / MICRO_CREDITS_PER_CREDIT).toLocaleString("en-US", { maximumFractionDigits: 2 }),
  );

  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">计量 / 流水</p>
          <h1>用量</h1>
        </div>
        <p className="readout m0">
          共 {totalRequests.toLocaleString("en-US")} 次请求 · {credits(totalCredits)} CR
        </p>
      </header>

      {error ? (
        <Notice tone="stop" k="加载失败">
          {error}
        </Notice>
      ) : null}
      {nearQuotaTeam ? (
        <Notice k={nearQuotaTeam.team.name} action={{ href: "#/teams", label: "调整额度" }}>
          额度已用 {formatPct(nearQuotaTeam.ratio)}，接近上限。
        </Notice>
      ) : null}

      <section className="pane">
        <div className="pane-b">
          <div className="form-row">
            <Select
              ariaLabel="按团队筛选"
              style={{ width: 170 }}
              value={teamId}
              onChange={setTeamId}
              options={[
                { value: "", label: "全部团队" },
                ...teams.map((team) => ({ value: team.id, label: team.name })),
              ]}
            />
            <label className="form-row" style={{ gap: 6 }}>
              <span className="readout">从</span>
              <input className="input" type="date" value={from} onChange={(event) => setFrom(event.target.value)} />
            </label>
            <label className="form-row" style={{ gap: 6 }}>
              <span className="readout">到</span>
              <input className="input" type="date" value={to} onChange={(event) => setTo(event.target.value)} />
            </label>
            <button className="btn btn-ghost" type="button" disabled={loading} onClick={() => void load()}>
              刷新
            </button>
          </div>
        </div>
      </section>

      <section className="pane d1">
        <div className="pane-h">
          <h2>近 14 天费用</h2>
          <span className="readout">单位：CR</span>
        </div>
        <div className="pane-b">
          <div className="chart">
            <div className="chart-y" aria-hidden="true">
              {yStops.map((stop, index) => (
                <span key={index}>{stop}</span>
              ))}
            </div>
            <div className="chart-main">
              <div className="chart-plot">
                <div className="bars">
                  {days.map((day, index) => (
                    <div
                      key={day.date}
                      className={index === days.length - 1 ? "bar today" : "bar"}
                      style={{ "--h": `${maxDay > 0 ? (day.creditsMicro / maxDay) * 100 : 0}%` } as CSSProperties}
                    />
                  ))}
                </div>
              </div>
              <div className="chart-x" aria-hidden="true">
                {days.map((day) => (
                  <span key={day.date}>{day.date.slice(5)}</span>
                ))}
              </div>
            </div>
          </div>
        </div>
      </section>

      <section className="pane d1">
        <div className="pane-h">
          <h2>按模型汇总</h2>
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
                  费用（CR）
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
                    所选范围内暂无用量。
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
                    {aggregates.reduce((sum, row) => sum + row.inputTokens, 0).toLocaleString("en-US")}
                  </td>
                  <td className="num">
                    {aggregates.reduce((sum, row) => sum + row.outputTokens, 0).toLocaleString("en-US")}
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
          <span className="readout">每页 100 条</span>
        </div>
        <div className="pane-b flush table-scroll vscroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">时间</th>
                <th scope="col">账号</th>
                <th scope="col">模型</th>
                <th scope="col">状态</th>
                <th scope="col" className="num">
                  Token（入 / 出）
                </th>
                <th scope="col" className="num">
                  费用
                </th>
              </tr>
            </thead>
            <tbody>
              {records.map((record) => {
                const status = statusChip(record.status);
                return (
                  <tr key={record.id}>
                    <td className="num">{record.createdAt.replace("T", " ").slice(0, 19)}</td>
                    <td>{userEmails.get(record.userId) ?? record.userId.slice(0, 8)}</td>
                    <td>{record.model}</td>
                    <td>
                      <Chip tone={status.tone}>{status.label}</Chip>
                    </td>
                    <td className="num">
                      {record.inputTokens.toLocaleString("en-US")} / {record.outputTokens.toLocaleString("en-US")}
                    </td>
                    <td className="num">{credits(record.creditsMicro)}</td>
                  </tr>
                );
              })}
              {records.length === 0 ? (
                <tr>
                  <td colSpan={6} className="readout">
                    暂无请求记录。
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
        {nextCursor ? (
          <div className="pane-b">
            <button
              className="btn btn-ghost"
              type="button"
              disabled={moreLoading}
              onClick={() => void loadMoreRecords()}
            >
              {moreLoading ? "加载中…" : "加载更多"}
            </button>
          </div>
        ) : null}
      </section>
    </div>
  );
}
