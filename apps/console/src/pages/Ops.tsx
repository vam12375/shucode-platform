import { useCallback, useEffect, useMemo, useState } from "react";
import { api } from "../api.js";
import {
  ChartFrame,
  ComposedTrendChart,
  type ChartDatum,
  type ChartSeries,
} from "../components/charts.js";
import { confirmDialog, toastErr, toastOk } from "../components/Feedback.js";
import { formatPct } from "../components/Gauge.js";
import { Select } from "../components/Select.js";
import { Chip, Notice } from "../components/StatusChip.js";
import { formatShanghaiTime } from "../time.js";

interface ProviderStat {
  providerId: string;
  providerName: string;
  requests: number;
  errors: number;
  errorRate: number;
  p95LatencyMs: number | null;
}

interface OpsOverview {
  requests: number;
  successRequests: number;
  errorRequests: number;
  abortedRequests: number;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  creditsMicro: number;
  p50LatencyMs: number | null;
  p95LatencyMs: number | null;
  p99LatencyMs: number | null;
  activeDevices: number;
  openReservations: number;
  pendingSettlements: number;
  auditOutboxPending: number;
  providerStats: ProviderStat[];
}

interface OpsSeriesRow {
  bucketStart: string;
  requests: number;
  errors: number;
  creditsMicro: number;
  p95LatencyMs: number | null;
}

interface AlertRule {
  id: string;
  name: string;
  metric: string;
  threshold: string;
  windowMinutes: number;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
}

interface AlertEvent {
  id: string;
  ruleId: string;
  ruleName: string;
  metric: string;
  value: string;
  threshold: string;
  message: string;
  status: string;
  createdAt: string;
  resolvedAt: string | null;
}

interface ReservationRow {
  id: string;
  teamId: string;
  requestId: string;
  reservedCreditsMicro: number;
  createdAt: string;
  expiresAt: string;
}

interface PendingSettlementRow {
  reservationId: string;
  requestId: string;
  teamId: string;
  creditsMicro: number;
  settlementStatus: "pending" | "failed";
  usageStatus: string;
  attempts: number;
  lastError: string;
  nextAttemptAt: string;
  createdAt: string;
}

interface Reconciliation {
  open: ReservationRow[];
  openCount: number;
  staleCount: number;
  pendingSettlements: PendingSettlementRow[];
  pendingSettlementCount: number;
  failedSettlementCount: number;
}

const HOUR_OPTIONS = [
  { value: "1", label: "近 1 小时" },
  { value: "6", label: "近 6 小时" },
  { value: "24", label: "近 24 小时" },
  { value: "72", label: "近 3 天" },
  { value: "168", label: "近 7 天" },
];

const METRIC_OPTIONS = [
  { value: "error_rate", label: "错误率" },
  { value: "p95_latency_ms", label: "P95 延迟 (ms)" },
  { value: "open_reservations", label: "未释放预留数" },
  { value: "pending_settlements", label: "待结算补偿数" },
  { value: "audit_outbox", label: "待入库审计数" },
  { value: "daily_credits", label: "窗口消费 (credit)" },
  { value: "provider_failures", label: "上游错误数" },
];

const METRIC_DEFAULT_THRESHOLDS: Record<string, string> = {
  error_rate: "0.1",
  p95_latency_ms: "5000",
  open_reservations: "10",
  pending_settlements: "1",
  audit_outbox: "1",
  daily_credits: "100",
  provider_failures: "10",
};

function formatCredits(micro: number): string {
  return (micro / 1_000_000).toLocaleString("en-US", {
    maximumFractionDigits: 4,
  });
}

function formatCreditLabel(micro: number): string {
  return `${formatCredits(micro)} credit`;
}

function formatLatency(value: number | null): string {
  return value === null ? "—" : `${Math.round(value)} ms`;
}

