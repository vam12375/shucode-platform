import {
  MICRO_CREDITS_PER_CREDIT,
  formatCreditsDisplay,
} from "@shucode-platform/contracts/credits";
import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { api, apiUrl } from "../api.js";
import { useAuth } from "../auth.js";
import {
  summarizeHistoricalCosts,
  type PriceSnapshot,
} from "../usage-costs.js";
import {
  ComposedTrendChart,
  ModelDonutChart,
  type ChartDatum,
  type ChartSeries,
} from "../components/charts.js";
import { toastErr } from "../components/Feedback.js";
import { formatPct } from "../components/Gauge.js";
import { Select } from "../components/Select.js";
import { Chip, Notice, type ChipTone } from "../components/StatusChip.js";
import {
  formatShanghaiTime,
  shanghaiDateOf,
  shanghaiMonthRange,
} from "../time.js";

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

interface ProviderOption {
  id: string;
  name: string;
  enabled: boolean;
}

interface ModelOption {
  modelId: string;
  displayName: string;
  enabled: boolean;
  inputPricePerMtok: string;
  outputPricePerMtok: string;
  cacheReadPricePerMtok: string;
  cacheWritePricePerMtok: string;
  offPeakMultiplier: string;
}

interface DeviceOption {
  id: string;
  name: string;
  platform: string | null;
}

interface AggregateRow {
  model: string;
  requests: number;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  creditsMicro: number;
  latencyMsSum: number;
}

/** 与 /console/api/usage/series 响应对齐。 */
interface SeriesRow {
  bucketStart: string;
  requests: number;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  creditsMicro: number;
  latencyMsSum: number;
}

type ChartPreset =
  | "today"
  | "yesterday"
  | "7d"
  | "14d"
  | "30d"
  | "month"
  | "lastMonth";
type ChartGranularity = "day" | "hour";

/** 与 /console/api/usage 响应对齐：后端直接返回存储层记录，字段全量下发。 */
interface UsageRecordRow {
  id: string;
  requestId: string;
  userId: string;
  teamId: string;
  deviceTokenId: string | null;
  model: string;
  upstreamModel: string;
  priceSnapshot?: PriceSnapshot | null;
  providerId: string | null;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  creditsMicro: number;
  status: string;
  errorType: string | null;
  clientIp: string | null;
  latencyMs: number | null;
  createdAt: string;
}

interface DayPoint {
  date: string;
  requests: number;
  creditsMicro: number;
  /** 当日缓存读命中 token 总量。 */
  cacheReadTokens: number;
  /** 当日总 prompt token：入（新输入）+ 缓存读 + 缓存写，缓存率分母。 */
  promptTokens: number;
}

/** 与 /console/api/usage 响应对齐：records 为当前页，nextCursor 为下一页游标。 */
interface UsagePageData {
  aggregates: AggregateRow[];
  records: UsageRecordRow[];
  nextCursor: string | null;
}

const DAY = 86_400_000;

function credits(micro: number): string {
  return formatCreditsDisplay(micro / MICRO_CREDITS_PER_CREDIT);
}

/** 成本价库的金额展示：币种随条目；空币种按 USD（历史）处理。 */
function money(value: number, currency: string): string {
  try {
    return new Intl.NumberFormat("en-US", {
      style: "currency",
      currency: currency || "USD",
      currencyDisplay: "narrowSymbol",
      maximumFractionDigits: 4,
    }).format(value);
  } catch {
    return `${value.toFixed(4)} ${currency || "USD"}`;
  }
}

function statusChip(status: string): { tone?: ChipTone; label: string } {
  if (status === "ok") return { tone: "ok", label: "成功" };
  if (status === "aborted") return { tone: "hot", label: "中断" };
  return { tone: "stop", label: "错误" };
}

/**
 * 缓存命中率 = 缓存读 ÷ 总 prompt token。
 * 库内 input_tokens 是不含缓存的新输入，缓存读/写单独成列，所以总 prompt
 * = 新输入 + 缓存读 + 缓存写；缓存写是首写（未命中），只进分母不进分子。
 * 范围内没有 prompt 时返回 null（显示 —），避免 0/0 假装成 0% 命中。
 */
function cacheRate(
  cacheReadTokens: number,
  promptTokens: number,
): number | null {
  return promptTokens > 0 ? cacheReadTokens / promptTokens : null;
}

/** 缓存率的展示形式：无 prompt 显示 —，否则显示百分比。 */
function cacheRateText(cacheReadTokens: number, promptTokens: number): string {
  const rate = cacheRate(cacheReadTokens, promptTokens);
  return rate === null ? "—" : formatPct(rate);
}

/** 大 token 数的紧凑展示：578.66M / 3.25B 风格。 */
function compactToken(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(2)}B`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(2)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}K`;
  return n.toLocaleString("en-US");
}

