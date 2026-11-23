import {
  MICRO_CREDITS_PER_CREDIT,
  formatCreditsDisplay,
} from "@shucode-platform/contracts/credits";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { api, apiUrl } from "../api.js";
import { useAuth } from "../auth.js";
import { ChartColumn, ChevronLeft, ChevronRight, Download, RefreshCw } from "lucide-react";
import {
  frozenCostRateText,
  historicalCostDisplay,
  usageModelName,
  type PriceSnapshot,
} from "../usage-costs.js";
import {
  reasoningBudgetText,
  reasoningEffortText,
  type UsageReasoningParameters,
} from "../usage-reasoning.js";
import { UsageModel } from "../components/UsageModel.js";
import { UsageDateRange } from "../components/UsageDateRange.js";
import { resolveUsageChartRange, type UsageChartPreset } from "../usage-range.js";
import { type ChartDatum, type ChartSeries } from "../components/charts.js";
import { PolarBarChart, PolarBars, PolarSegments, PolarTrendChart, RecordHeatmap, polarCompact } from "../components/PolarPlots.js";
import { entityColor } from "../components/polar-colors.js";
import { showToast, toastErr } from "../components/Feedback.js";
import { formatPct } from "../components/Gauge.js";
import { Glyph, PageTitle } from "../components/Icon.js";
import { Select } from "../components/Select.js";
import { Chip, Notice, type ChipTone } from "../components/StatusChip.js";
import {
  formatShanghaiTime,
  shanghaiDayEndUtc,
  shanghaiDayStartUtc,
} from "../time.js";
import "./polar-metrics.css";
import "./polar-ops-layout.css";

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

interface UsageModelOption {
  modelId: string;
  displayName: string;
  enabled: boolean;
}

interface DeviceOption {
  id: string;
  name: string;
  platform: string | null;
}

interface AggregateRow {
  model: string;
  modelDisplayName?: string;
  modelKind?: "text" | "image" | "video" | "media" | "tool";
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

/** 非 root 响应仅包含公开模型投影；采购快照和实际路由只对 root 下发。 */
interface UsageRecordRow extends UsageReasoningParameters {
  id: string;
  requestId: string;
  userId: string;
  teamId: string;
  deviceTokenId: string | null;
  model: string;
  modelDisplayName?: string;
  modelKind?: "text" | "image" | "video" | "media" | "tool";
  upstreamModel?: string;
  priceSnapshot?: PriceSnapshot | null;
  providerId?: string | null;
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

interface ModelTrend {
  model: string;
  label: string;
  buckets: Record<string, number>;
}

interface UsageCostGroup {
  model: string;
  currency: string;
  requests: number;
  costMicro: number;
  chargedMicro: number;
  estimatedUsageRequests: number;
}

interface UsageCostTotal {
  currency: string;
  requests: number;
  costMicro: number;
  chargedMicro: number;
  estimatedUsageRequests: number;
}

/** 服务端按筛选范围汇总的冻结成本快照，不只是当前页。 */
interface UsageCostSummary {
  groups: UsageCostGroup[];
  totals: UsageCostTotal[];
  estimatedRequests: number;
  missingCostRequests: number;
  estimatedUsageRequests: number;
}

/** 与 /console/api/usage 响应对齐：records 为当前页，nextCursor 为下一页游标。 */
interface UsagePageData {
  aggregates: AggregateRow[];
  records: UsageRecordRow[];
  nextCursor: string | null;
  modelOptions?: UsageModelOption[];
  costSummary: UsageCostSummary | null;
}

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
  const { canManagePlatform, canManageTeam, user, teamId } = useAuth();
  const [teamRows, setTeams] = useState<TeamOption[]>([]);
  const [emailRows, setUserEmails] = useState<Map<string, string>>(new Map());
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [filterUserId, setFilterUserId] = useState("");
  const [filterModel, setFilterModel] = useState("");
  const [filterProviderId, setFilterProviderId] = useState("");
  const [filterStatus, setFilterStatus] = useState("");
  const [filterRequestId, setFilterRequestId] = useState("");
  const [providerRows, setProviders] = useState<ProviderOption[]>([]);
  const [catalogModels, setModelOptions] = useState<ModelOption[]>([]);
  const [usageModels, setUsageModelOptions] = useState<UsageModelOption[]>([]);
  const [deviceRows, setDevices] = useState<DeviceOption[]>([]);
  const [detail, setDetail] = useState<UsageRecordRow | null>(null);
  const [aggregateRows, setAggregates] = useState<AggregateRow[]>([]);
  const [recordRows, setRecords] = useState<UsageRecordRow[]>([]);
  const [costTotals, setCostSummary] = useState<UsageCostSummary | null>(null);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [pageSize, setPageSize] = useState(100);
  const [pageIndex, setPageIndex] = useState(0);
  const [pageCursors, setPageCursors] = useState<(string | null)[]>([null]);
  const [responseScope, setResponseScope] = useState("");
  const requestVersion = useRef(0);
  const [metadataScope, setMetadataScope] = useState("");
  const [seriesScope, setSeriesScope] = useState("");
  const [dayRows, setDays] = useState<DayPoint[]>([]);
  const [modelTrendRows, setModelTrends] = useState<ModelTrend[]>([]);
  const [chartSelection, setChartSelection] = useState<{ preset: UsageChartPreset; from: string; to: string }>({ preset: "today", from: "", to: "" });
  const [donutBy, setDonutBy] = useState<"tokens" | "credits">("credits");
  const [error, setError] = useState<string | null>(null);
  const [seriesError, setSeriesError] = useState<string | null>(null);
  const [seriesReloadKey, setSeriesReloadKey] = useState(0);
  const [loading, setLoading] = useState(true);