export function OpsPage() {
  const [hours, setHours] = useState("24");
  const [overview, setOverview] = useState<OpsOverview | null>(null);
  const [series, setSeries] = useState<OpsSeriesRow[]>([]);
  const [rules, setRules] = useState<AlertRule[]>([]);
  const [alerts, setAlerts] = useState<AlertEvent[]>([]);
  const [reconciliation, setReconciliation] = useState<Reconciliation | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [ruleForm, setRuleForm] = useState({
    name: "",
    metric: "error_rate",
    threshold: "0.1",
    windowMinutes: "15",
  });

  const load = useCallback(async () => {
    try {
      const [overviewData, seriesData, ruleData, alertData, reconciliationData] = await Promise.all([
        api<OpsOverview>(`/ops/overview?hours=${hours}`),
        api<{ series: OpsSeriesRow[] }>(`/ops/series?hours=${hours}&bucketMinutes=5`),
        api<{ rules: AlertRule[] }>("/ops/alert-rules"),
        api<{ alerts: AlertEvent[] }>("/ops/alerts?status=open&limit=50"),
        api<Reconciliation>("/ops/reconciliation?limit=50"),
      ]);
      setOverview(overviewData);
      setSeries(seriesData.series);
      setRules(ruleData.rules);
      setAlerts(alertData.alerts);
      setReconciliation(reconciliationData);
      setError(null);
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载失败");
    }
  }, [hours]);

  useEffect(() => {
    void load();
  }, [load]);

  const successRate =
    overview && overview.requests > 0 ? overview.successRequests / overview.requests : null;

  const trafficData: ChartDatum[] = useMemo(
    () =>
      series.map((row) => ({
        label: row.bucketStart.slice(11, 16),
        requests: row.requests,
        errors: row.errors,
        p95: row.p95LatencyMs ?? 0,
      })),
    [series],
  );
  const trafficSeries: ChartSeries[] = useMemo(
    () => [
      { key: "requests", label: "请求数", color: "var(--ink)", kind: "line", axisFormat: (v) => Math.round(v).toLocaleString("en-US") },
      {
        key: "errors", label: "错误数", color: "var(--signal, #bf3a1f)", kind: "bar",
        format: (v) => Math.round(v).toLocaleString("en-US"),
        axisFormat: (v) => Math.round(v).toLocaleString("en-US"),
      },
      {
        key: "p95",
        label: "P95 延迟 (ms)",
        color: "var(--info)",
        kind: "line",
        axis: "right",
        format: (v) => `${Math.round(v).toLocaleString("en-US")} ms`,
        axisFormat: (v) => `${Math.round(v / 1000)}s`,
      },
    ],
    [],
  );

  const creditsData: ChartDatum[] = useMemo(
    () => series.map((row) => ({ label: row.bucketStart.slice(11, 16), credits: row.creditsMicro / 1_000_000 })),
    [series],
  );
  const creditsSeries: ChartSeries[] = useMemo(
    () => [{
      key: "credits", label: "积分 (credit)", color: "var(--amber)", kind: "bar",
      format: (v) => v.toFixed(4), axisFormat: (v) => v.toFixed(2),
    }],
    [],
  );

  const createRule = async (): Promise<void> => {
    setBusy(true);
    try {
      await api("/ops/alert-rules", {
        method: "POST",
        body: JSON.stringify({
          name: ruleForm.name.trim(),
          metric: ruleForm.metric,
          threshold: Number.parseFloat(ruleForm.threshold),
          windowMinutes: Number.parseInt(ruleForm.windowMinutes, 10) || 15,
          enabled: true,
        }),
      });
      setRuleForm({ name: "", metric: "error_rate", threshold: "0.1", windowMinutes: "15" });
      await load();
      toastOk("告警规则已创建", "已创建");
    } catch (actionError) {
      toastErr(actionError instanceof Error ? actionError.message : "创建失败", "创建失败");
    } finally {
      setBusy(false);
    }
  };

  const toggleRule = async (rule: AlertRule): Promise<void> => {
    setBusy(true);
    try {
      await api(`/ops/alert-rules/${rule.id}`, {
        method: "PUT",
        body: JSON.stringify({
          name: rule.name,
          metric: rule.metric,
          threshold: Number.parseFloat(rule.threshold),
          windowMinutes: rule.windowMinutes,
          enabled: !rule.enabled,
        }),
      });
      await load();
    } catch (actionError) {
      toastErr(actionError instanceof Error ? actionError.message : "更新失败", "更新失败");
    } finally {
      setBusy(false);
    }
  };

  const removeRule = async (rule: AlertRule): Promise<void> => {
    const answer = await confirmDialog({
      k: "删除告警规则",
      title: `删除「${rule.name}」？`,
      message: "规则关联的历史事件会一并删除。",
      confirmText: "删除",
      danger: true,
    });
    if (!answer.confirmed) return;
    setBusy(true);
    try {
      await api(`/ops/alert-rules/${rule.id}`, { method: "DELETE" });
      await load();
      toastOk("告警规则已删除", "已删除");
    } catch (actionError) {
      toastErr(actionError instanceof Error ? actionError.message : "删除失败", "删除失败");
    } finally {
      setBusy(false);
    }
  };

  const resolveAlert = async (alert: AlertEvent): Promise<void> => {
    setBusy(true);
    try {
      await api(`/ops/alerts/${alert.id}/resolve`, { method: "POST" });
      await load();
      toastOk("告警已标记处理", "已处理");
    } catch (actionError) {
      toastErr(actionError instanceof Error ? actionError.message : "操作失败", "操作失败");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">运维 / 实时读数</p>
          <h1>运维大盘</h1>
        </div>
        <div className="form-row" style={{ gap: 8 }}>
          <Select
            ariaLabel="统计窗口"
            style={{ width: 130 }}
            value={hours}
            onChange={setHours}
            options={HOUR_OPTIONS}
          />
          <button className="btn btn-ghost" type="button" onClick={() => void load()}>
            刷新
          </button>
        </div>
      </header>

      {error ? (
        <Notice tone="stop" k="加载失败">
          {error}
        </Notice>
      ) : null}

      <section className="ops-metric-grid" aria-label="运维指标">
        <div className="ops-metric">
          <p className="stat-label">请求数</p>
          <p className="stat-value">{overview?.requests.toLocaleString("en-US") ?? "—"}</p>
          <p className="stat-sub">
            成功 {overview?.successRequests.toLocaleString("en-US") ?? "—"} · 错误{" "}
            {overview?.errorRequests.toLocaleString("en-US") ?? "—"} · 中断{" "}
            {overview?.abortedRequests.toLocaleString("en-US") ?? "—"}
          </p>
        </div>
        <div className="ops-metric">
          <p className="stat-label">成功率</p>
          <p className="stat-value">{successRate === null ? "—" : formatPct(successRate)}</p>
          <p className="stat-sub">按窗口内请求统计</p>
        </div>
        <div className="ops-metric">
          <p className="stat-label">P95 延迟</p>
          <p className="stat-value">{formatLatency(overview?.p95LatencyMs ?? null)}</p>
          <p className="stat-sub">
            P50 {formatLatency(overview?.p50LatencyMs ?? null)} · P99 {formatLatency(overview?.p99LatencyMs ?? null)}
          </p>
        </div>
        <div className="ops-metric">
          <p className="stat-label">窗口积分</p>
          <p className="stat-value">{formatCreditLabel(overview?.creditsMicro ?? 0)}</p>
          <p className="stat-sub">credit · 网关计量</p>
        </div>
        <div className="ops-metric">
          <p className="stat-label">活跃设备</p>
          <p className="stat-value">{overview?.activeDevices.toLocaleString("en-US") ?? "—"}</p>
          <p className="stat-sub">窗口内有请求的设备</p>
        </div>
        <div className="ops-metric">
          <p className="stat-label">未释放预留</p>
          <p className="stat-value">{overview?.openReservations.toLocaleString("en-US") ?? "—"}</p>
          <p className="stat-sub">
            过期 {reconciliation?.staleCount ?? 0} 条
          </p>
        </div>
        <div className="ops-metric">
          <p className="stat-label">待结算补偿</p>
          <p className="stat-value">
            {overview?.pendingSettlements.toLocaleString("en-US") ?? "—"}
          </p>
          <p className="stat-sub">
            待重试 {reconciliation?.pendingSettlementCount ?? 0} · 人工{" "}
            {reconciliation?.failedSettlementCount ?? 0}
          </p>
        </div>
        <div className="ops-metric">
          <p className="stat-label">审计 outbox</p>
          <p className="stat-value">
            {overview?.auditOutboxPending.toLocaleString("en-US") ?? "—"}
          </p>
          <p className="stat-sub">待入库事件 · 进程本地持久化</p>
        </div>
      </section>

      <div className="chart-grid">
        <ChartFrame title="流量与错误" subtitle="灰色为请求数，红色为错误数，蓝线为 P95 延迟">
          <ComposedTrendChart data={trafficData} series={trafficSeries} height={260} emptyText="窗口内暂无请求。" />
        </ChartFrame>
        <ChartFrame title="消费趋势" subtitle="按 5 分钟分桶的 credit 消费">
          <ComposedTrendChart data={creditsData} series={creditsSeries} height={260} emptyText="窗口内暂无积分数据。" />
        </ChartFrame>
      </div>

      <section className="pane d1">
        <div className="pane-h">
          <h2>供应商健康</h2>
          <span className="readout">窗口内请求 / 错误 / P95</span>
        </div>
        <div className="pane-b flush table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">供应商</th>
                <th scope="col" className="num">请求数</th>
                <th scope="col" className="num">错误数</th>
                <th scope="col" className="num">错误率</th>
                <th scope="col" className="num">P95</th>
              </tr>
            </thead>
            <tbody>
              {(overview?.providerStats ?? []).map((stat) => (
                <tr key={stat.providerId || "env-default"}>
                  <td>{stat.providerName}</td>
                  <td className="num">{stat.requests.toLocaleString("en-US")}</td>
                  <td className="num">{stat.errors.toLocaleString("en-US")}</td>
                  <td className="num">
                    <Chip tone={stat.errorRate >= 0.1 ? "hot" : undefined}>{formatPct(stat.errorRate)}</Chip>
                  </td>
                  <td className="num">{formatLatency(stat.p95LatencyMs)}</td>
                </tr>
              ))}
              {(overview?.providerStats.length ?? 0) === 0 ? (
                <tr>
                  <td colSpan={5} className="readout">窗口内暂无供应商数据。</td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </section>

      <section className="pane d1">
        <div className="pane-h">
          <h2>告警</h2>
          <span className="readout">{alerts.length} 条未处理</span>
        </div>
        <div className="pane-b">
          <div className="form-row" style={{ gap: 8, marginBottom: 12 }}>
            <input
              className="input"
              style={{ width: 180 }}
              value={ruleForm.name}
              onChange={(event) => setRuleForm((current) => ({ ...current, name: event.target.value }))}
              placeholder="规则名称，如「错误率过高」"
              aria-label="告警规则名称"
            />
            <Select
              ariaLabel="告警指标"
              style={{ width: 180 }}
              value={ruleForm.metric}
              onChange={(next) =>
                setRuleForm((current) => ({
                  ...current,
                  metric: next,
                  threshold: METRIC_DEFAULT_THRESHOLDS[next] ?? current.threshold,
                }))
              }
              options={METRIC_OPTIONS}
            />
            <input
              className="input num"
              style={{ width: 110 }}
              value={ruleForm.threshold}
              onChange={(event) => setRuleForm((current) => ({ ...current, threshold: event.target.value }))}
              placeholder="阈值"
              aria-label="告警阈值"
            />
            <input
              className="input num"
              style={{ width: 90 }}
              value={ruleForm.windowMinutes}
              onChange={(event) => setRuleForm((current) => ({ ...current, windowMinutes: event.target.value }))}
              placeholder="窗口分钟"
              aria-label="告警窗口分钟"
            />
            <button className="btn" type="button" disabled={busy || !ruleForm.name.trim()} onClick={() => void createRule()}>
              新增规则
            </button>
          </div>

          <div className="table-scroll">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">规则</th>
                  <th scope="col">指标</th>
                  <th scope="col" className="num">阈值</th>
                  <th scope="col" className="num">窗口</th>
                  <th scope="col">状态</th>
                  <th scope="col" aria-label="操作" />
                </tr>
              </thead>
              <tbody>
                {rules.map((rule) => (
                  <tr key={rule.id}>
                    <td>{rule.name}</td>
                    <td className="readout">{METRIC_OPTIONS.find((item) => item.value === rule.metric)?.label ?? rule.metric}</td>
                    <td className="num">{rule.threshold}</td>
                    <td className="num">{rule.windowMinutes} 分钟</td>
                    <td>
                      <Chip tone={rule.enabled ? "ok" : "mute"}>{rule.enabled ? "启用" : "停用"}</Chip>
                    </td>
                    <td>
                      <div className="form-row" style={{ gap: 6 }}>
                        <button className="btn btn-ghost btn-sm" type="button" disabled={busy} onClick={() => void toggleRule(rule)}>
                          {rule.enabled ? "停用" : "启用"}
                        </button>
                        <button className="btn btn-ghost btn-sm" type="button" disabled={busy} onClick={() => void removeRule(rule)}>
                          删除
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
                {rules.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="readout">还没有告警规则。</td>
                  </tr>
                ) : null}
              </tbody>
            </table>
          </div>

          <div className="table-scroll" style={{ marginTop: 14 }}>
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">时间</th>
                  <th scope="col">规则</th>
                  <th scope="col">说明</th>
                  <th scope="col">状态</th>
                  <th scope="col" aria-label="操作" />
                </tr>
              </thead>
              <tbody>
                {alerts.map((alert) => (
                  <tr key={alert.id}>
                    <td className="num">{formatShanghaiTime(alert.createdAt)}</td>
                    <td>{alert.ruleName || alert.metric}</td>
                    <td>{alert.message}</td>
                    <td>
                      <Chip tone="hot">{alert.status}</Chip>
                    </td>
                    <td>
                      <button className="btn btn-ghost btn-sm" type="button" disabled={busy} onClick={() => void resolveAlert(alert)}>
                        标记处理
                      </button>
                    </td>
                  </tr>
                ))}
                {alerts.length === 0 ? (
                  <tr>
                    <td colSpan={5} className="readout">当前没有未处理告警。</td>
                  </tr>
                ) : null}
              </tbody>
            </table>
          </div>
        </div>
      </section>

      <section className="pane d2">
        <div className="pane-h">
          <h2>待结算补偿</h2>
          <span className="readout">
            待重试 {reconciliation?.pendingSettlementCount ?? 0} · 转人工{" "}
            {reconciliation?.failedSettlementCount ?? 0}
          </span>
        </div>
        <div className="pane-b flush table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">创建时间</th>
                <th scope="col">下次重试</th>
                <th scope="col">request ID</th>
                <th scope="col">团队</th>
                <th scope="col">补偿状态</th>
                <th scope="col">用量状态</th>
                <th scope="col" className="num">积分（credit）</th>
                <th scope="col" className="num">次数</th>
                <th scope="col">最近错误</th>
              </tr>
            </thead>
            <tbody>
              {(reconciliation?.pendingSettlements ?? []).map((row) => (
                <tr key={row.reservationId}>
                  <td className="num">{formatShanghaiTime(row.createdAt)}</td>
                  <td className="num">{formatShanghaiTime(row.nextAttemptAt)}</td>
                  <td><code>{row.requestId}</code></td>
                  <td>{row.teamId.slice(0, 8)}</td>
                  <td>
                    <Chip tone={row.settlementStatus === "failed" ? "stop" : "hot"}>
                      {row.settlementStatus === "failed" ? "转人工" : "待重试"}
                    </Chip>
                  </td>
                  <td>{row.usageStatus}</td>
                  <td className="num">{formatCredits(row.creditsMicro)}</td>
                  <td className="num">{row.attempts}</td>
                  <td title={row.lastError}>{row.lastError || "—"}</td>
                </tr>
              ))}
              {(reconciliation?.pendingSettlements.length ?? 0) === 0 ? (
                <tr>
                  <td colSpan={9} className="readout">没有待重试或转人工的结算补偿。</td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </section>

      <section className="pane d2">
        <div className="pane-h">
          <h2>预留对账</h2>
          <span className="readout">
            未释放 {reconciliation?.openCount ?? 0} · 已过期 {reconciliation?.staleCount ?? 0}
          </span>
        </div>
        <div className="pane-b flush table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">创建时间</th>
                <th scope="col">过期时间</th>
                <th scope="col">request ID</th>
                <th scope="col">团队</th>
                <th scope="col" className="num">预留（credit）</th>
              </tr>
            </thead>
            <tbody>
              {(reconciliation?.open ?? []).map((row) => (
                <tr key={row.id}>
                  <td className="num">{formatShanghaiTime(row.createdAt)}</td>
                  <td className="num">{formatShanghaiTime(row.expiresAt)}</td>
                  <td><code>{row.requestId}</code></td>
                  <td>{row.teamId.slice(0, 8)}</td>
                  <td className="num">{formatCredits(row.reservedCreditsMicro)}</td>
                </tr>
              ))}
              {(reconciliation?.open.length ?? 0) === 0 ? (
                <tr>
                  <td colSpan={5} className="readout">没有未释放的预留。</td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}
