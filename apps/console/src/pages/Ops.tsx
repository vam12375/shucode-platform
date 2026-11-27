import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Activity, ArrowDownRight, ArrowUpRight, Minus, RefreshCw, TriangleAlert } from "lucide-react";
import { api } from "../api.js";
import { MediaFailures } from "./MediaFailures.js";
import { type ChartDatum, type ChartSeries } from "../components/charts.js";
import { PolarBarChart, PolarBars, PolarFlowBars, PolarSparkline, PolarStatusHexes, PolarTrendChart, type PolarPlotSeries } from "../components/PolarPlots.js";
import { confirmDialog, toastErr, toastOk } from "../components/Feedback.js";
import { formatPct } from "../components/Gauge.js";
import { Glyph, PageTitle } from "../components/Icon.js";
import { Select } from "../components/Select.js";
import { Chip, Notice } from "../components/StatusChip.js";
import { formatShanghaiTime } from "../time.js";
import { fillOpsSeries } from "../ops-series.js";
import "./polar-metrics.css";
import "./polar-ops-layout.css";

interface ProviderStat {
  providerId: string;
  providerName: string;
  requests: number;
  errors: number;
  errorRate: number;
  p95LatencyMs: number | null;
}

interface OpsTeam {
  id: string;
  name: string;
  concurrencyLimit: number;
  activeConcurrency?: number | null;
}

interface OpsProvider {
  id: string;
  name: string;
  enabled: boolean;
  hasKey: boolean;
  disabledMode: "manual" | "auto" | null;
}

interface OpsCostTotal {
  currency: string;
  requests: number;
  costMicro: number;
  chargedMicro: number;
  estimatedUsageRequests: number;
}

interface OpsCostSummary {
  totals: OpsCostTotal[];
  estimatedRequests: number;
  missingCostRequests: number;
  estimatedUsageRequests: number;
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
  costSummary: OpsCostSummary | null;
}