  // 滚动范围只在选择、应用或刷新时捕获当前时间；普通重渲染不能推进窗口并循环请求。
  const chartRange = useMemo(() => resolveUsageChartRange(chartSelection.preset, chartSelection.from, chartSelection.to),
    [chartSelection.preset, chartSelection.from, chartSelection.to, seriesReloadKey]);
  const chartGranularity = chartRange.granularity;
  const identityScope = JSON.stringify([teamId, user.id, canManagePlatform, canManageTeam]);
  const chartScope = JSON.stringify([identityScope, chartRange.queryFrom, chartRange.queryTo, chartGranularity]);
  const currentMetadata = metadataScope === identityScope;
  const teams = currentMetadata ? teamRows : [];
  const userEmails = currentMetadata ? emailRows : new Map<string, string>();
  const providers = currentMetadata ? providerRows : [];
  const modelOptions = currentMetadata ? catalogModels : [];
  const devices = currentMetadata ? deviceRows : [];
  const days = seriesScope === chartScope ? dayRows : [];
  const modelTrends = seriesScope === chartScope ? modelTrendRows : [];

  // 游标只属于创建它的筛选和身份范围，切换团队/权限时不得复用或展示上一范围的记录。
  const pageScope = JSON.stringify([teamId, user.id, canManagePlatform, canManageTeam, from, to,
    filterUserId, filterModel, filterProviderId, filterStatus, filterRequestId, pageSize]);
  const currentPage = responseScope === pageScope;
  const aggregates = currentPage ? aggregateRows : [];
  const records = currentPage ? recordRows : [];
  const costSummary = currentPage ? costTotals : null;
  // 筛选选项同样属于响应范围；不能在新团队请求尚未返回时显示旧模型目录。
  const usageModelOptions = currentPage ? usageModels : [];

  const providerLabel = (providerId: string | null | undefined, model: string): string => {
    if (!providerId) {
      if (model === "media:reference-analysis") return "平台固定服务（无上游路由）";
      return model.startsWith("media:") ? "未记录供应商" : "网关默认上游（env 未绑定）";
    }
    if (!currentMetadata) return "供应商信息加载中";
    return providers.find((item) => item.id === providerId)?.name ?? `供应商已删除（${providerId}）`;
  };

  useEffect(() => {
    let current = true;
    void (async () => {
      try {
        const [teamData, userData, providerData, modelData, deviceData] =
          await Promise.all([
            api<{ teams: TeamOption[] }>("/teams"),
            canManagePlatform || canManageTeam
              ? api<{ users: UserOption[] }>("/users")
              : Promise.resolve({ users: [user] }),
            canManagePlatform
              ? api<{ providers: ProviderOption[] }>("/providers")
              : Promise.resolve({ providers: [] }),
            canManagePlatform
              ? api<{ models: ModelOption[] }>(
                  "/models?includeDisabled=1",
                )
              : Promise.resolve({ models: [] }),
            api<{ devices: DeviceOption[] }>("/devices"),
          ]);
        if (!current) return;
        setTeams(teamData.teams);
        setUserEmails(
          new Map(userData.users.map((user) => [user.id, user.email])),
        );
        setProviders(providerData.providers);
        setModelOptions(modelData.models);
        setDevices(deviceData.devices);
        setMetadataScope(identityScope);
      } catch (loadError) {
        if (current) setError(loadError instanceof Error ? loadError.message : "加载失败");
      }
    })();
    return () => { current = false; };
  }, [canManagePlatform, canManageTeam, user, teamId, identityScope]);

