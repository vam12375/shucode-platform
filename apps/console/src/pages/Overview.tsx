import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Activity, ArrowDownRight, ArrowUpRight, Check, ChartColumn, RefreshCw, TriangleAlert, X } from "lucide-react";
import { api } from "../api.js";
import { useAuth } from "../auth.js";
import { confirmDialog, toastErr, toastOk } from "../components/Feedback.js";
import { Glyph } from "../components/Icon.js";
import { Chip, Notice } from "../components/StatusChip.js";
import { useCountUp } from "../components/Gauge.js";
import {
  entityColor,
  NightClock,
  PolarBars,
  PolarSegments,
  PolarSparkline,
  PolarStatusHexes,
  polarCompact,
  polarCredits,
  PolarTrendChart,
  RequestTopology,
  type TopologyModel,
  type TopologyProvider,
  type TopologyRoute,
} from "../components/PolarCharts.js";
import type { ChartDatum, ChartSeries } from "../components/charts.js";
import { formatShanghaiTime } from "../time.js";
import { resolveUsageChartRange } from "../usage-range.js";
import "./polar-metrics.css";
import "./polar-overview.css";

interface Provider extends TopologyProvider {
  hasKey: boolean;
  disabledMode: "manual" | "auto" | null;
}

interface Team {
  id: string;
  name: string;
  quotaCredits: string;
  usedCredits: string;
  unlimited: boolean;
}

interface Aggregate {
  model: string;
  modelDisplayName?: string;
  requests: number;
  creditsMicro: number;
  inputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
}

interface RecordRow {
  teamId: string;
  latencyMs: number | null;
}

interface SeriesRow {
  bucketStart: string;
  requests: number;
  creditsMicro: number;
  inputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
}

interface OpsSeriesRow { bucketStart: string; requests: number; errors: number; p95LatencyMs: number | null; creditsMicro: number }
interface DeviceRow { lastUsedAt: string | null; revokedAt: string | null }

interface OpsSummary {
  requests: number;
  errorRequests: number;
  creditsMicro: number;
  inputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  activeDevices: number;
  p95LatencyMs: number | null;
  pendingSettlements: number;
  providerStats: { providerId: string; providerName: string; requests: number; errors: number; errorRate: number; p95LatencyMs: number | null }[];
}

interface Alert {
  id: string;
  ruleName: string;
  message: string;
  metric: string;
  createdAt: string;
}

interface QuotaRequest {
  id: string;
  teamId: string;
  amountCredits: string;
  reason: string;
  status: string;
  createdAt: string;
}

interface Order {
  id: string;
  targetType: string;
  targetId: string;
  teamId?: string;
  amountYuan: string;
  creditAmount: string;
  status: string;
  createdAt: string;
}

interface OverviewData {
  ops: OpsSummary | null;
  topologyOps: OpsSummary | null;
  topologyUsage: Aggregate[] | null;
  today: Aggregate[] | null;
  yesterday: Aggregate[] | null;
  daily: SeriesRow[] | null;
  opsSeries: OpsSeriesRow[] | null;
  devices: DeviceRow[] | null;
  teams: Team[] | null;
  models: TopologyModel[] | null;
  providers: Provider[] | null;
  aggregates: Aggregate[] | null;
  records: RecordRow[] | null;
  hourly: SeriesRow[] | null;
  trend: ChartDatum[] | null;
  trendSeries: ChartSeries[];
  alerts: Alert[] | null;
  quota: QuotaRequest[] | null;
  orders: Order[] | null;
  updatedAt: Date;
  errors: string[];
}

const number = (value: number) => value.toLocaleString("en-US");
const credits = (value: number) => value.toLocaleString("en-US", { maximumFractionDigits: 4 });
const percent = (value: number) => `${(value * 100).toFixed(1)}%`;