const CHART_PRESETS: { value: ChartPreset; label: string }[] = [
  { value: "today", label: "今天" },
  { value: "yesterday", label: "昨天" },
  { value: "7d", label: "近 7 天" },
  { value: "14d", label: "近 14 天" },
  { value: "30d", label: "近 30 天" },
  { value: "month", label: "本月" },
  { value: "lastMonth", label: "上月" },
];

/** 预设的上海墙钟日期区间（闭集，含首尾）。 */
function presetRange(preset: ChartPreset): { from: string; to: string } {
  const now = new Date();
  const today = shanghaiDateOf(now);
  if (preset === "today") return { from: today, to: today };
  if (preset === "yesterday") {
    const yesterday = shanghaiDateOf(new Date(now.getTime() - DAY));
    return { from: yesterday, to: yesterday };
  }
  const span =
    preset === "7d" ? 7 : preset === "14d" ? 14 : preset === "30d" ? 30 : 0;
  if (span > 0) {
    return {
      from: shanghaiDateOf(new Date(now.getTime() - (span - 1) * DAY)),
      to: today,
    };
  }
  return preset === "month"
    ? shanghaiMonthRange(now, 0)
    : shanghaiMonthRange(now, -1);
}

/** 补齐时间轴的空桶：key 与后端 bucketStart 对齐（上海墙钟）。hours 全天补零，未来小时显示 0 柱。 */
function buildBucketKeys(
  from: string,
  to: string,
  granularity: ChartGranularity,
): string[] {
  const keys: string[] = [];
  for (
    let t = Date.parse(`${from}T00:00:00Z`);
    t <= Date.parse(`${to}T00:00:00Z`);
    t += DAY
  ) {
    const day = new Date(t).toISOString().slice(0, 10);
    if (granularity === "day") {
      keys.push(`${day}T00:00:00`);
    } else {
      for (let h = 0; h < 24; h += 1)
        keys.push(`${day}T${String(h).padStart(2, "0")}:00:00`);
    }
  }
  return keys;
}

/** 详情弹窗里的单行键值：readout 作键，右对齐作值。 */
function DetailRow({ k, children }: { k: string; children: ReactNode }) {
  return (
    <div
      className="form-row"
      style={{
        justifyContent: "space-between",
        alignItems: "baseline",
        gap: 16,
        marginTop: 8,
      }}
    >
      <span className="readout" style={{ flexShrink: 0 }}>
        {k}
      </span>
      <span style={{ textAlign: "right", wordBreak: "break-all" }}>
        {children}
      </span>
    </div>
  );
}