interface OpsSeriesRow {
  bucketStart: string;
  requests: number;
  errors: number;
  creditsMicro: number;
  p50LatencyMs?: number | null;
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
  backgroundResponseId?: string;
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
  { value: "open_reservations", label: "未结束请求身份数" },
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

function formatMoney(micro: number, currency: string): string {
  const value = micro / 1_000_000;
  try {
    return new Intl.NumberFormat("en-US", {
      style: "currency",
      currency: currency || "CNY",
      currencyDisplay: "narrowSymbol",
      maximumFractionDigits: 4,
    }).format(value);
  } catch {
    return `${value.toFixed(4)} ${currency || "CNY"}`;
  }
}

function formatLatency(value: number | null): string {
  return value === null ? "—" : `${Math.round(value)} ms`;
}

export function OpsPage() {
  const [hours, setHours] = useState("6");
  const [overview, setOverview] = useState<OpsOverview | null>(null);
  const [series, setSeries] = useState<OpsSeriesRow[]>([]);
  const [teams, setTeams] = useState<OpsTeam[]>([]);
  const [providers, setProviders] = useState<OpsProvider[]>([]);
  const [providerObservations, setProviderObservations] = useState<Record<string, (number | null)[]>>({});
  const [rules, setRules] = useState<AlertRule[]>([]);
  const [alerts, setAlerts] = useState<AlertEvent[]>([]);
  const [reconciliation, setReconciliation] = useState<Reconciliation | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(false);
  const requestVersion = useRef(0);
  const [ruleForm, setRuleForm] = useState({
    name: "",
    metric: "error_rate",
    threshold: "0.1",
    windowMinutes: "15",
  });

  const load = useCallback(async () => {
    const version = ++requestVersion.current;
    const observedAt = Date.now();
    setLoading(true);
    try {
      const [overviewData, seriesData, ruleData, alertData, reconciliationData, teamData, providerData] = await Promise.all([
        api<OpsOverview>(`/ops/overview?hours=${hours}`),
        api<{ series: OpsSeriesRow[] }>(`/ops/series?hours=${hours}&bucketMinutes=5`),
        api<{ rules: AlertRule[] }>("/ops/alert-rules"),
        api<{ alerts: AlertEvent[] }>("/ops/alerts?status=open&limit=50"),
        api<Reconciliation>("/ops/reconciliation?limit=50"),
        api<{ teams: OpsTeam[] }>("/teams"),
        api<{ providers: OpsProvider[] }>("/providers"),
      ]);
      // 窗口切换后旧请求仍可能返回；只允许当前窗口的最后一次请求更新读数。
      if (version !== requestVersion.current) return;
      setOverview(overviewData);
      // SQL 只返回有请求的桶：补齐真实空桶保持 5 分钟间距，延迟仍为未知值。
      setSeries(fillOpsSeries(seriesData.series, Number(hours), observedAt));
      setTeams(teamData.teams);
      setProviders(providerData.providers);
      setProviderObservations((previous) => Object.fromEntries(overviewData.providerStats.map((stat) => [stat.providerId, [...(previous[stat.providerId] ?? []), stat.p95LatencyMs === null ? null : stat.p95LatencyMs / 1000].slice(-120)])));
      setRules(ruleData.rules);
      setAlerts(alertData.alerts);
      setReconciliation(reconciliationData);
      setError(null);
    } catch (loadError) {
      if (version === requestVersion.current) setError(loadError instanceof Error ? loadError.message : "加载失败");
    } finally {
      if (version === requestVersion.current) setLoading(false);
    }
  }, [hours]);

  useEffect(() => {
    setOverview(null);
    setSeries([]);
    setTeams([]);
    setProviders([]);
    setProviderObservations({});
    void load();
    const timer = window.setInterval(() => { if (!document.hidden) void load(); }, 30_000);
    return () => { requestVersion.current += 1; window.clearInterval(timer); };
  }, [load]);

  const successRate =
    overview && overview.requests > 0 ? overview.successRequests / overview.requests : null;
  const primaryCostTotal =
    overview?.costSummary?.totals.find((total) => total.currency === "CNY") ??
    overview?.costSummary?.totals[0] ??
    null;

  const trafficData: ChartDatum[] = useMemo(
    () =>
      series.map((row) => ({
        label: Number(hours) > 24 ? `${row.bucketStart.slice(5, 10)} ${row.bucketStart.slice(11, 16)}` : row.bucketStart.slice(11, 16),
        requests: row.requests,
        errors: row.errors,
        p95: row.p95LatencyMs === null ? null : row.p95LatencyMs / 1000,
        p50: row.p50LatencyMs === null || row.p50LatencyMs === undefined ? null : row.p50LatencyMs / 1000,
      })),
    [series, hours],
  );
  const trafficSeries: ChartSeries[] = useMemo(
    () => [
      { key: "requests", label: "请求数", color: "var(--accent)", kind: "area", axisFormat: (v) => Math.round(v).toLocaleString("en-US") },
    ],
    [],
  );
  const errorSeries: PolarPlotSeries[] = [{ key: "errors", label: "错误数", color: "var(--muted)", colorAt: (_index, value) => value > 8 ? "var(--crit)" : "var(--muted)", kind: "bar", format: (v) => Math.round(v).toLocaleString("en-US"), axisFormat: (v) => Math.round(v).toLocaleString("en-US") }];
  const latencySeries: ChartSeries[] = [{ key: "p95", label: "P95", color: "var(--accent)", kind: "line", format: (v) => `${v.toFixed(2)}s`, axisFormat: (v) => `${Math.round(v)}s` }, { key: "p50", label: "P50", color: "#3D93D6", kind: "line", format: (v) => `${v.toFixed(2)}s`, axisFormat: (v) => `${Math.round(v)}s` }];

  const creditsData: ChartDatum[] = useMemo(
    () => series.map((row) => ({ label: row.bucketStart.slice(11, 16), credits: row.creditsMicro / 1_000_000 })),
    [series],
  );
  const creditsSeries: ChartSeries[] = useMemo(
    () => [{
      key: "credits", label: "积分 (credit)", color: "var(--accent)", kind: "bar",
      format: (v) => v.toFixed(4), axisFormat: (v) => v.toFixed(2),
    }],
    [],
  );
  const latest = series.at(-1);
  const previousHour = series.at(-13);
  const change = (value: number | null | undefined, previous: number | null | undefined, reverse = false) => {
    if (value == null || previous == null || previous === 0) return <span className="polar-ops-delta muted">较 1 小时前 —</span>;
    const ratio = (value - previous) / previous;
    const positive = ratio > 0;
    return <span className={`polar-ops-delta ${ratio === 0 ? "muted" : positive !== reverse ? "good" : "bad"}`}>{<Glyph icon={positive ? ArrowUpRight : ratio < 0 ? ArrowDownRight : Minus} size={12} />} {Math.abs(ratio * 100).toFixed(1)}%</span>;
  };
  const latencyRule = rules.find((rule) => rule.enabled && rule.metric === "p95_latency_ms" && Number(rule.threshold) > 0);
  const latencyReference = latencyRule ? { value: Number(latencyRule.threshold) / 1000, label: `${latencyRule.name} ${(Number(latencyRule.threshold) / 1000).toFixed(1)}s` } : undefined;
  const maximumLatency = Math.max(0, ...trafficData.map((row) => typeof row.p95 === "number" ? row.p95 : 0), latencyReference?.value ?? 0);
  const eventBand = useMemo<[number, number] | undefined>(() => {
    const relevant = alerts.find((alert) => ["p95_latency_ms", "error_rate", "provider_failures"].includes(alert.metric));
    if (!relevant || !series.length) return undefined;
    const start = formatShanghaiTime(relevant.createdAt).slice(0, 19);
    const index = series.findIndex((row) => row.bucketStart >= start);
    return index < 0 ? undefined : [index, series.length - 1];
  }, [alerts, series]);
  // 待核对请求已经结束上游调用，不能把请求身份数量当成当前并发；未采集的读数保持未知。
  const flowRows = useMemo(() => teams.slice(0, 6).map((team) => ({ key: team.id, label: team.name, value: team.activeConcurrency ?? null, limit: team.concurrencyLimit })), [teams]);

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
    <div className="polar-metrics polar-ops">
      <header className="page-head">
        <div>
          <p className="eyebrow">Operations · {HOUR_OPTIONS.find((option) => option.value === hours)?.label}</p>
          <PageTitle icon={Activity}>运维大盘</PageTitle>
          <p className="page-sub">5 分钟分桶 · 每 30 秒刷新 · 运行健康与结算读数</p>
        </div>
        <div className="form-row" style={{ gap: 8 }}>
          <Select
            ariaLabel="统计窗口"
            style={{ width: 130 }}
            value={hours}
            onChange={setHours}
            options={HOUR_OPTIONS}
          />
          <button className="btn btn-ghost" type="button" disabled={loading} onClick={() => void load()}>
            <Glyph icon={RefreshCw} size={14} strokeWidth={2} />
            {loading ? "更新中…" : "刷新"}
          </button>
        </div>
      </header>

      {error ? (
        <Notice tone="stop" k="加载失败">
          {error}
        </Notice>
      ) : null}

      <section className="polar-ops-live-grid" aria-label="实时运维指标">
        <div className="ops-live-metric"><p className="stat-label">请求 / 5 分钟</p><p className="stat-value">{latest?.requests.toLocaleString("en-US") ?? "—"}</p><div className="polar-ops-kpi-row">{change(latest?.requests, previousHour?.requests)}<PolarSparkline values={series.map((row) => row.requests)} width={120} area /></div></div>
        <div className="ops-live-metric"><p className="stat-label">错误数 / 5 分钟</p><p className="stat-value">{latest?.errors.toLocaleString("en-US") ?? "—"}</p><div className="polar-ops-kpi-row">{change(latest?.errors, previousHour?.errors, true)}<PolarSparkline values={series.map((row) => row.errors)} width={120} /></div></div>
        <div className="ops-live-metric"><p className="stat-label">P95 延迟</p><p className="stat-value">{latest?.p95LatencyMs == null ? "—" : (latest.p95LatencyMs / 1000).toFixed(2)}<small>s</small></p><div className="polar-ops-kpi-row">{change(latest?.p95LatencyMs, previousHour?.p95LatencyMs, true)}<PolarSparkline values={series.map((row) => row.p95LatencyMs === null ? null : row.p95LatencyMs / 1000)} width={120} /></div></div>
        <div className="ops-live-metric"><p className="stat-label">窗口积分</p><p className="stat-value">{overview ? formatCredits(overview.creditsMicro) : "—"}<small>credit</small></p><div className="polar-ops-kpi-row"><span className="polar-ops-delta muted">实际结算</span><PolarSparkline values={series.map((row) => row.creditsMicro / 1_000_000)} width={120} /></div></div>
      </section>

      <details className="polar-ops-window-summary"><summary>窗口汇总 <span>{overview ? `${overview.requests.toLocaleString("en-US")} 次请求 · ${successRate === null ? "—" : formatPct(successRate)} 成功 · ${overview.activeDevices} 台活跃设备` : "正在加载…"}</span></summary>
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
          <p className="stat-value">{overview ? formatCredits(overview.creditsMicro) : "—"}</p>
          <p className="stat-sub">credit · 网关计量</p>
        </div>
        <div
          className="ops-metric"
          title={
            overview?.costSummary?.totals
              .map((total) => formatMoney(total.costMicro, total.currency))
              .join(" · ") || undefined
          }
        >
          <p className="stat-label">窗口成本</p>
          <p className="stat-value">
            {primaryCostTotal
              ? formatMoney(primaryCostTotal.costMicro, primaryCostTotal.currency)
              : "—"}
          </p>
          <p className="stat-sub">
            估算 · 实扣 {formatCreditLabel(overview?.creditsMicro ?? 0)}
            {overview?.costSummary
              ? ` · ${overview.costSummary.missingCostRequests} 条无成本快照`
              : ""}
          </p>
        </div>
        <div className="ops-metric">
          <p className="stat-label">活跃设备</p>
          <p className="stat-value">{overview?.activeDevices.toLocaleString("en-US") ?? "—"}</p>
          <p className="stat-sub">窗口内有请求的设备</p>
        </div>
        <div className="ops-metric">
          <p className="stat-label">未结束请求身份</p>
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
      </details>

      <div className="polar-ops-diagnostics"><MediaFailures key={hours} hours={hours} /></div>

      <div className="polar-ops-charts">
        <section className="polar-panel polar-span-8">
          <div className="polar-panel-head"><div><h2>流量与错误</h2><p>上：请求数 · 下：错误数 · 同一时间轴、独立刻度</p></div></div>
          <PolarTrendChart data={trafficData} series={trafficSeries} height={190} band={eventBand} emptyText={loading ? "正在加载流量…" : "窗口内暂无请求。"} />
          <div className="polar-ops-error-plot"><PolarBarChart data={trafficData} series={errorSeries} height={110} ticks={2} ratio={.7} minGap={64} emptyText={loading ? "正在加载错误读数…" : "窗口内没有错误请求。"} /></div>
        </section>
        <section className="polar-panel polar-span-4">
          <div className="polar-panel-head"><div><h2>网关流控</h2><p>各团队当前并发 / 配置上限</p></div></div>
          {flowRows.length ? <PolarFlowBars rows={flowRows} /> : <div className="polar-chart-empty">{loading ? "正在加载团队流控…" : "暂无团队配置。"}</div>}
          <p className="polar-ops-flow-note">刻度线为 75% 预警位；未采集的当前并发以 — 表示。</p>
        </section>
        <section className="polar-panel polar-span-8">
          <div className="polar-panel-head"><div><h2>延迟 P50 / P95</h2><p>单位秒 · 虚线为告警阈值 · 阴影为事件窗口</p></div></div>
          <PolarTrendChart data={trafficData} series={latencySeries} height={230} area={false} yMax={maximumLatency <= 12 ? 12 : undefined} reference={latencyReference} band={eventBand} emptyText={loading ? "正在加载延迟…" : "窗口内暂无带延迟的记录。"} />
        </section>
        <section className="polar-panel polar-span-4">
          <div className="polar-panel-head"><div><h2>上游流量份额</h2><p>所选窗口内的请求数</p></div></div>
          <PolarBars rows={[...(overview?.providerStats ?? [])].sort((a, b) => b.requests - a.requests).slice(0, 8).map((provider) => ({ key: provider.providerId, label: provider.providerName, value: provider.requests, color: provider.errorRate >= .1 ? "var(--crit)" : provider.errorRate >= .03 ? "var(--warn)" : "var(--accent)", detail: `错误率 ${formatPct(provider.errorRate)}` }))} format={(value) => Math.round(value).toLocaleString("en-US")} unit="请求" emptyText={loading ? "正在加载上游流量…" : "窗口内暂无上游请求。"} />
        </section>

      </div>

      <details className="polar-ops-extra-credits"><summary>积分实扣趋势</summary><div className="polar-panel">
          <PolarBarChart data={creditsData} series={creditsSeries} height={200} emptyText={loading ? "正在加载消费趋势…" : "窗口内暂无积分数据。"} />
      </div></details>

      <section className="pane d1 polar-ops-providers">
        <div className="pane-h">
          <h2>供应商健康</h2>
          <span className="readout">窗口内请求 / 错误 · 走势为本页观测的 P95</span>
        </div>
        <div className="pane-b flush table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">供应商</th>
                <th scope="col">状态</th>
                <th scope="col" className="num">非错误占比</th>
                <th scope="col" className="num">请求数</th>
                <th scope="col" className="num">错误数</th>
                <th scope="col" className="num">错误率</th>
                <th scope="col" className="num">P95</th>
                <th scope="col">P95 观测</th>
                <th scope="col">上游凭据</th>
              </tr>
            </thead>
            <tbody>
              {(overview?.providerStats ?? []).map((stat) => (
                <tr key={stat.providerId || "env-default"}>
                  <td>{stat.providerName}</td>
                  <td><Chip tone={stat.errorRate >= 0.1 ? "stop" : stat.errorRate >= 0.03 ? "hot" : "ok"}>{stat.errorRate >= 0.1 ? "错误率偏高" : stat.errorRate >= 0.03 ? "需要关注" : "健康"}</Chip></td>
                  <td className="num">{stat.requests > 0 ? formatPct(1 - stat.errorRate) : "—"}</td>
                  <td className="num">{stat.requests.toLocaleString("en-US")}</td>
                  <td className="num">{stat.errors.toLocaleString("en-US")}</td>
                  <td className="num">
                    <Chip tone={stat.errorRate >= 0.1 ? "hot" : undefined}>{formatPct(stat.errorRate)}</Chip>
                  </td>
                  <td className="num">{formatLatency(stat.p95LatencyMs)}</td>
                  <td><span className="polar-ops-provider-spark"><PolarSparkline values={providerObservations[stat.providerId] ?? []} width={130} height={26} color={stat.errorRate >= .1 ? "var(--crit)" : stat.errorRate >= .03 ? "var(--warn)" : "var(--accent)"} /></span></td>
                  <td>{(() => {
                    const provider = providers.find((item) => item.id === stat.providerId);
                    if (!provider) return <span className="readout">—</span>;
                    const tone = !provider.hasKey ? "idle" : provider.disabledMode === "auto" ? "crit" : !provider.enabled ? "warn" : "ok";
                    const detail = !provider.hasKey ? "未配置凭据" : provider.disabledMode === "auto" ? "自动停用" : !provider.enabled ? "手动停用" : "已配置并启用";
                    return <PolarStatusHexes items={[{ key: provider.id, label: provider.name, tone, detail }]} />;
                  })()}</td>
                </tr>
              ))}
              {(overview?.providerStats.length ?? 0) === 0 ? (
                <tr>
                  <td colSpan={9} className="readout">{loading ? "正在加载供应商读数…" : "窗口内暂无供应商数据。"}</td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </section>

      <section className="pane d1 polar-ops-alerts">
        <div className="pane-h">
          <h2>告警</h2>
          <span className="readout">{alerts.length} 条未处理</span>
        </div>
        <div className="pane-b">
          <div className="polar-ops-alert-cards">{alerts.length ? alerts.map((alert) => <div className="polar-ops-alert-card" key={alert.id}>
            <Glyph icon={TriangleAlert} size={16} /><div><b>{alert.ruleName || alert.metric}</b><p>{alert.message}</p></div>
            <div><time>{formatShanghaiTime(alert.createdAt).slice(11, 16)}</time><button className="btn btn-ghost btn-sm" type="button" disabled={busy} onClick={() => void resolveAlert(alert)}>标记处理</button></div>
          </div>) : <p className="readout m0">当前没有未处理告警。</p>}</div>
          <details className="polar-ops-rule-settings"><summary>告警规则 · {rules.length} 条</summary>
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

          </details>
        </div>
      </section>

      <section className="pane d2 polar-ops-settlements">
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
                <th scope="col">上游任务 ID</th>
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
                  <td><code>{row.backgroundResponseId || "—"}</code></td>
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
                  <td colSpan={10} className="readout">没有待重试或转人工的结算补偿。</td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </section>

      <section className="pane d2">
        <div className="pane-h">
          <h2>请求身份对账</h2>
          <span className="readout">
            未结束 {reconciliation?.openCount ?? 0} · 已过期 {reconciliation?.staleCount ?? 0}
          </span>
        </div>
        <div className="pane-b">
          <p className="readout">请求身份用于恢复与幂等结算，不占余额。历史预留列仅兼容旧记录，新请求金额恒为 0。</p>
        </div>
        <div className="pane-b flush table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">创建时间</th>
                <th scope="col">过期时间</th>
                <th scope="col">request ID</th>
                <th scope="col">团队</th>
                <th scope="col" className="num">历史预留（credit）</th>
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
                  <td colSpan={5} className="readout">没有未结束的请求身份。</td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}