function Panel({ title, subtitle, span, action, children, className = "" }: { title: string; subtitle?: string; span: number; action?: ReactNode; children: ReactNode; className?: string }) {
  return <section className={`polar-panel polar-span-${span} ${className}`}>
    <div className="polar-panel-head"><div><h2>{title}</h2>{subtitle ? <p>{subtitle}</p> : null}</div>{action}</div>
    <div className="polar-panel-body">{children}</div>
  </section>;
}

function Empty({ failed, children = "暂无数据。" }: { failed?: boolean; children?: ReactNode }) {
  return <div className="polar-chart-empty">{failed ? "数据加载失败，请刷新重试。" : children}</div>;
}

interface Reading {
  label: string;
  value: number | null;
  decimals: number;
  unit: string;
  note: string;
  spark?: (number | null)[];
  sparkLabel?: string;
  area?: boolean;
  delta?: number | null;
  deltaLabel?: string;
  invert?: boolean;
}

function OverviewReading({ reading }: { reading: Reading }) {
  const text = useCountUp(reading.value ?? 0, reading.decimals);
  const good = reading.delta !== null && reading.delta !== undefined && (reading.invert ? reading.delta < 0 : reading.delta >= 0);
  return <div className="polar-reading" title={reading.note}>
    <small>{reading.label}</small><p>{reading.value === null ? "—" : text}<span>{reading.unit}</span></p>
    <div className="overview-reading-row">
      {reading.delta !== null && reading.delta !== undefined ? <span className={`overview-delta ${good ? "good" : "bad"}`} title={reading.deltaLabel}><Glyph icon={reading.delta >= 0 ? ArrowUpRight : ArrowDownRight} size={12} />{Math.abs(reading.delta * 100).toFixed(1)}%</span> : <em>{reading.sparkLabel || reading.note}</em>}
      {reading.spark?.length ? <span title={reading.sparkLabel ?? reading.note}><PolarSparkline values={reading.spark} width={96} height={28} area={reading.area} /></span> : null}
    </div>
  </div>;
}