  // 趋势查询与最近请求分页独立。按区间聚合发现历史／媒体模型后逐模型查询，
  // 不能用模型的全期占比拆分每日积分；每层都必须来自该模型的实际分桶结果。
  useEffect(() => {
    let current = true;
    void (async () => {
      const keys = chartRange.bucketKeys;
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
        // 工具已将自然日或精确滚动窗口转换为服务端约定的 UTC naive 边界。
        from: chartRange.queryFrom,
        to: chartRange.queryTo,
        granularity: chartGranularity,
      });
      setSeriesError(null);
      try {
        const modelQuery = new URLSearchParams({ from: chartRange.queryFrom, to: chartRange.queryTo, limit: "1" });
        const rangeUsage = await api<UsagePageData>(`/usage?${modelQuery.toString()}`);
        if (!current) return;
        const models = rangeUsage.aggregates;
        const [data, modelData] = await Promise.all([
          api<{ series: SeriesRow[] }>(`/usage/series?${query.toString()}`),
          Promise.all(models.map(async (model) => {
            const modelSeriesQuery = new URLSearchParams(query);
            modelSeriesQuery.set("model", model.model);
            const result = await api<{ series: SeriesRow[] }>(`/usage/series?${modelSeriesQuery.toString()}`);
            return { model: model.model, label: usageModelName(model.model, model.modelDisplayName), buckets: Object.fromEntries(result.series.map((row) => [row.bucketStart, row.creditsMicro / MICRO_CREDITS_PER_CREDIT])) };
          })),
        ]);
        if (!current) return;
        for (const row of data.series) {
          const bucket = zeroed.get(row.bucketStart);
          if (!bucket) continue;
          bucket.requests = row.requests;
          bucket.creditsMicro = row.creditsMicro;
          bucket.cacheReadTokens = row.cacheReadTokens;
          bucket.promptTokens =
            row.inputTokens + row.cacheReadTokens + row.cacheCreationTokens;
        }
        setDays([...zeroed.values()]);
        setModelTrends(modelData);
        setSeriesScope(chartScope);
      } catch (seriesLoadError) {
        if (!current) return;
        // 失败不能把空桶当成真实零用量展示。
        setDays([]);
        setModelTrends([]);
        setSeriesScope(chartScope);
        setSeriesError(
          seriesLoadError instanceof Error
            ? seriesLoadError.message
            : "趋势数据加载失败",
        );
      }
    })();
    return () => { current = false; };
  }, [chartRange, chartGranularity, chartScope]);

  const usageParams = useCallback(
    (cursor?: string) => {
      const params = new URLSearchParams();
      // 服务端把无时区时间按 UTC 解析；这里把上海自然日边界换算成 UTC。
      if (from) params.set("from", shanghaiDayStartUtc(from));
      if (to) params.set("to", shanghaiDayEndUtc(to));
      if ((canManagePlatform || canManageTeam) && filterUserId) params.set("userId", filterUserId);
      if (filterModel) params.set("model", filterModel);
      if (canManagePlatform && filterProviderId) params.set("providerId", filterProviderId);
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
      canManagePlatform,
      canManageTeam,
    ],
  );

  const fetchUsage = useCallback(
    (cursor: string | null) => {
      const params = usageParams(cursor ?? undefined);
      params.set("limit", String(pageSize));
      return api<UsagePageData>(
        `/usage${params.size > 0 ? `?${params.toString()}` : ""}`,
      );
    },
    [usageParams, pageSize],
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
    const truncated = response.headers.get("x-usage-export-truncated") === "1";
    const blob = await response.blob();
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `usage-${new Date().toISOString().slice(0, 19).replaceAll(":", "")}.csv`;
    anchor.click();
    URL.revokeObjectURL(url);
    if (truncated) {
      showToast(
        "导出达到 100000 条上限，结果可能被截断；请缩小日期范围或增加筛选条件。",
        { tone: "warn", k: "导出可能不完整" },
      );
    }
  }, [usageParams]);

  const load = useCallback(async (
    cursor: string | null = null,
    targetPage = 0,
    cursors: (string | null)[] = [null],
  ) => {
    const version = ++requestVersion.current;
    setLoading(true);
    setDetail(null);
    try {
      const data = await fetchUsage(cursor);
      if (version !== requestVersion.current) return;
      setAggregates(data.aggregates);
      setUsageModelOptions(data.modelOptions ?? []);
      setRecords(data.records);
      setCostSummary(data.costSummary);
      setNextCursor(data.nextCursor);
      setPageIndex(targetPage);
      setPageCursors(cursors);
      setResponseScope(pageScope);
      setError(null);
    } catch (loadError) {
      if (version === requestVersion.current) setError(loadError instanceof Error ? loadError.message : "加载失败");
    } finally {
      if (version === requestVersion.current) setLoading(false);
    }
  }, [fetchUsage, pageScope]);

  useEffect(() => {
    setPageIndex(0);
    setPageCursors([null]);
    setNextCursor(null);
    setDetail(null);
    setError(null);
    void load();
    // 旧页请求即使晚于新范围返回，也不能覆盖新页或重新开放旧游标。
    return () => { requestVersion.current += 1; };
  }, [load]);

  const previousPage = (): void => {
    if (loading || !currentPage || pageIndex === 0) return;
    void load(pageCursors[pageIndex - 1], pageIndex - 1, pageCursors);
  };
  const nextPage = (): void => {
    if (loading || !currentPage || !nextCursor) return;
    void load(nextCursor, pageIndex + 1, [...pageCursors.slice(0, pageIndex + 1), nextCursor]);
  };

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
  // 加载失败且没有任何已加载数据时，不能用 0 冒充真实用量。
  const dataUnavailable = error !== null && aggregates.length === 0;
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

  const primaryCostTotal =
    costSummary?.totals.find((total) => total.currency === "CNY") ??
    costSummary?.totals[0] ??
    null;
  const costDifferenceMicro = primaryCostTotal
    ? primaryCostTotal.chargedMicro - primaryCostTotal.costMicro
    : 0;

  const presetLabel = chartRange.label;
  const chartRangeLabel = chartSelection.preset === "last24h"
    ? `${formatShanghaiTime(`${chartRange.queryFrom}Z`).slice(0, 16)} ~ ${formatShanghaiTime(`${chartRange.queryTo}Z`).slice(0, 16)}`
    : `${chartRange.from} ~ ${chartRange.to}`;

  // 模型分布环形图：Token 口径 = 总 prompt（入 + 缓存读 + 缓存写），积分口径 = credit。
  const donutSegments = useMemo(() => {
    const valueOf = (row: AggregateRow) =>
      donutBy === "tokens"
        ? row.inputTokens + row.cacheReadTokens + row.cacheCreationTokens
        : row.creditsMicro;
    const rows = aggregates
      .map((row) => ({ model: row.model, label: usageModelName(row.model, row.modelDisplayName), value: valueOf(row) }))
      .filter((row) => row.value > 0)
      .sort((a, b) => b.value - a.value);
    const total = rows.reduce((sum, row) => sum + row.value, 0);
    const top = rows.slice(0, 6);
    const rest = rows.slice(6);
    if (rest.length > 0) {
      top.push({
        model: `其他 ${rest.length} 模型`,
        label: `其他 ${rest.length} 模型`,
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
    chartGranularity === "day" ? date.slice(5, 10)
      : chartRange.from !== chartRange.to ? `${date.slice(5, 10)} ${date.slice(11, 16)}` : date.slice(11, 16);

  const costTrend: ChartDatum[] = days.map((day) => ({
    label: bucketLabel(day.date),
    ...Object.fromEntries(modelTrends.map((model) => [`model:${model.model}`, model.buckets[day.date] ?? 0])),
  }));
  const costSeries: ChartSeries[] = modelTrends.map((model) => ({
    key: `model:${model.model}`, label: model.label, color: entityColor(model.model), kind: "area",
    format: (value) => value.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 }),
    axisFormat: polarCompact,
  }));

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
      key: "hitRate",
      label: "缓存命中率",
      color: "var(--accent)",
      kind: "area",
      format: (value) => `${value.toFixed(1)}%`,
      axisFormat: (value) => `${Math.round(value)}%`,
    },
  ];
  const cacheValues = cacheTrend.map((row) => row.hitRate).filter((value): value is number => typeof value === "number");
  const cacheMinimum = Math.min(30, Math.floor(Math.min(100, ...cacheValues) / 10) * 10);
  const cacheMaximum = Math.max(70, Math.ceil(Math.max(0, ...cacheValues) / 10) * 10);

  const recordTeamRank = useMemo(() => {
    const result = new Map<string, number>();
    for (const record of records) result.set(record.teamId, (result.get(record.teamId) ?? 0) + record.creditsMicro);
    return [...result].map(([id, micro]) => ({ key: id, label: teams.find((team) => team.id === id)?.name ?? id.slice(0, 8), value: micro / MICRO_CREDITS_PER_CREDIT })).sort((a, b) => b.value - a.value).slice(0, 6);
  }, [records, teams]);
  const reasoningRank = useMemo(() => {
    const result = new Map<string, number>();
    for (const record of records) {
      const effort = reasoningEffortText(record);
      result.set(effort, (result.get(effort) ?? 0) + 1);
    }
    const ordered = ["none", "minimal", "low", "medium", "high", "xhigh", "max", "未记录", "不适用"];
    const labels = [...new Set(["low", "medium", "high", "xhigh", "max", ...result.keys()])];
    const ramp: Record<string, string> = { none: "#1C2E3A", minimal: "#234052", low: "#2A4A5C", medium: "#3E7590", high: "#58A2C2", xhigh: "#73C8E6", max: "#A9E9FF" };
    return labels.sort((a, b) => (ordered.indexOf(a) < 0 ? ordered.length : ordered.indexOf(a)) - (ordered.indexOf(b) < 0 ? ordered.length : ordered.indexOf(b))).map((label) => ({ label, value: result.get(label) ?? 0, color: ramp[label] ?? "var(--muted)" }));
  }, [records]);

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
    <div className="polar-metrics polar-usage">
      <header className="page-head">
        <div>
          <p className="eyebrow">Usage analytics</p>
          <PageTitle icon={ChartColumn}>
            {canManagePlatform || canManageTeam ? "用量与消费" : "我的用量"}
          </PageTitle>
          <p className="page-sub">按上海墙钟分桶 · 单个自然日与近 24 小时按小时，其余按天</p>
        </div>
        <p className="readout m0">
          {dataUnavailable
            ? "用量数据加载失败"
            : `共 ${totalRequests.toLocaleString("en-US")} 次请求 · ${credits(totalCredits)} credit`}
        </p>
      </header>

      {error ? (
        <Notice tone="stop" k="加载失败">
          {error}{" "}
          <button
            className="btn btn-sm"
            type="button"
            disabled={loading}
            onClick={() => void load()}
          >
            <Glyph icon={RefreshCw} size={13} strokeWidth={2} />
            重试
          </button>
        </Notice>
      ) : null}
      {seriesError ? (
        <Notice tone="stop" k="趋势数据加载失败">
          {seriesError}{" "}
          <button
            className="btn btn-sm"
            type="button"
            onClick={() => setSeriesReloadKey((value) => value + 1)}
          >
            <Glyph icon={RefreshCw} size={13} strokeWidth={2} />
            重试
          </button>
        </Notice>
      ) : null}
      {nearQuotaTeam ? (
        <Notice
          k={nearQuotaTeam.team.name}
          action={
            canManagePlatform
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
            {dataUnavailable ? "—" : totalRequests.toLocaleString("en-US")}
            <span className="unit">次</span>
          </p>
          <p className="stat-sub">
            {dataUnavailable
              ? "加载失败，请重试"
              : `所选范围内${from || to ? "（含日期筛选）" : ""}`}
          </p>
        </div>
        <div className="stat-card">
          <p className="stat-label">缓存命中率</p>
          <p className="stat-value">
            {dataUnavailable
              ? "—"
              : cacheRateText(aggregates.reduce((sum, row) => sum + row.cacheReadTokens, 0), totalInputTokens + totalCacheTokens)}
          </p>
          <p
            className="stat-sub"
            title="缓存命中按缓存读单价计费，通常远低于输入价"
          >
            {dataUnavailable
              ? "加载失败，请重试"
              : `总 ${compactToken(totalInputTokens + totalOutputTokens + totalCacheTokens)} tok · 缓存 ${compactToken(totalCacheTokens)} tok`}
          </p>
        </div>
        <div className="stat-card">
          <p className="stat-label">总积分消耗</p>
          <p className="stat-value stat-amber">
            {dataUnavailable ? "—" : credits(totalCredits)}
            <span className="unit">credit</span>
          </p>
          <p className="stat-sub">
            {dataUnavailable ? "加载失败，请重试" : "网关计量 · 团队账单口径"}
          </p>
        </div>
        <div className="stat-card">
          <p className="stat-label">每请求均价</p>
          <p className="stat-value">
            {dataUnavailable || totalRequests === 0
              ? "—"
              : formatCreditsDisplay(totalCredits / MICRO_CREDITS_PER_CREDIT / totalRequests)}
            <span className="unit">credit</span>
          </p>
          <p className="stat-sub">
            {dataUnavailable
              ? "加载失败，请重试"
              : avgLatencyS === null
                ? "范围内暂无带延迟记录"
                : `平均耗时 ${avgLatencyS.toFixed(2)} s`}
          </p>
        </div>
      </section>

      <details className="polar-usage-billing-note"><summary>计费口径与当前售价对照</summary>
      <p className="readout" style={{ margin: "0 0 18px" }}>
        计费口径：总积分为请求发生时实扣，历史记录不随调价回写；缓存命中按缓存读单价计费，通常远低于输入价。
        当前售价对照（不代表历史实扣）：
        {currentPriceEstimate.offPeak.toLocaleString("en-US", {
          maximumFractionDigits: 4,
        })}{" "}
        credit（全空闲）~
        {currentPriceEstimate.peak.toLocaleString("en-US", {
          maximumFractionDigits: 4,
        })}{" "}
        credit（全高峰）
        {currentPriceEstimate.missingModels > 0
          ? `；${currentPriceEstimate.missingModels} 个模型未配置售价，未计入估算`
          : ""}
        。
      </p>

      </details>

      <section className="pane polar-span-12 polar-usage-filters">
        <div className="pane-b">
          <div className="form-row" style={{ marginBottom: 12 }}>
            <span className="eyebrow" style={{ margin: 0, fontSize: 10 }}>趋势范围</span>
            <div className="polar-segmented" aria-label="趋势日期预设">{([{ value: "today", label: "今天" }, { value: "last24h", label: "近 24 小时" }, { value: "7d", label: "近 7 天" }, { value: "30d", label: "近 30 天" }] as const).map((preset) => <button key={preset.value} type="button" aria-pressed={chartSelection.preset === preset.value} onClick={() => { setChartSelection({ preset: preset.value, from: "", to: "" }); setSeriesReloadKey((value) => value + 1); }}>{preset.label}</button>)}</div>
            <span className="readout" style={{ fontSize: 10 }}>{chartRangeLabel}</span>
          </div>
          <div className="form-row">
            {canManagePlatform || canManageTeam ? (
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
                ...(currentPage ? (usageModelOptions.length ? usageModelOptions : canManagePlatform ? modelOptions : []) : []).map((model) => ({
                  value: model.modelId,
                  label: model.enabled
                    ? model.displayName
                    : `${model.displayName}（停用）`,
                })),
              ]}
            />
            {canManagePlatform ? (
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
              <Glyph icon={RefreshCw} size={14} strokeWidth={2} />
              刷新
            </button>
            <button
              className="btn btn-ghost"
              type="button"
              onClick={() => void exportUsage()}
            >
              <Glyph icon={Download} size={14} strokeWidth={2} />
              导出 CSV
            </button>
          </div>
        </div>
      </section>

      <section className="pane d1 polar-span-8 polar-usage-trend">
        <div className="pane-h">
          <h2>积分趋势 · {presetLabel}</h2>
          <div className="form-row" style={{ gap: 8 }}>
            <UsageDateRange
              preset={chartSelection.preset}
              from={chartRange.from}
              to={chartRange.to}
              label={presetLabel}
              onApply={(preset, from, to) => {
                try {
                  resolveUsageChartRange(preset, from, to);
                  setChartSelection({ preset, from, to });
                  setSeriesReloadKey((value) => value + 1);
                } catch (cause) {
                  setSeriesError(cause instanceof Error ? cause.message : "趋势日期范围不合法");
                }
              }}
            />
            <Chip tone="info" title="单个自然日和近24小时按小时；其他范围按天自动分桶">
              自动 · {chartGranularity === "hour" ? "按小时" : "按天"}
            </Chip>
            <span className="readout">{chartRangeLabel} · 单位 credit</span>
          </div>
        </div>
        <div className="pane-b">
          <PolarTrendChart
            data={costTrend}
            series={costSeries}
            height={260}
            stacked
            ariaLabel="按模型堆叠的积分趋势"
            emptyText={
              seriesError ? "趋势数据加载失败，请重试。" : "所选范围暂无积分数据。"
            }
          />
        </div>
      </section>

      <section className="pane d1 polar-span-4 polar-usage-cache">
        <div className="pane-h">
          <h2>缓存率趋势 · {presetLabel}</h2>
          <span className="readout">
            {seriesError
              ? "趋势加载失败"
              : chartCachePrompt > 0
                ? `命中 ${compactToken(chartCacheRead)} tok · ${formatPct(chartCacheRate ?? 0)}`
                : "范围内暂无 prompt"}
          </span>
        </div>
        <div className="pane-b">
          <PolarTrendChart
            data={cacheTrend}
            series={cacheSeries}
            height={260}
            yMin={cacheMinimum}
            yMax={cacheMaximum}
            reference={chartCacheRate === null ? undefined : { value: chartCacheRate * 100, label: `均值 ${formatPct(chartCacheRate)}` }}
            endLabel="value"
            emptyText={
              seriesError
                ? "趋势数据加载失败，请重试。"
                : "所选范围暂无 prompt 数据。"
            }
          />
        </div>
      </section>

      <section className="pane d1 polar-span-7 polar-usage-models">
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
                <th scope="col" className="num">缓存读</th>
                <th scope="col">Token 构成</th>
                <th scope="col" className="num">
                  缓存命中率
                </th>
                <th scope="col" className="num">
                  积分（credit）
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
                    <td><i className="polar-model-dot" style={{ background: entityColor(row.model) }} />{usageModelName(row.model, row.modelDisplayName)}</td>
                    <td className="num">
                      {row.requests.toLocaleString("en-US")}
                    </td>
                    <td className="num">
                      {row.inputTokens.toLocaleString("en-US")}
                    </td>
                    <td className="num">
                      {row.outputTokens.toLocaleString("en-US")}
                    </td>
                    <td className="num">{row.cacheReadTokens.toLocaleString("en-US")}</td>
                    <td>{row.inputTokens + row.cacheReadTokens + row.outputTokens > 0 ? <PolarSegments showLegend={false} height={6} rows={[{ label: "输入", value: row.inputTokens, color: "#3D6E8F" }, { label: "缓存读", value: row.cacheReadTokens, color: "#8FE3FF" }, { label: "输出", value: row.outputTokens, color: "#DCEAF4" }]} format={(value) => `${value.toLocaleString("en-US")} tok`} /> : "—"}</td>
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
                  <td colSpan={8} className="readout">
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
                  <td className="num">{aggregates.reduce((sum, row) => sum + row.cacheReadTokens, 0).toLocaleString("en-US")}</td>
                  <td />
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
        <div className="polar-token-key"><span><i style={{ background: "#3D6E8F" }} />输入</span><span><i style={{ background: "#8FE3FF" }} />缓存读</span><span><i style={{ background: "#DCEAF4" }} />输出</span></div>
      </section>

      <section className="pane d1 polar-span-5 polar-usage-distribution">
        <div className="pane-h">
          <h2>{donutBy === "tokens" ? "模型 Token 构成" : "模型消费构成"}</h2>
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
              按积分
            </button>
          </div>
        </div>
        <div className="pane-b">
          {donutSegments.total > 0 ? (
            <>
              <PolarSegments rows={donutSegments.segments.map((segment) => ({ key: segment.model, label: segment.label ?? usageModelName(segment.model), value: segment.value, color: entityColor(segment.model) }))} format={(value) => donutBy === "tokens" ? `${compactToken(value)} tok` : `${credits(value)} credit`} />
            </>
          ) : (
            <p className="readout" style={{ margin: 0 }}>
              所选范围内暂无分布数据。
            </p>
          )}
          {canManagePlatform ? <><h3 className="polar-subheading">团队排行 · 本页请求</h3><PolarBars rows={recordTeamRank} format={(value) => `${formatCreditsDisplay(value)} credit`} emptyText="本页暂无团队消费。" /><p className="readout" style={{ margin: "12px 0 0", fontSize: 10 }}>按当前页 {records.length} 条请求汇总，不代表完整区间排名。</p></> : null}
        </div>
      </section>

      <section className="pane d1 polar-span-7">
        <div className="pane-h"><div><h2>用量热力 · 星期 × 小时</h2><p className="readout" style={{ margin: "6px 0 0", fontSize: 11 }}>本页请求数 · 上海墙钟 · 颜色越亮越忙</p></div></div>
        <div className="pane-b"><RecordHeatmap records={records} /></div>
      </section>
      <section className="pane d1 polar-span-5">
        <div className="pane-h"><div><h2>思考强度分布</h2><p className="readout" style={{ margin: "6px 0 0", fontSize: 11 }}>本页请求记录 · 缺少参数显示为未记录</p></div></div>
        <div className="pane-b"><PolarBarChart
          data={records.length ? reasoningRank.map((row) => ({ label: row.label, share: row.value / records.length * 100 })) : []}
          series={[{ key: "share", label: "请求占比", kind: "bar", color: "var(--accent)", colorAt: (index) => reasoningRank[index].color, format: (value) => `${value.toFixed(1)}%`, axisFormat: (value) => `${Math.round(value)}%` }]}
          height={230} ratio={.6} ticks={5} yMax={Math.max(50, Math.ceil(Math.max(0, ...reasoningRank.map((row) => records.length ? row.value / records.length * 100 : 0)) / 10) * 10)}
          tipExtra={(index) => [{ label: "请求数", value: `${reasoningRank[index].value.toLocaleString("en-US")} 次` }]}
          emptyText="本页暂无思考参数记录。"
        /></div>
      </section>

      {canManagePlatform ? (
        <section className="pane d1 polar-span-12 polar-usage-costs" style={{ order: 10 }}>
          <div className="pane-h">
            <h2>区间历史成本估算</h2>
            <span className="readout">
              冻结价格快照 · 服务端汇总
              {costSummary ? ` · ${costSummary.estimatedRequests} 条已计入` : ""}
            </span>
          </div>
          <div className="pane-b">
            {!costSummary ? (
              <p className="readout m0">成本汇总加载中…</p>
            ) : (
              <>
                <div className="stat-grid" style={{ marginBottom: 14 }}>
                  <div className="stat-card">
                    <p className="stat-label">区间总成本</p>
                    <p className="stat-value">
                      {primaryCostTotal
                        ? money(
                            primaryCostTotal.costMicro / 1_000_000,
                            primaryCostTotal.currency,
                          )
                        : "—"}
                    </p>
                    <p className="stat-sub">
                      {primaryCostTotal
                        ? `${primaryCostTotal.requests} 条已知成本请求`
                        : "筛选范围内暂无成本快照"}
                      {costSummary.totals.length > 1
                        ? ` · 另有 ${costSummary.totals.length - 1} 种币种见下表`
                        : ""}
                    </p>
                  </div>
                  <div className="stat-card">
                    <p className="stat-label">对应实扣</p>
                    <p className="stat-value">
                      {primaryCostTotal
                        ? `${credits(primaryCostTotal.chargedMicro)} credit`
                        : "—"}
                    </p>
                    <p className="stat-sub">仅统计已匹配成本快照的请求</p>
                  </div>
                  <div className="stat-card">
                    <p className="stat-label">估算差额</p>
                    <p className="stat-value">
                      {primaryCostTotal?.currency === "CNY"
                        ? money(costDifferenceMicro / 1_000_000, "CNY")
                        : "—"}
                    </p>
                    <p className="stat-sub">实扣 − 成本 · 仅 CNY</p>
                  </div>
                </div>
                <p className="readout m0">
                  覆盖当前筛选范围内的全部请求，不只是已加载页。
                  {costSummary.estimatedRequests} 条有成本快照，
                  {costSummary.missingCostRequests} 条旧记录或未知成本未计入；其中{" "}
                  {costSummary.estimatedUsageRequests} 条采用估算 Token 用量。各币种独立统计；
                  这是参考估算，不是已结算毛利或供应商账单。
                </p>
              </>
            )}
          </div>
          {costSummary ? (
            <div className="table-scroll">
              <table className="table">
                <thead>
                  <tr>
                    <th>模型</th>
                    <th>币种</th>
                    <th>已知成本请求</th>
                    <th>其中估算用量</th>
                    <th>历史成本估算</th>
                    <th>对应实扣（credit）</th>
                    <th>CNY 估算差额</th>
                  </tr>
                </thead>
                <tbody>
                  {costSummary.groups.map((row) => (
                    <tr key={`${row.model}:${row.currency}`}>
                      <td>{row.model}</td>
                      <td>{row.currency}</td>
                      <td className="num">{row.requests}</td>
                      <td className="num">{row.estimatedUsageRequests}</td>
                      <td className="num">
                        {money(row.costMicro / 1_000_000, row.currency)}
                      </td>
                      <td className="num">{credits(row.chargedMicro)}</td>
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
                  {costSummary.groups.length === 0 ? (
                    <tr>
                      <td colSpan={7} className="readout">
                        筛选范围内暂无可估算的历史成本快照。
                      </td>
                    </tr>
                  ) : null}
                </tbody>
                <tfoot>
                  {costSummary.totals.map((total) => (
                    <tr key={total.currency}>
                      <td>合计</td>
                      <td>{total.currency}</td>
                      <td className="num">{total.requests}</td>
                      <td className="num">{total.estimatedUsageRequests}</td>
                      <td className="num">
                        {money(total.costMicro / 1_000_000, total.currency)}
                      </td>
                      <td className="num">{credits(total.chargedMicro)}</td>
                      <td className="num">
                        {total.currency === "CNY"
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
          ) : null}
        </section>
      ) : null}

      <section className="pane d2 polar-span-12 polar-usage-detail">
        <div className="pane-h">
          <h2>最近请求</h2>
          <span className="readout">每页 {pageSize} 条 · 筛选联动</span>
        </div>
        <div className="pane-b">
          <p className="readout m0">
            原片分析期间，模型请求的用户实扣为 0；成功后另有一笔固定分析费，已包含本次分析的模型调用。
            {canManagePlatform ? "供应商成本按请求时的采购价快照估算，与用户实扣分别展示，不代表供应商最终账单。" : null}
          </p>
        </div>
        <div className="pane-b flush table-scroll vscroll">
          <table className="table" style={{ whiteSpace: "nowrap", minWidth: canManagePlatform ? 1140 : 900 }}>
            <thead>
              <tr>
                <th scope="col">时间</th>
                <th scope="col">团队</th>
                <th scope="col">账号</th>
                <th scope="col">模型</th>
                <th scope="col">思考</th>
                <th scope="col" className="num">输入</th>
                <th scope="col" className="num">缓存读</th>
                <th scope="col" className="num">输出</th>
                <th scope="col" className="num">
                  用户实扣（credit）
                </th>
                {canManagePlatform ? <th scope="col" className="num">供应商成本（估算）</th> : null}
                <th scope="col" className="num">延迟</th>
                <th scope="col">状态</th>
                <th scope="col" aria-label="操作" />
              </tr>
            </thead>
            <tbody>
              {records.map((record) => {
                const status = statusChip(record.status);
                const cost = canManagePlatform ? historicalCostDisplay(record.priceSnapshot) : null;
                return (
                  <tr key={record.id}>
                    <td className="num">
                      {formatShanghaiTime(record.createdAt).slice(5, 16)}
                    </td>
                    <td>{teams.find((team) => team.id === record.teamId)?.name ?? record.teamId.slice(0, 8)}</td>
                    <td>
                      {userEmails.get(record.userId) ??
                        record.userId.slice(0, 8)}
                      {canManagePlatform ? <div className="readout" style={{ fontSize: 10 }}>{record.clientIp ?? "—"}</div> : null}
                    </td>
                    <td>
                      <i className="polar-model-dot" style={{ background: entityColor(record.model) }} />
                      {record.model === "media:reference-analysis" ? (
                        <span>{usageModelName(record.model, record.modelDisplayName)}</span>
                      ) : <UsageModel record={record} />}
                      {canManagePlatform && record.model !== "media:reference-analysis" ? (
                        <div className="readout" title={`平台模型：${record.model}`}>
                          {providerLabel(record.providerId, record.model)} · {record.upstreamModel || record.model}
                        </div>
                      ) : null}
                    </td>
                    <td className="num" title={`思考预算：${reasoningBudgetText(record)}`}>{reasoningEffortText(record)}</td>
                    <td className="num">{record.inputTokens.toLocaleString("en-US")}</td>
                    <td className="num">{record.cacheReadTokens.toLocaleString("en-US")}</td>
                    <td className="num">{record.outputTokens.toLocaleString("en-US")}</td>
                    <td className="num">{credits(record.creditsMicro)}</td>
                    {cost ? (
                      <td className="num" title={cost.reason}>
                        {cost.text}
                      </td>
                    ) : null}
                    <td className="num">{record.latencyMs === null || record.latencyMs === undefined ? "—" : `${(record.latencyMs / 1000).toFixed(2)}s`}</td>
                    <td><Chip tone={status.tone}>{status.label}</Chip></td>
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
                  <td colSpan={canManagePlatform ? 13 : 12} className="readout">
                    {loading ? "加载中…" : "暂无请求记录。"}
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
        <nav className="pane-b form-row" aria-label="最近请求分页" style={{ justifyContent: "space-between" }}>
          <div className="form-row">
            <Select
              ariaLabel="最近请求每页条数"
              value={String(pageSize)}
              onChange={(value) => setPageSize(Number(value))}
              options={[20, 50, 100].map((size) => ({ value: String(size), label: `每页 ${size} 条` }))}
              style={{ width: 130 }}
            />
            <span className="readout" aria-live="polite">第 {currentPage ? pageIndex + 1 : 1} 页 · 本页 {records.length} 条{loading ? " · 加载中…" : ""}</span>
          </div>
          <div className="form-row">
            <button className="btn btn-ghost" type="button" aria-label="最近请求上一页" disabled={loading || !currentPage || pageIndex === 0} onClick={previousPage}><Glyph icon={ChevronLeft} size={14} strokeWidth={2} />上一页</button>
            <button className="btn btn-ghost" type="button" aria-label="最近请求下一页" disabled={loading || !currentPage || !nextCursor} onClick={nextPage}>下一页<Glyph icon={ChevronRight} size={14} strokeWidth={2} /></button>
          </div>
        </nav>
      </section>

      {detail && currentPage ? (
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
            style={{ maxHeight: "calc(100dvh - 32px)", overflowY: "auto" }}
            onClick={(event) => event.stopPropagation()}
          >
            <p className="readout dialog-k">计量 / 请求详情</p>
            <h3 className="dialog-title">{usageModelName(detail.model, detail.modelDisplayName)}</h3>
            {(() => {
              const status = statusChip(detail.status);
              const cost = canManagePlatform ? historicalCostDisplay(detail.priceSnapshot) : null;
              const provider = providerLabel(detail.providerId, detail.model);
              const requestPriced = detail.modelKind === "tool" || detail.priceSnapshot?.sale.unit === "request";
              const mediaUnit = detail.priceSnapshot?.sale.unit;
              const mediaPriced = mediaUnit === "image" || mediaUnit === "second";
              const mediaRateLabel = mediaUnit === "image" ? "每张" : "每秒输出";
              const mediaQuantity = detail.priceSnapshot?.sale.quantity;
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
                  <DetailRow k="思考强度">
                    {reasoningEffortText(detail)}
                  </DetailRow>
                  <DetailRow k="思考预算">
                    {reasoningBudgetText(detail)}
                  </DetailRow>
                  {canManagePlatform ? (
                    <>
                      <DetailRow k="模型映射">
                        {detail.model} → {detail.upstreamModel || "—"}
                      </DetailRow>
                      <DetailRow k="供应商">{provider}</DetailRow>
                    </>
                  ) : null}
                  {requestPriced ? (
                    <DetailRow k="计费单位">每次成功请求；用户失败及空结果不扣费</DetailRow>
                  ) : mediaPriced ? (
                    <>
                      <DetailRow k="计费单位">{mediaUnit === "image" ? "按张" : "按输出秒数"}</DetailRow>
                      <DetailRow k="计费数量">
                        {typeof mediaQuantity === "number" && Number.isSafeInteger(mediaQuantity) && mediaQuantity >= 0
                          ? `${mediaQuantity.toLocaleString("en-US")} ${mediaUnit === "image" ? "张" : "秒"}`
                          : "未记录"}
                      </DetailRow>
                    </>
                  ) : <DetailRow k="Token（入 / 出 / 缓存读 / 缓存写）">
                    {detail.inputTokens.toLocaleString("en-US")} /{" "}
                    {detail.outputTokens.toLocaleString("en-US")} /{" "}
                    {detail.cacheReadTokens.toLocaleString("en-US")} /{" "}
                    {detail.cacheCreationTokens.toLocaleString("en-US")}
                  </DetailRow>}
                  <DetailRow k="用户实扣（credit）">
                    {credits(detail.creditsMicro)} credit
                  </DetailRow>
                  {cost ? (
                    <>
                      <DetailRow k="供应商成本（估算）">
                        {cost.text}
                      </DetailRow>
                      <DetailRow k="成本说明">
                        {cost.reason}
                      </DetailRow>
                      <DetailRow k="成本价格版本">
                        {detail.priceSnapshot?.cost.pricingVersion || "未记录"}
                      </DetailRow>
                      <DetailRow k="成本匹配模型">
                        {detail.priceSnapshot?.cost.modelId || "未记录"}
                      </DetailRow>
                      <DetailRow k="成本快照时间（上海）">
                        {detail.priceSnapshot?.capturedAt
                          ? formatShanghaiTime(detail.priceSnapshot.capturedAt)
                          : "未记录"}
                      </DetailRow>
                      {requestPriced ? (
                        <>
                          <DetailRow k="售价 · 每次成功请求">
                            {frozenCostRateText(detail.priceSnapshot?.sale, "perRequest")}
                          </DetailRow>
                          <DetailRow k="采购价 · 每次成功请求">
                            {frozenCostRateText(detail.priceSnapshot?.cost.rates, "perRequest")}
                          </DetailRow>
                          <p className="readout">按请求次数计费，返回条数、正文长度和 Token 数不影响工具单次价格。</p>
                        </>
                      ) : mediaPriced ? (
                        <>
                          <DetailRow k={`售价 · ${mediaRateLabel}`}>
                            {frozenCostRateText(detail.priceSnapshot?.sale, "perUnit")}
                          </DetailRow>
                          <DetailRow k={`采购价 · ${mediaRateLabel}`}>
                            {frozenCostRateText(detail.priceSnapshot?.cost.rates, "perUnit")}
                          </DetailRow>
                          <p className="readout">
                            <a href="#/media-models">查看媒体模型价格</a>；修改采购价仅影响未来任务，不改变此历史快照。
                          </p>
                        </>
                      ) : <>
                      <DetailRow k="采购价 · 输入">
                        {frozenCostRateText(detail.priceSnapshot?.cost.rates, "inputPerMtok")}
                      </DetailRow>
                      <DetailRow k="采购价 · 输出">
                        {frozenCostRateText(detail.priceSnapshot?.cost.rates, "outputPerMtok")}
                      </DetailRow>
                      <DetailRow k="采购价 · 缓存读">
                        {frozenCostRateText(detail.priceSnapshot?.cost.rates, "cacheReadPerMtok")}
                      </DetailRow>
                      <DetailRow k="采购价 · 缓存写">
                        {frozenCostRateText(detail.priceSnapshot?.cost.rates, "cacheWritePerMtok")}
                      </DetailRow>
                      <DetailRow k="冻结采购价倍率">
                        {detail.priceSnapshot?.cost.rates?.multiplier || "未记录"}
                      </DetailRow>
                      <p className="readout">
                        <a href="#/pricing">查看模型定价库</a>；修改采购价仅影响未来请求，不改变此历史快照。
                      </p>
                      </>}
                    </>
                  ) : null}
                  <DetailRow k="延迟">
                    {detail.latencyMs === null ? "—" : `${detail.latencyMs} ms`}
                  </DetailRow>
                  {canManagePlatform ? (
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