export function UsagePage() {
  const { isRoot, canManageTeam, user } = useAuth();
  const [teams, setTeams] = useState<TeamOption[]>([]);
  const [userEmails, setUserEmails] = useState<Map<string, string>>(new Map());
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [filterUserId, setFilterUserId] = useState("");
  const [filterModel, setFilterModel] = useState("");
  const [filterProviderId, setFilterProviderId] = useState("");
  const [filterStatus, setFilterStatus] = useState("");
  const [filterRequestId, setFilterRequestId] = useState("");
  const [providers, setProviders] = useState<ProviderOption[]>([]);
  const [modelOptions, setModelOptions] = useState<ModelOption[]>([]);
  const [devices, setDevices] = useState<DeviceOption[]>([]);
  const [detail, setDetail] = useState<UsageRecordRow | null>(null);
  const [aggregates, setAggregates] = useState<AggregateRow[]>([]);
  const [records, setRecords] = useState<UsageRecordRow[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [moreLoading, setMoreLoading] = useState(false);
  const [days, setDays] = useState<DayPoint[]>([]);
  // 默认按小时看当天走势；大范围（>7 天）切预设时仍会自动回落按天。
  const [chartPreset, setChartPreset] = useState<ChartPreset>("today");
  const [chartGranularity, setChartGranularity] =
    useState<ChartGranularity>("hour");
  const [donutBy, setDonutBy] = useState<"tokens" | "credits">("tokens");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    void (async () => {
      try {
        const [teamData, userData, providerData, modelData, deviceData] =
          await Promise.all([
            api<{ teams: TeamOption[] }>("/teams"),
            isRoot || canManageTeam
              ? api<{ users: UserOption[] }>("/users")
              : Promise.resolve({ users: [user] }),
            isRoot
              ? api<{ providers: ProviderOption[] }>("/providers")
              : Promise.resolve({ providers: [] }),
            api<{ models: ModelOption[] }>(
              isRoot ? "/models?includeDisabled=1" : "/models",
            ),
            api<{ devices: DeviceOption[] }>("/devices"),
          ]);
        setTeams(teamData.teams);
        setUserEmails(
          new Map(userData.users.map((user) => [user.id, user.email])),
        );
        setProviders(providerData.providers);
        setModelOptions(modelData.models);
        setDevices(deviceData.devices);
      } catch (loadError) {
        setError(loadError instanceof Error ? loadError.message : "加载失败");
      }
    })();
  }, [isRoot, canManageTeam, user]);

  // 趋势图为平台口径，与页内筛选无关；一次 series 请求按上海墙钟分桶，
  // 前端补零对齐时间轴（费用图与缓存率图共用这份数据）。
  useEffect(() => {
    void (async () => {
      const { from, to } = presetRange(chartPreset);
      const keys = buildBucketKeys(from, to, chartGranularity);
      const zeroed = new Map(
        keys.map((key) => [
          key,
          {
            date: key,
            requests: 0,
            creditsMicro: 0,
            cacheReadTokens: 0,
            promptTokens: 0,
          },
        ]),
      );
      const query = new URLSearchParams({
        from,
        to: `${to}T23:59:59`,
        granularity: chartGranularity,
      });
      const data = await api<{ series: SeriesRow[] }>(
        `/usage/series?${query.toString()}`,
      ).catch(() => null);
      if (data) {
        for (const row of data.series) {
          const bucket = zeroed.get(row.bucketStart);
          if (!bucket) continue;
          bucket.requests = row.requests;
          bucket.creditsMicro = row.creditsMicro;
          bucket.cacheReadTokens = row.cacheReadTokens;
          bucket.promptTokens =
            row.inputTokens + row.cacheReadTokens + row.cacheCreationTokens;
        }
      }
      setDays([...zeroed.values()]);
    })();
  }, [chartPreset, chartGranularity]);

  const usageParams = useCallback(
    (cursor?: string) => {
      const params = new URLSearchParams();
      if (from) params.set("from", `${from}T00:00:00`);
      if (to) params.set("to", `${to}T23:59:59`);
      if (filterUserId) params.set("userId", filterUserId);
      if (filterModel) params.set("model", filterModel);
      if (filterProviderId) params.set("providerId", filterProviderId);
      if (filterStatus) params.set("status", filterStatus);
      if (filterRequestId.trim())
        params.set("requestId", filterRequestId.trim());
      if (cursor) params.set("cursor", cursor);
      return params;
    },
    [
      from,
      to,
      filterUserId,
      filterModel,
      filterProviderId,
      filterStatus,
      filterRequestId,
    ],
  );

  const fetchUsage = useCallback(
    (cursor: string | null) => {
      const params = usageParams(cursor ?? undefined);
      return api<UsagePageData>(
        `/usage${params.size > 0 ? `?${params.toString()}` : ""}`,
      );
    },
    [usageParams],
  );

  const exportUsage = useCallback(async (): Promise<void> => {
    const params = usageParams();
    const response = await fetch(
      apiUrl(`/usage/export${params.size > 0 ? `?${params.toString()}` : ""}`),
      {
        credentials: "include",
      },
    );
    if (!response.ok) {
      toastErr(`导出失败 (${response.status})`, "导出失败");
      return;
    }
    const blob = await response.blob();
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `usage-${new Date().toISOString().slice(0, 19).replaceAll(":", "")}.csv`;
    anchor.click();
    URL.revokeObjectURL(url);
  }, [usageParams]);

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

  const totalCredits = aggregates.reduce(
    (sum, row) => sum + row.creditsMicro,
    0,
  );
  const totalRequests = aggregates.reduce((sum, row) => sum + row.requests, 0);
  const totalInputTokens = aggregates.reduce(
    (sum, row) => sum + row.inputTokens,
    0,
  );
  const totalOutputTokens = aggregates.reduce(
    (sum, row) => sum + row.outputTokens,
    0,
  );
  const totalCacheTokens = aggregates.reduce(
    (sum, row) => sum + row.cacheReadTokens + row.cacheCreationTokens,
    0,
  );
  const totalLatencyMs = aggregates.reduce(
    (sum, row) => sum + row.latencyMsSum,
    0,
  );
  // 平均耗时仅在既有延迟记录时给出（部分错误/中断记录无 latency）。
  const avgLatencyS =
    totalLatencyMs > 0 && totalRequests > 0
      ? totalLatencyMs / totalRequests / 1000
      : null;

  // 按当前模型售价重算的区间估算（全高峰 ~ 全空闲）。
  // 账单本身是请求发生时的实扣，历史记录不随调价回写；这里只做口径对照。
  const currentPriceEstimate = useMemo(() => {
    let peakMicro = 0;
    let offPeakMicro = 0;
    let missingModels = 0;
    for (const row of aggregates) {
      const model = modelOptions.find((item) => item.modelId === row.model);
      if (!model) {
        const hasUsage =
          row.inputTokens +
            row.outputTokens +
            row.cacheReadTokens +
            row.cacheCreationTokens +
            row.creditsMicro >
          0;
        if (hasUsage) missingModels += 1;
        continue;
      }
      const price = (value: string) => Number.parseFloat(value) || 0;
      const micro =
        row.inputTokens * price(model.inputPricePerMtok) +
        row.outputTokens * price(model.outputPricePerMtok) +
        row.cacheReadTokens * price(model.cacheReadPricePerMtok) +
        row.cacheCreationTokens * price(model.cacheWritePricePerMtok);
      peakMicro += micro;
      const multiplier = price(model.offPeakMultiplier) || 1;
      offPeakMicro += micro * (multiplier > 0 ? multiplier : 1);
    }
    return {
      peak: peakMicro / MICRO_CREDITS_PER_CREDIT,
      offPeak: offPeakMicro / MICRO_CREDITS_PER_CREDIT,
      missingModels,
    };
  }, [aggregates, modelOptions]);

  const costEstimate = useMemo(
    () => summarizeHistoricalCosts(records),
    [records],
  );

  // 趋势图当前范围（上海墙钟），预设/粒度切换后随 days 一起刷新。
  const chartRange = useMemo(() => presetRange(chartPreset), [chartPreset]);
  const presetLabel =
    CHART_PRESETS.find((item) => item.value === chartPreset)?.label ?? "";

  // 模型分布环形图：Token 口径 = 总 prompt（入 + 缓存读 + 缓存写），费用口径 = CR。
  const donutSegments = useMemo(() => {
    const valueOf = (row: AggregateRow) =>
      donutBy === "tokens"
        ? row.inputTokens + row.cacheReadTokens + row.cacheCreationTokens
        : row.creditsMicro;
    const rows = aggregates
      .map((row) => ({ model: row.model, value: valueOf(row) }))
      .filter((row) => row.value > 0)
      .sort((a, b) => b.value - a.value);
    const total = rows.reduce((sum, row) => sum + row.value, 0);
    const top = rows.slice(0, 6);
    const rest = rows.slice(6);
    if (rest.length > 0) {
      top.push({
        model: `其他 ${rest.length} 模型`,
        value: rest.reduce((sum, row) => sum + row.value, 0),
      });
    }
    return { segments: top, total };
  }, [aggregates, donutBy]);

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

  // 缓存率图整体口径：分母用同一批趋势数据的 prompt token。
  const chartCacheRead = days.reduce(
    (sum, day) => sum + day.cacheReadTokens,
    0,
  );
  const chartCachePrompt = days.reduce((sum, day) => sum + day.promptTokens, 0);
  const chartCacheRate = cacheRate(chartCacheRead, chartCachePrompt);

  const bucketLabel = (date: string): string =>
    chartGranularity === "day" ? date.slice(5, 10) : date.slice(11, 16);

  const costTrend: ChartDatum[] = days.map((day) => ({
    label: bucketLabel(day.date),
    credits: day.creditsMicro / MICRO_CREDITS_PER_CREDIT,
    requests: day.requests,
  }));
  const costSeries: ChartSeries[] = [
    {
      key: "credits",
      label: "费用 (¥)",
      color: "var(--amber)",
      kind: "bar",
      format: (value) =>
        value.toLocaleString("en-US", { maximumFractionDigits: 4 }),
      axisFormat: (value) =>
        value.toLocaleString("en-US", { maximumFractionDigits: 2 }),
    },
    {
      key: "requests",
      label: "请求数",
      color: "var(--ink)",
      kind: "line",
      axis: "right",
      format: (value) => Math.round(value).toLocaleString("en-US"),
      axisFormat: (value) => Math.round(value).toLocaleString("en-US"),
    },
  ];

  const cacheTrend: ChartDatum[] = days.map((day) => {
    const rate = cacheRate(day.cacheReadTokens, day.promptTokens);
    return {
      label: bucketLabel(day.date),
      cacheRead: day.cacheReadTokens,
      hitRate: rate === null ? null : rate * 100,
    };
  });
  const cacheSeries: ChartSeries[] = [
    {
      key: "cacheRead",
      label: "缓存读 token",
      color: "var(--amber)",
      kind: "area",
      format: (value) => compactToken(value),
      axisFormat: (value) => compactToken(value),
    },
    {
      key: "hitRate",
      label: "命中率",
      color: "var(--info)",
      kind: "line",
      axis: "right",
      format: (value) => `${value.toFixed(1)}%`,
      axisFormat: (value) => `${Math.round(value)}%`,
    },
  ];

  // 详情弹窗的设备显示：名称 + 客户端上报的系统；凭据已删除时回退 ID 前缀。
  const deviceLabel = (deviceTokenId: string | null): string => {
    if (!deviceTokenId) return "—";
    const device = devices.find((item) => item.id === deviceTokenId);
    if (!device) return deviceTokenId.slice(0, 8);
    return device.platform
      ? `${device.name} · ${device.platform}`
      : device.name;
  };

  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">计量 / 流水</p>
          <h1>{isRoot ? "用量" : canManageTeam ? "用量与消费" : "我的用量"}</h1>
        </div>
        <p className="readout m0">
          共 {totalRequests.toLocaleString("en-US")} 次请求 · ¥
          {credits(totalCredits)}
        </p>
      </header>

      {error ? (
        <Notice tone="stop" k="加载失败">
          {error}
        </Notice>
      ) : null}
      {nearQuotaTeam ? (
        <Notice
          k={nearQuotaTeam.team.name}
          action={
            isRoot
              ? {
                  href: `#/teams/${nearQuotaTeam.team.id}`,
                  label: "调整额度",
                }
              : canManageTeam
                ? { href: "#/policy", label: "查看预算" }
                : undefined
          }
        >
          额度已用 {formatPct(nearQuotaTeam.ratio)}，接近上限。
        </Notice>
      ) : null}

      <section className="stat-grid" aria-label="所选范围汇总">
        <div className="stat-card">
          <p className="stat-label">总请求数</p>
          <p className="stat-value">
            {totalRequests.toLocaleString("en-US")}
            <span className="unit">次</span>
          </p>
          <p className="stat-sub">
            所选范围内{from || to ? "（含日期筛选）" : ""}
          </p>
        </div>
        <div className="stat-card">
          <p className="stat-label">总 Token</p>
          <p className="stat-value">
            {compactToken(
              totalInputTokens + totalOutputTokens + totalCacheTokens,
            )}
            <span className="unit">tok</span>
          </p>
          <p
            className="stat-sub"
            title="缓存命中按缓存读单价计费，通常远低于输入价"
          >
            输入 {compactToken(totalInputTokens)} · 输出{" "}
            {compactToken(totalOutputTokens)} · 缓存{" "}
            {compactToken(totalCacheTokens)}（按缓存价）
          </p>
        </div>
        <div className="stat-card">
          <p className="stat-label">总消费</p>
          <p className="stat-value stat-amber">
            {credits(totalCredits)}
            <span className="unit">¥</span>
          </p>
          <p className="stat-sub">网关计量 · 团队账单口径</p>
        </div>
        <div className="stat-card">
          <p className="stat-label">平均耗时</p>
          <p className="stat-value">
            {avgLatencyS === null ? "—" : avgLatencyS.toFixed(2)}
            <span className="unit">s</span>
          </p>
          <p className="stat-sub">
            {avgLatencyS === null ? "范围内暂无带延迟记录" : "成功请求均值"}
          </p>
        </div>
      </section>

      <p className="readout" style={{ margin: "0 0 18px" }}>
        计费口径：总消费为请求发生时实扣，历史记录不随调价回写；缓存命中按缓存读单价计费，通常远低于输入价。
        当前售价对照（不代表历史实扣）：¥
        {currentPriceEstimate.offPeak.toLocaleString("en-US", {
          maximumFractionDigits: 4,
        })}
        （全空闲）~ ¥
        {currentPriceEstimate.peak.toLocaleString("en-US", {
          maximumFractionDigits: 4,
        })}
        （全高峰）
        {currentPriceEstimate.missingModels > 0
          ? `；${currentPriceEstimate.missingModels} 个模型未配置售价，未计入估算`
          : ""}
        。
      </p>

      <section className="pane">
        <div className="pane-b">
          <div className="form-row">
            {isRoot || canManageTeam ? (
              <Select
                ariaLabel="按用户筛选"
                style={{ width: 180 }}
                value={filterUserId}
                onChange={setFilterUserId}
                options={[
                  { value: "", label: "全部用户" },
                  ...[...userEmails.entries()].map(([id, email]) => ({
                    value: id,
                    label: email,
                  })),
                ]}
              />
            ) : null}
            <Select
              ariaLabel="按模型筛选"
              style={{ width: 200 }}
              value={filterModel}
              onChange={setFilterModel}
              options={[
                { value: "", label: "全部模型" },
                ...modelOptions.map((model) => ({
                  value: model.modelId,
                  label: model.enabled
                    ? model.displayName
                    : `${model.displayName}（停用）`,
                })),
              ]}
            />
            {isRoot ? (
              <Select
                ariaLabel="按供应商筛选"
                style={{ width: 170 }}
                value={filterProviderId}
                onChange={setFilterProviderId}
                options={[
                  { value: "", label: "全部供应商" },
                  ...providers.map((provider) => ({
                    value: provider.id,
                    label: provider.name,
                  })),
                ]}
              />
            ) : null}
            <Select
              ariaLabel="按状态筛选"
              style={{ width: 110 }}
              value={filterStatus}
              onChange={setFilterStatus}
              options={[
                { value: "", label: "全部状态" },
                { value: "ok", label: "成功" },
                { value: "error", label: "错误" },
                { value: "aborted", label: "中断" },
              ]}
            />
            <input
              className="input"
              style={{ width: 210 }}
              value={filterRequestId}
              onChange={(event) => setFilterRequestId(event.target.value)}
              placeholder="request ID"
              aria-label="按 request ID 筛选"
            />
            <label className="form-row" style={{ gap: 6 }}>
              <span className="readout">从</span>
              <input
                className="input"
                type="date"
                value={from}
                onChange={(event) => setFrom(event.target.value)}
              />
            </label>
            <label className="form-row" style={{ gap: 6 }}>
              <span className="readout">到</span>
              <input
                className="input"
                type="date"
                value={to}
                onChange={(event) => setTo(event.target.value)}
              />
            </label>
            <button
              className="btn btn-ghost"
              type="button"
              disabled={loading}
              onClick={() => void load()}
            >
              刷新
            </button>
            <button
              className="btn btn-ghost"
              type="button"
              onClick={() => void exportUsage()}
            >
              导出 CSV
            </button>
          </div>
        </div>
      </section>

      <section className="pane d1">
        <div className="pane-h">
          <h2>费用趋势 · {presetLabel}</h2>
          <div className="form-row" style={{ gap: 8 }}>
            <Select
              ariaLabel="趋势时间范围"
              style={{ width: 126 }}
              value={chartPreset}
              onChange={(next) => {
                const preset = next as ChartPreset;
                setChartPreset(preset);
                // 大范围配小时粒度桶数过多，自动回落按天。
                if (
                  preset !== "today" &&
                  preset !== "yesterday" &&
                  preset !== "7d"
                ) {
                  setChartGranularity("day");
                }
              }}
              options={CHART_PRESETS.map((preset) => ({
                value: preset.value,
                label: preset.label,
              }))}
            />
            <Select
              ariaLabel="趋势粒度"
              style={{ width: 96 }}
              value={chartGranularity}
              onChange={(next) => setChartGranularity(next as ChartGranularity)}
              options={[
                { value: "day", label: "按天" },
                { value: "hour", label: "按小时" },
              ]}
            />
            <span className="readout">
              {chartRange.from} ~ {chartRange.to} · 单位 ¥
            </span>
          </div>
        </div>
        <div className="pane-b">
          <ComposedTrendChart
            data={costTrend}
            series={costSeries}
            height={280}
            emptyText="所选范围暂无费用数据。"
          />
        </div>
      </section>

      <section className="pane d1">
        <div className="pane-h">
          <h2>缓存率趋势 · {presetLabel}</h2>
          <span className="readout">
            {chartCachePrompt > 0
              ? `命中 ${compactToken(chartCacheRead)} tok · ${formatPct(chartCacheRate ?? 0)}`
              : "范围内暂无 prompt"}
          </span>
        </div>
        <div className="pane-b">
          <ComposedTrendChart
            data={cacheTrend}
            series={cacheSeries}
            height={240}
            emptyText="所选范围暂无 prompt 数据。"
          />
        </div>
      </section>

      <section className="pane d1">
        <div className="pane-h">
          <h2>模型分布</h2>
          <div className="form-row" style={{ gap: 6 }}>
            <button
              className="btn btn-sm"
              type="button"
              aria-pressed={donutBy === "tokens"}
              style={donutBy === "tokens" ? undefined : { opacity: 0.55 }}
              onClick={() => setDonutBy("tokens")}
            >
              按 Token
            </button>
            <button
              className="btn btn-sm"
              type="button"
              aria-pressed={donutBy === "credits"}
              style={donutBy === "credits" ? undefined : { opacity: 0.55 }}
              onClick={() => setDonutBy("credits")}
            >
              按实际消费
            </button>
          </div>
        </div>
        <div className="pane-b donut-wrap">
          {donutSegments.total > 0 ? (
            <>
              <div
                style={{ width: 280, flex: "0 0 auto" }}
                role="img"
                aria-label={`按${donutBy === "tokens" ? "Token" : "实际消费"}的模型分布`}
              >
                <ModelDonutChart
                  segments={donutSegments.segments}
                  total={donutSegments.total}
                  totalUnit={donutBy === "tokens" ? "tok" : "¥"}
                  formatValue={(value) =>
                    donutBy === "tokens" ? compactToken(value) : credits(value)
                  }
                />
              </div>
              <ul className="donut-legend">
                {donutSegments.segments.map((segment, index) => (
                  <li key={segment.model}>
                    <span
                      className={`donut-dot donut-color-${(index % 6) + 1}`}
                      aria-hidden="true"
                    />
                    <span className="donut-name">{segment.model}</span>
                    <span className="num readout">
                      {donutBy === "tokens"
                        ? compactToken(segment.value)
                        : credits(segment.value)}
                      · {formatPct(segment.value / donutSegments.total)}
                    </span>
                  </li>
                ))}
              </ul>
            </>
          ) : (
            <p className="readout" style={{ margin: 0 }}>
              所选范围内暂无分布数据。
            </p>
          )}
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
                  缓存命中率
                </th>
                <th scope="col" className="num">
                  费用（¥）
                </th>
              </tr>
            </thead>
            <tbody>
              {aggregates.map((row) => {
                const prompt =
                  row.inputTokens +
                  row.cacheReadTokens +
                  row.cacheCreationTokens;
                return (
                  <tr key={row.model}>
                    <td>{row.model}</td>
                    <td className="num">
                      {row.requests.toLocaleString("en-US")}
                    </td>
                    <td className="num">
                      {row.inputTokens.toLocaleString("en-US")}
                    </td>
                    <td className="num">
                      {row.outputTokens.toLocaleString("en-US")}
                    </td>
                    <td
                      className="num"
                      title={`缓存读 ${row.cacheReadTokens.toLocaleString("en-US")} / 总 ${prompt.toLocaleString("en-US")} tok`}
                    >
                      {cacheRateText(row.cacheReadTokens, prompt)}
                    </td>
                    <td className="num">{credits(row.creditsMicro)}</td>
                  </tr>
                );
              })}
              {aggregates.length === 0 ? (
                <tr>
                  <td colSpan={6} className="readout">
                    所选范围内暂无用量。
                  </td>
                </tr>
              ) : null}
            </tbody>
            {aggregates.length > 0 ? (
              <tfoot>
                <tr>
                  <td>合计 · {aggregates.length} 个模型</td>
                  <td className="num">
                    {totalRequests.toLocaleString("en-US")}
                  </td>
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
                  <td className="num">
                    {cacheRateText(
                      aggregates.reduce(
                        (sum, row) => sum + row.cacheReadTokens,
                        0,
                      ),
                      aggregates.reduce(
                        (sum, row) =>
                          sum +
                          row.inputTokens +
                          row.cacheReadTokens +
                          row.cacheCreationTokens,
                        0,
                      ),
                    )}
                  </td>
                  <td className="num">{credits(totalCredits)}</td>
                </tr>
              </tfoot>
            ) : null}
          </table>
        </div>
      </section>

      {isRoot ? (
        <section className="pane d1">
          <div className="pane-h">
            <h2>当前页历史成本估算</h2>
            <span className="readout">
              冻结价格快照 · 已加载 {records.length} 条请求
            </span>
          </div>
          <div className="pane-b">
            <p className="readout m0">
              仅汇总已加载记录，覆盖 {costEstimate.estimatedCount} 条；
              {costEstimate.missingCount} 条旧记录或未知成本未计入。其中{" "}
              {costEstimate.estimatedUsageCount} 条采用估算 Token
              用量。各币种独立统计，估算差额仅比较具有 CNY
              成本快照的同一批请求；这些数字不是已结算毛利或全期账单。
            </p>
          </div>
          <div className="table-scroll">
            <table className="table">
              <thead>
                <tr>
                  <th>模型</th>
                  <th>币种</th>
                  <th>已知成本请求</th>
                  <th>历史成本估算</th>
                  <th>对应实扣（CNY）</th>
                  <th>CNY 估算差额</th>
                </tr>
              </thead>
              <tbody>
                {costEstimate.groups.map((row) => (
                  <tr key={`${row.model}:${row.currency}`}>
                    <td>{row.model}</td>
                    <td>{row.currency}</td>
                    <td className="num">{row.count}</td>
                    <td className="num">
                      {money(row.costMicro / 1_000_000, row.currency)}
                    </td>
                    <td className="num">¥{credits(row.chargedMicro)}</td>
                    <td className="num">
                      {row.currency === "CNY"
                        ? money(
                            (row.chargedMicro - row.costMicro) / 1_000_000,
                            "CNY",
                          )
                        : "—"}
                    </td>
                  </tr>
                ))}
                {costEstimate.groups.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="readout">
                      暂无可估算的历史成本快照。
                    </td>
                  </tr>
                ) : null}
              </tbody>
              <tfoot>
                {costEstimate.totals.map(([currency, total]) => (
                  <tr key={currency}>
                    <td>合计</td>
                    <td>{currency}</td>
                    <td className="num">{total.count}</td>
                    <td className="num">
                      {money(total.costMicro / 1_000_000, currency)}
                    </td>
                    <td className="num">¥{credits(total.chargedMicro)}</td>
                    <td className="num">
                      {currency === "CNY"
                        ? money(
                            (total.chargedMicro - total.costMicro) / 1_000_000,
                            "CNY",
                          )
                        : "—"}
                    </td>
                  </tr>
                ))}
              </tfoot>
            </table>
          </div>
        </section>
      ) : null}

      <section className="pane d2">
        <div className="pane-h">
          <h2>最近请求</h2>
          <span className="readout">每页 100 条 · 筛选联动</span>
        </div>
        <div className="pane-b flush table-scroll vscroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">时间</th>
                <th scope="col">账号</th>
                {isRoot ? <th scope="col">真实IP</th> : null}
                <th scope="col">模型</th>
                <th scope="col">状态</th>
                <th scope="col" className="num">
                  Token（入 / 出）
                </th>
                <th scope="col" className="num">
                  费用
                </th>
                <th scope="col" aria-label="操作" />
              </tr>
            </thead>
            <tbody>
              {records.map((record) => {
                const status = statusChip(record.status);
                return (
                  <tr key={record.id}>
                    <td className="num">
                      {formatShanghaiTime(record.createdAt)}
                    </td>
                    <td>
                      {userEmails.get(record.userId) ??
                        record.userId.slice(0, 8)}
                    </td>
                    {isRoot ? <td>{record.clientIp ?? "—"}</td> : null}
                    <td>{record.model}</td>
                    <td>
                      <Chip tone={status.tone}>{status.label}</Chip>
                    </td>
                    <td className="num">
                      {record.inputTokens.toLocaleString("en-US")} /{" "}
                      {record.outputTokens.toLocaleString("en-US")}
                    </td>
                    <td className="num">{credits(record.creditsMicro)}</td>
                    <td>
                      <button
                        className="btn btn-ghost btn-sm"
                        type="button"
                        aria-label={`查看请求详情 ${record.requestId}`}
                        onClick={() => setDetail(record)}
                      >
                        详情
                      </button>
                    </td>
                  </tr>
                );
              })}
              {records.length === 0 ? (
                <tr>
                  <td colSpan={isRoot ? 8 : 7} className="readout">
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

      {detail ? (
        <div
          className="dialog-backdrop"
          onClick={() => setDetail(null)}
          onKeyDown={(event) => {
            if (event.key === "Escape") setDetail(null);
          }}
        >
          <div
            className="dialog"
            role="dialog"
            aria-modal="true"
            aria-label="请求详情"
            onClick={(event) => event.stopPropagation()}
          >
            <p className="readout dialog-k">计量 / 请求详情</p>
            <h3 className="dialog-title">{detail.model}</h3>
            {(() => {
              const status = statusChip(detail.status);
              // providerId 为 null 表示请求未绑定供应商、走了网关 env 默认上游。
              const provider = detail.providerId
                ? (providers.find((item) => item.id === detail.providerId)
                    ?.name ?? "（该供应商已删除）")
                : "网关默认上游（env 未绑定）";
              return (
                <>
                  <DetailRow k="状态">
                    <Chip tone={status.tone}>{status.label}</Chip>
                    {detail.errorType ? (
                      <code style={{ marginLeft: 8 }}>{detail.errorType}</code>
                    ) : null}
                  </DetailRow>
                  <DetailRow k="时间">
                    {formatShanghaiTime(detail.createdAt, true)}
                  </DetailRow>
                  <DetailRow k="请求 ID">
                    <code>{detail.requestId}</code>
                  </DetailRow>
                  <DetailRow k="用户">
                    {userEmails.get(detail.userId) ?? detail.userId}
                  </DetailRow>
                  <DetailRow k="团队">
                    {teams.find((team) => team.id === detail.teamId)?.name ??
                      detail.teamId.slice(0, 8)}
                  </DetailRow>
                  <DetailRow k="设备">
                    {deviceLabel(detail.deviceTokenId)}
                  </DetailRow>
                  {isRoot ? (
                    <>
                      <DetailRow k="模型映射">
                        {detail.model} → {detail.upstreamModel || "—"}
                      </DetailRow>
                      <DetailRow k="供应商">{provider}</DetailRow>
                    </>
                  ) : null}
                  <DetailRow k="Token（入 / 出 / 缓存读 / 缓存写）">
                    {detail.inputTokens.toLocaleString("en-US")} /{" "}
                    {detail.outputTokens.toLocaleString("en-US")} /{" "}
                    {detail.cacheReadTokens.toLocaleString("en-US")} /{" "}
                    {detail.cacheCreationTokens.toLocaleString("en-US")}
                  </DetailRow>
                  <DetailRow k="费用">
                    ¥{credits(detail.creditsMicro)}
                  </DetailRow>
                  <DetailRow k="延迟">
                    {detail.latencyMs === null ? "—" : `${detail.latencyMs} ms`}
                  </DetailRow>
                  {isRoot ? (
                    <DetailRow k="客户端 IP">
                      {detail.clientIp ?? "—"}
                    </DetailRow>
                  ) : null}
                </>
              );
            })()}
            <div className="dialog-actions">
              <button
                className="btn btn-ghost"
                type="button"
                onClick={() => setDetail(null)}
              >
                关闭
              </button>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  );
}