export function OverviewPage() {
  const { canManagePlatform, refreshSession } = useAuth();
  const [period, setPeriod] = useState<"7d" | "30d">("30d");
  const [data, setData] = useState<OverviewData | null>(null);
  const [loading, setLoading] = useState(false);
  const [busyId, setBusyId] = useState<string | null>(null);
  const request = useRef<AbortController | null>(null);
  const reviewBusy = useRef(false);

  const load = useCallback(async () => {
    if (!canManagePlatform) return;
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setLoading(true);
    const now = new Date();
    const range = resolveUsageChartRange(period, "", "", now);
    const hourlyRange = resolveUsageChartRange("last24h", "", "", now);
    const todayRange = resolveUsageChartRange("today", "", "", now);
    const yesterdayRange = resolveUsageChartRange("yesterday", "", "", now);
    const dailyRange = resolveUsageChartRange("14d", "", "", now);
    const errors: string[] = [];
    const get = async <T,>(label: string, path: string): Promise<T | null> => {
      try { return await api<T>(path, { signal: controller.signal }); }
      catch (error) {
        if (!controller.signal.aborted) errors.push(`${label}：${error instanceof Error ? error.message : "加载失败"}`);
        return null;
      }
    };
    // 平台总览不能继承团队切换器的隐式范围，否则全局运维读数会混入单团队消费。
    const interval = new URLSearchParams({ teamId: "", from: range.queryFrom, to: range.queryTo });
    const hourlyQuery = new URLSearchParams({ teamId: "", from: hourlyRange.queryFrom, to: hourlyRange.queryTo, granularity: "hour" });
    const rangeQuery = (value: { queryFrom: string; queryTo: string }) => new URLSearchParams({ teamId: "", from: value.queryFrom, to: value.queryTo }).toString();
    const hourQuery = new URLSearchParams({ teamId: "", from: new Date(now.getTime() - 3_600_000).toISOString().slice(0, -1), to: now.toISOString().slice(0, -1) });
    const [ops, teamResult, modelResult, providerResult, usage, trendResult, hourlyResult, alertResult, quotaResult, orderResult, topologyOps, flowUsage, todayUsage, yesterdayUsage, dailyUsage, opsTimeseries, deviceResult] = await Promise.all([
      get<OpsSummary>("运行读数", "/ops/overview?hours=24"),
      get<{ teams: Team[] }>("团队", "/teams"),
      get<{ models: TopologyModel[] }>("模型路由", "/models?includeDisabled=1&teamId="),
      get<{ providers: Provider[] }>("上游", "/providers"),
      get<{ aggregates: Aggregate[]; records: RecordRow[] }>("消费构成", `/usage?${interval.toString()}&limit=200`),
      get<{ series: SeriesRow[] }>("消费趋势", `/usage/series?${interval.toString()}&granularity=day`),
      get<{ series: SeriesRow[] }>("夜钟", `/usage/series?${hourlyQuery.toString()}`),
      get<{ alerts: Alert[] }>("告警", "/ops/alerts?status=open&limit=5"),
      get<{ requests: QuotaRequest[] }>("额度申请", "/quota-requests?teamId="),
      get<{ orders: Order[] }>("最近充值", "/payments/orders"),
      get<OpsSummary>("拓扑小时流量", "/ops/overview?hours=1"),
      get<{ aggregates: Aggregate[] }>("拓扑模型流量", `/usage?${hourQuery.toString()}&limit=1`),
      get<{ aggregates: Aggregate[] }>("今日用量", `/usage?${rangeQuery(todayRange)}&limit=1`),
      get<{ aggregates: Aggregate[] }>("昨日用量", `/usage?${rangeQuery(yesterdayRange)}&limit=1`),
      get<{ series: SeriesRow[] }>("缓存日趋势", `/usage/series?${rangeQuery(dailyRange)}&granularity=day`),
      get<{ series: OpsSeriesRow[] }>("运行趋势", "/ops/series?hours=24&bucketMinutes=5"),
      get<{ devices: DeviceRow[] }>("设备最近活跃", "/devices?teamId="),
    ]);
    if (controller.signal.aborted) return;
    const routeModels = modelResult?.models.filter((model) => model.enabled).slice(0, 6) ?? [];
    const routeResults = await Promise.all(routeModels.map((model) => get<{ routes: TopologyRoute[] }>(`${model.displayName} 路由`, `/models/${encodeURIComponent(model.modelId)}/routes`)));
    if (controller.signal.aborted) return;
    const routesByModel = new Map(routeModels.map((model, index) => [model.modelId, routeResults[index]?.routes ?? null]));
    const models = modelResult?.models.map((model) => ({ ...model, ...(routesByModel.has(model.modelId) ? { routes: routesByModel.get(model.modelId) } : {}) })) ?? null;
    const zeroed = new Map(range.bucketKeys.map((key) => [key, { label: key.slice(5, 10), credits: 0 } as ChartDatum]));
    for (const row of trendResult?.series ?? []) {
      const bucket = zeroed.get(row.bucketStart);
      if (bucket) bucket.credits = row.creditsMicro / 1_000_000;
    }
    const topModels = [...(usage?.aggregates ?? [])].filter((row) => row.creditsMicro > 0).sort((a, b) => b.creditsMicro - a.creditsMicro).slice(0, 6);
    let trendSeries: ChartSeries[] = [{ key: "credits", label: "总消耗", color: "var(--accent)", kind: "area", format: polarCredits, axisFormat: polarCompact }];
    if (topModels.length) {
      const modelResults = await Promise.all(topModels.map((model) => get<{ series: SeriesRow[] }>(`${model.model} 趋势`, `/usage/series?${interval.toString()}&granularity=day&model=${encodeURIComponent(model.model)}`)));
      if (controller.signal.aborted) return;
      if (modelResults.every((result) => result !== null)) {
        trendSeries = topModels.map((model, index) => ({ key: `model${index}`, label: model.modelDisplayName || model.model, color: entityColor(model.model), kind: "area", format: polarCredits, axisFormat: polarCompact }));
        for (const bucket of zeroed.values()) for (let index = 0; index < topModels.length; index++) bucket[`model${index}`] = 0;
        modelResults.forEach((result, index) => {
          for (const row of result?.series ?? []) {
            const bucket = zeroed.get(row.bucketStart);
            if (bucket) bucket[`model${index}`] = row.creditsMicro / 1_000_000;
          }
        });
        if ((usage?.aggregates.filter((row) => row.creditsMicro > 0).length ?? 0) > 6) {
          trendSeries.push({ key: "other", label: "其他模型", color: "#788BA2", kind: "area", format: polarCredits, axisFormat: polarCompact });
          for (const bucket of zeroed.values()) bucket.other = Math.max(0, Number(bucket.credits) - topModels.reduce((sum, _, index) => sum + Number(bucket[`model${index}`] || 0), 0));
        }
      }
    }
    setData({ ops, topologyOps, topologyUsage: flowUsage?.aggregates ?? null, today: todayUsage?.aggregates ?? null, yesterday: yesterdayUsage?.aggregates ?? null, daily: dailyUsage?.series ?? null, opsSeries: opsTimeseries?.series ?? null, devices: deviceResult?.devices ?? null, teams: teamResult?.teams ?? null, models, providers: providerResult?.providers ?? null, aggregates: usage?.aggregates ?? null, records: usage?.records ?? null, hourly: hourlyResult?.series ?? null, trend: trendResult ? [...zeroed.values()] : null, trendSeries, alerts: alertResult?.alerts ?? null, quota: quotaResult?.requests.filter((row) => row.status === "pending") ?? null, orders: orderResult?.orders.slice(0, 5) ?? null, errors, updatedAt: now });
    setLoading(false);
  }, [canManagePlatform, period]);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => { if (!document.hidden && !reviewBusy.current) void load(); }, 30_000);
    return () => { window.clearInterval(timer); request.current?.abort(); };
  }, [load]);

  const review = async (row: QuotaRequest, decision: "approve" | "reject") => {
    if (reviewBusy.current) return;
    const teamName = data?.teams?.find((team) => team.id === row.teamId)?.name ?? "团队";
    const answer = await confirmDialog({ title: decision === "approve" ? `批准 ${teamName} 增加 ${credits(Number(row.amountCredits))} credit？` : `驳回 ${teamName} 的额度申请？`, message: decision === "approve" ? "批准后将一次性增加团队额度。" : "驳回后团队可查看审批说明。", confirmText: decision === "approve" ? "批准" : "驳回", danger: decision === "reject", input: { label: "审批说明", placeholder: "处理依据或说明（可选）" } });
    if (!answer.confirmed) return;
    reviewBusy.current = true;
    setBusyId(row.id);
    try {
      await api(`/quota-requests/${row.id}/review`, { method: "POST", body: JSON.stringify({ decision, note: answer.value }) });
      await Promise.all([load(), refreshSession()]);
      toastOk(decision === "approve" ? "额度申请已批准并增加团队额度" : "额度申请已驳回");
    } catch (error) { toastErr(error instanceof Error ? error.message : "审批失败"); }
    finally { reviewBusy.current = false; setBusyId(null); }
  };

  const ops = data?.ops;
  const pendingCount = data?.quota?.length ?? 0;
  const providerAttention = data?.providers?.filter((provider) => provider.disabledMode === "auto" || (ops?.providerStats.find((stat) => stat.providerId === provider.id)?.errorRate ?? 0) >= 0.1).length ?? 0;
  const prompt = ops ? ops.inputTokens + ops.cacheReadTokens + ops.cacheCreationTokens : 0;
  const hourly = useMemo(() => {
    const result = new Map<number, { requests: number; credits: number; periods: string[] }>();
    for (const row of data?.hourly ?? []) {
      const hour = Number(row.bucketStart.slice(11, 13));
      const previous = result.get(hour) ?? { requests: 0, credits: 0, periods: [] };
      result.set(hour, { requests: previous.requests + row.requests, credits: previous.credits + row.creditsMicro / 1_000_000, periods: [...previous.periods, row.bucketStart.slice(5, 16).replace("T", " ")] });
    }
    return [...result].map(([hour, reading]) => ({ hour, ...reading }));
  }, [data?.hourly]);
  const peak = [...hourly].sort((a, b) => b.requests - a.requests)[0];
  const topModels = [...(data?.aggregates ?? [])].filter((row) => row.creditsMicro > 0).sort((a, b) => b.creditsMicro - a.creditsMicro);
  const modelSegments = topModels.slice(0, 5).map((row) => ({ key: row.model, label: row.modelDisplayName || row.model, value: row.creditsMicro / 1_000_000, color: entityColor(row.model) }));
  if (topModels.length > 5) modelSegments.push({ key: "other", label: "其他模型", value: topModels.slice(5).reduce((sum, row) => sum + row.creditsMicro, 0) / 1_000_000, color: "#788BA2" });
  const histLabels = ["<1s", "1–2s", "2–3s", "3–5s", "5–8s", "8–12s", "12–20s", ">20s"];
  const histLimits = [1000, 2000, 3000, 5000, 8000, 12_000, 20_000, Infinity];
  const histogram: ChartDatum[] = histLabels.map((label) => ({ label, requests: 0 }));
  for (const row of data?.records ?? []) if (row.latencyMs !== null && Number.isFinite(row.latencyMs)) {
    const index = histLimits.findIndex((limit) => row.latencyMs! < limit);
    if (index >= 0) histogram[index].requests = Number(histogram[index].requests) + 1;
  }
  const todayRequests = data?.today?.reduce((sum, row) => sum + row.requests, 0) ?? null;
  const todayCredits = data?.today?.reduce((sum, row) => sum + row.creditsMicro / 1_000_000, 0) ?? null;
  const yesterdayRequests = data?.yesterday?.reduce((sum, row) => sum + row.requests, 0) ?? null;
  const yesterdayCredits = data?.yesterday?.reduce((sum, row) => sum + row.creditsMicro / 1_000_000, 0) ?? null;
  const currentDate = data ? formatShanghaiTime(data.updatedAt.toISOString()).slice(0, 10) : "";
  const todayHours = data?.hourly?.filter((row) => row.bucketStart.slice(0, 10) === currentDate) ?? [];
  // 滚动 24 小时的首尾可能属于同一个墙钟小时；当前小时读数只取今天的这个桶。
  const currentHourKey = data ? formatShanghaiTime(data.updatedAt.toISOString()).replace(" ", "T").slice(0, 13) : "";
  const currentHourRequests = data?.hourly?.filter((row) => row.bucketStart.startsWith(currentHourKey)).reduce((sum, row) => sum + row.requests, 0) ?? null;
  const weekendTotals = { requests: 0, days: 0 }, weekdayTotals = { requests: 0, days: 0 };
  if (data?.daily) for (let index = 0; index < 14; index++) {
    const date = formatShanghaiTime(new Date(data.updatedAt.getTime() - index * 86_400_000).toISOString()).slice(0, 10);
    const day = new Date(`${date}T00:00:00Z`).getUTCDay();
    const totals = day === 0 || day === 6 ? weekendTotals : weekdayTotals;
    totals.days += 1;
    totals.requests += data.daily.find((row) => row.bucketStart.startsWith(date))?.requests ?? 0;
  }
  const weekendFactor = weekendTotals.days && weekdayTotals.days && weekdayTotals.requests > 0 ? weekendTotals.requests / weekendTotals.days / (weekdayTotals.requests / weekdayTotals.days) : null;
  const change = (value: number | null, previous: number | null) => value !== null && previous !== null && previous > 0 ? (value - previous) / previous : null;
  const deviceDays = Array.from({ length: 14 }, (_, index) => {
    if (!data?.devices) return null;
    const date = formatShanghaiTime(new Date(data.updatedAt.getTime() - (13 - index) * 86_400_000).toISOString()).slice(0, 10);
    return data.devices.filter((device) => !device.revokedAt && device.lastUsedAt && formatShanghaiTime(device.lastUsedAt).startsWith(date)).length;
  });
  const metrics: Reading[] = [
    { label: "今日请求", value: todayRequests, decimals: 0, unit: "", note: "上海自然日累计请求", spark: todayHours.map((hour) => hour.requests), area: true, delta: change(todayRequests, yesterdayRequests), deltaLabel: "相对昨日全日请求数" },
    { label: "今日消耗", value: todayCredits, decimals: 2, unit: "credit", note: "上海自然日合法 usage 实扣", spark: todayHours.map((hour) => hour.creditsMicro / 1_000_000), area: true, delta: change(todayCredits, yesterdayCredits), deltaLabel: "相对昨日全日实扣 credit" },
    { label: "平台团队", value: data?.teams?.length ?? null, decimals: 0, unit: "个", note: "已创建的团队" },
    { label: "活跃设备", value: ops?.activeDevices ?? null, decimals: 0, unit: "台", note: "近 24 小时有请求且未吊销的设备", spark: deviceDays, sparkLabel: "最近使用日分布" },
    { label: "错误率", value: ops && ops.requests > 0 ? ops.errorRequests / ops.requests * 100 : null, decimals: 2, unit: "%", note: "近 24 小时错误 / 全部请求", spark: data?.opsSeries?.map((row) => row.errors) ?? [], sparkLabel: "5 分钟错误走势", invert: true },
    { label: "P95 延迟", value: ops?.p95LatencyMs === null || ops?.p95LatencyMs === undefined ? null : ops.p95LatencyMs / 1000, decimals: 2, unit: "s", note: "近 24 小时 P95 延迟", spark: data?.opsSeries?.map((row) => row.p95LatencyMs === null ? null : row.p95LatencyMs / 1000) ?? [], sparkLabel: "5 分钟 P95 走势", invert: true },
    { label: "缓存命中", value: prompt > 0 && ops ? ops.cacheReadTokens / prompt * 100 : null, decimals: 1, unit: "%", note: "缓存读 / 总 prompt token", spark: data?.daily?.map((row) => { const total = row.inputTokens + row.cacheReadTokens + row.cacheCreationTokens; return total > 0 ? row.cacheReadTokens / total : null; }) ?? [], sparkLabel: "近 14 日缓存率" },
  ];

  if (!canManagePlatform) return <Notice tone="stop" k="无权访问">平台总览需要平台管理权限。</Notice>;
  return <div className="polar-metrics polar-overview">
    <header className="page-head"><div><p className="eyebrow">Night shift · {formatShanghaiTime((data?.updatedAt ?? new Date()).toISOString()).slice(5, 16)}</p><h1 className="page-title">{providerAttention ? `有 ${providerAttention} 个上游需要关注` : "平台运行总览"}</h1></div>
      <div className="form-row"><button className="btn btn-ghost" type="button" aria-label="刷新平台总览" disabled={loading} onClick={() => void load()}><Glyph icon={RefreshCw} size={14} />{loading ? "更新中" : "刷新"}</button><a className="btn btn-ghost" href="#/ops"><Glyph icon={Activity} size={14} />运维大盘</a><a className="btn" href="#/usage"><Glyph icon={ChartColumn} size={14} />用量分析</a></div></header>
    {data?.errors.length ? <Notice tone="stop" k="部分读数加载失败">{data.errors.join("；")}</Notice> : null}
    <div className="polar-dashboard-grid">
      <Panel title="实时请求拓扑" subtitle="设备 → 网关 → 模型 → 上游 · 线宽按近 1 小时流量 · 光点显示请求流向" span={8} className="polar-topology-panel" action={providerAttention ? <Chip tone="stop">{providerAttention} 个上游需关注</Chip> : <Chip tone="info">目录路由</Chip>}>
        {data?.models && data.providers ? <RequestTopology models={data.models} providers={data.providers} requests={data.topologyOps?.providerStats ?? []} activeDevices={data.topologyOps?.activeDevices ?? 0} modelRequests={data.topologyUsage ?? []} windowRequests={data.topologyOps?.requests} /> : <Empty failed={Boolean(data)}>正在加载模型路由…</Empty>}
      </Panel>
      <Panel title="夜钟 · 近 24 小时" subtitle="扇形长度为每小时请求数，高亮为当前小时" span={4}>
        {data?.hourly ? <><NightClock hours={hourly} now={data.updatedAt} todayRequests={data.today?.reduce((sum, row) => sum + row.requests, 0)} /><div className="polar-substats"><div><small>峰值时段</small><b>{peak?.requests ? `${String(peak.hour).padStart(2, "0")}:00` : "—"}</b></div><div><small>当前小时</small><b>{currentHourRequests === null ? "—" : number(currentHourRequests)}</b></div><div title="近 14 日周末请求日均 / 工作日请求日均"><small>周末系数</small><b>{weekendFactor === null ? "—" : `${weekendFactor.toFixed(2)}×`}</b></div></div></> : <Empty failed={Boolean(data)}>正在加载小时读数…</Empty>}
      </Panel>
      <section className="polar-panel polar-span-12 polar-readings" aria-label="平台关键读数">{metrics.map((reading) => <OverviewReading key={reading.label} reading={reading} />)}</section>
      <Panel title={`消费趋势 · 近 ${period === "30d" ? "30" : "7"} 天`} subtitle="每日 credit 实扣 · 按上海墙钟日界分桶" span={8} action={<div className="polar-segmented"><button type="button" aria-pressed={period === "7d"} onClick={() => { if (period !== "7d") { setData(null); setPeriod("7d"); } }}>7 天</button><button type="button" aria-pressed={period === "30d"} onClick={() => { if (period !== "30d") { setData(null); setPeriod("30d"); } }}>30 天</button></div>}>
        {data?.trend ? <PolarTrendChart data={data.trend} series={data.trendSeries} height={250} stacked={data.trendSeries.length > 1} /> : <Empty failed={Boolean(data)}>正在加载消费趋势…</Empty>}
      </Panel>
      <Panel title={`消费构成 · 近 ${period === "30d" ? "30" : "7"} 天`} subtitle="各模型占 credit 消耗的比例" span={4}>
        {data?.aggregates ? <PolarSegments rows={modelSegments} height={14} format={(value) => `${polarCredits(value)} credit`} /> : <Empty failed={Boolean(data)} />}
        <h3 className="polar-subheading">团队消耗排行 · 累计</h3>
        {data?.teams ? <PolarBars rows={[...data.teams].sort((a, b) => Number(b.usedCredits) - Number(a.usedCredits)).slice(0, 6).map((team) => ({ key: team.id, label: team.name, value: Number(team.usedCredits) }))} format={(value) => polarCredits(value, 0)} rank unit="credit" /> : <Empty failed={Boolean(data)} />}
      </Panel>
      <Panel title="上游健康" subtitle="六边形为供应商凭据 · 数字为窗口非错误请求占比" span={4} action={<a className="btn btn-ghost btn-sm" href="#/providers">管理<Glyph icon={ArrowUpRight} size={12} /></a>}>
        {data?.providers?.length ? <div className="polar-provider-list">{data.providers.slice(0, 8).map((provider) => {
          const stat = ops?.providerStats.find((row) => row.providerId === provider.id), state = !provider.enabled ? "stop" : stat && stat.errorRate >= 0.1 ? "hot" : "ok";
          return <div className="polar-provider-row" key={provider.id}><span><i className={`polar-status-dot ${state}`} />{provider.name}</span><PolarStatusHexes items={[{ key: provider.id, label: provider.name, tone: !provider.hasKey ? "idle" : state === "stop" ? "crit" : state === "hot" ? "warn" : "ok", detail: `${provider.hasKey ? "已配置凭据" : "未配置凭据"} · ${provider.enabled ? "启用" : "停用"}` }]} /><b>{stat && stat.requests > 0 ? percent(1 - stat.errorRate) : !provider.enabled ? "已停用" : !ops ? "读数未知" : "无请求"}</b></div>;
        })}</div> : <Empty failed={data?.providers === null}>暂无上游供应商。</Empty>}
      </Panel>
      <Panel title="告警" subtitle={`${data?.alerts?.length ?? 0} 条未处理`} span={4} action={<a className="btn btn-ghost btn-sm" href="#/ops">详情</a>}>
        {data?.alerts?.length ? <div className="polar-alert-list">{data.alerts.map((alert) => <div className="polar-alert-row" key={alert.id}><Glyph icon={TriangleAlert} size={16} /><div><b>{alert.ruleName || alert.metric}</b><p>{alert.message}</p></div><time>{formatShanghaiTime(alert.createdAt).slice(11, 16)}</time></div>)}</div> : <Empty failed={data?.alerts === null}>当前没有未处理告警。</Empty>}
      </Panel>
      <Panel title="待审批额度申请" subtitle={`${pendingCount} 条 · 合计 ${credits(data?.quota?.reduce((sum, row) => sum + Number(row.amountCredits), 0) ?? 0)} credit`} span={4} action={<a className="btn btn-ghost btn-sm" href="#/quota-requests">全部</a>}>
        {data?.quota?.length ? <div className="polar-quota-list">{data.quota.slice(0, 3).map((row) => <div className="polar-quota-row" key={row.id}><div><b>{data.teams?.find((team) => team.id === row.teamId)?.name ?? row.teamId.slice(0, 8)}</b><strong>+{credits(Number(row.amountCredits))}</strong></div><p>{row.reason}</p><div className="form-row"><button className="btn btn-ghost btn-sm polar-approve" type="button" disabled={Boolean(busyId)} onClick={() => void review(row, "approve")}><Glyph icon={Check} size={12} />批准</button><button className="btn btn-ghost btn-sm polar-reject" type="button" disabled={Boolean(busyId)} onClick={() => void review(row, "reject")}><Glyph icon={X} size={12} />驳回</button><small>待审批</small></div></div>)}</div> : <Empty failed={data?.quota === null}>所有额度申请已处理。</Empty>}
      </Panel>
      <Panel title="请求延迟分布" subtitle={`区间内最近 ${data?.records?.length ?? 0} 条请求 · 横轴为秒`} span={6}>
        <PolarTrendChart data={histogram} series={[{ key: "requests", label: "请求数", color: "var(--accent)", kind: "bar", format: number, axisFormat: number }]} height={200} emptyText={data?.records === null ? "请求记录加载失败。" : "暂无带延迟的请求记录。"} />
      </Panel>
      <Panel title="最近充值" subtitle="平台近期订单 · 支付结果以可信确认入账为准" span={6} action={<a className="btn btn-ghost btn-sm" href="#/payments">充值与余额</a>}>
        <div className="table-scroll"><table className="table"><thead><tr><th>时间</th><th>对象</th><th className="num">金额</th><th className="num">credit</th><th>状态</th></tr></thead><tbody>
          {data?.orders?.map((order) => <tr key={order.id}><td className="num">{formatShanghaiTime(order.createdAt).slice(5, 16)}</td><td>{order.targetType === "team" ? data.teams?.find((team) => team.id === (order.teamId || order.targetId))?.name ?? "团队账户" : "个人账户"}</td><td className="num">¥{credits(Number(order.amountYuan))}</td><td className="num">+{credits(Number(order.creditAmount))}</td><td><Chip tone={order.status === "paid" ? "ok" : order.status === "closed" ? "mute" : "info"}>{order.status === "paid" ? "已确认入账" : order.status === "closed" ? "已关闭" : "待付款 / 待确认"}</Chip></td></tr>)}
          {!data?.orders?.length ? <tr><td colSpan={5} className="readout">{data?.orders === null ? "充值记录加载失败。" : "暂无充值记录。"}</td></tr> : null}
        </tbody></table></div>
      </Panel>
    </div>
  </div>;
}
