import {
  MICRO_CREDITS_PER_CREDIT,
  formatCreditsDisplay,
} from "@shucode-platform/contracts/credits";
import { useCallback, useEffect, useMemo, useState, type CSSProperties, type ReactNode } from "react";
import { api } from "../api.js";
import { formatPct } from "../components/Gauge.js";
import { Select } from "../components/Select.js";
import { Chip, Notice, type ChipTone } from "../components/StatusChip.js";
import { formatShanghaiTime, shanghaiDateOf, shanghaiMonthRange } from "../time.js";

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

type ChartPreset = "today" | "yesterday" | "7d" | "14d" | "30d" | "month" | "lastMonth";
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
function cacheRate(cacheReadTokens: number, promptTokens: number): number | null {
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
  const span = preset === "7d" ? 7 : preset === "14d" ? 14 : preset === "30d" ? 30 : 0;
  if (span > 0) {
    return { from: shanghaiDateOf(new Date(now.getTime() - (span - 1) * DAY)), to: today };
  }
  return preset === "month" ? shanghaiMonthRange(now, 0) : shanghaiMonthRange(now, -1);
}

/** 补齐时间轴的空桶：key 与后端 bucketStart 对齐（上海墙钟）。hours 全天补零，未来小时显示 0 柱。 */
function buildBucketKeys(from: string, to: string, granularity: ChartGranularity): string[] {
  const keys: string[] = [];
  for (let t = Date.parse(`${from}T00:00:00Z`); t <= Date.parse(`${to}T00:00:00Z`); t += DAY) {
    const day = new Date(t).toISOString().slice(0, 10);
    if (granularity === "day") {
      keys.push(`${day}T00:00:00`);
    } else {
      for (let h = 0; h < 24; h += 1) keys.push(`${day}T${String(h).padStart(2, "0")}:00:00`);
    }
  }
  return keys;
}

/** 详情弹窗里的单行键值：readout 作键，右对齐作值。 */
function DetailRow({ k, children }: { k: string; children: ReactNode }) {
  return (
    <div
      className="form-row"
      style={{ justifyContent: "space-between", alignItems: "baseline", gap: 16, marginTop: 8 }}
    >
      <span className="readout" style={{ flexShrink: 0 }}>
        {k}
      </span>
      <span style={{ textAlign: "right", wordBreak: "break-all" }}>{children}</span>
    </div>
  );
}

export function UsagePage() {
  const [teams, setTeams] = useState<TeamOption[]>([]);
  const [userEmails, setUserEmails] = useState<Map<string, string>>(new Map());
  const [teamId, setTeamId] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [filterUserId, setFilterUserId] = useState("");
  const [filterModel, setFilterModel] = useState("");
  const [filterProviderId, setFilterProviderId] = useState("");
  const [filterStatus, setFilterStatus] = useState("");
  const [providers, setProviders] = useState<ProviderOption[]>([]);
  const [modelOptions, setModelOptions] = useState<ModelOption[]>([]);
  const [devices, setDevices] = useState<DeviceOption[]>([]);
  const [detail, setDetail] = useState<UsageRecordRow | null>(null);
  const [aggregates, setAggregates] = useState<AggregateRow[]>([]);
  const [records, setRecords] = useState<UsageRecordRow[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [moreLoading, setMoreLoading] = useState(false);
  const [days, setDays] = useState<DayPoint[]>([]);
  const [chartPreset, setChartPreset] = useState<ChartPreset>("14d");
  const [chartGranularity, setChartGranularity] = useState<ChartGranularity>("day");
  const [donutBy, setDonutBy] = useState<"tokens" | "credits">("tokens");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    void (async () => {
      try {
        const [teamData, userData, providerData, modelData, deviceData] = await Promise.all([
          api<{ teams: TeamOption[] }>("/teams"),
          api<{ users: UserOption[] }>("/users"),
          api<{ providers: ProviderOption[] }>("/providers"),
          api<{ models: ModelOption[] }>("/models?includeDisabled=1"),
          api<{ devices: DeviceOption[] }>("/devices"),
        ]);
        setTeams(teamData.teams);
        setUserEmails(new Map(userData.users.map((user) => [user.id, user.email])));
        setProviders(providerData.providers);
        setModelOptions(modelData.models);
        setDevices(deviceData.devices);
      } catch (loadError) {
        setError(loadError instanceof Error ? loadError.message : "加载失败");
      }
    })();
  }, []);

  // 趋势图为平台口径，与页内筛选无关；一次 series 请求按上海墙钟分桶，
  // 前端补零对齐时间轴（费用图与缓存率图共用这份数据）。
  useEffect(() => {
    void (async () => {
      const { from, to } = presetRange(chartPreset);
      const keys = buildBucketKeys(from, to, chartGranularity);
      const zeroed = new Map(keys.map((key) => [key, { date: key, creditsMicro: 0, cacheReadTokens: 0, promptTokens: 0 }]));
      const query = new URLSearchParams({
        from,
        to: `${to}T23:59:59`,
        granularity: chartGranularity,
      });
      const data = await api<{ series: SeriesRow[] }>(`/usage/series?${query.toString()}`).catch(() => null);
      if (data) {
        for (const row of data.series) {
          const bucket = zeroed.get(row.bucketStart);
          if (!bucket) continue;
          bucket.creditsMicro = row.creditsMicro;
          bucket.cacheReadTokens = row.cacheReadTokens;
          bucket.promptTokens = row.inputTokens + row.cacheReadTokens + row.cacheCreationTokens;
        }
      }
      setDays([...zeroed.values()]);
    })();
  }, [chartPreset, chartGranularity]);

  const fetchUsage = useCallback(
    (cursor: string | null) => {
      const params = new URLSearchParams();
      if (teamId) params.set("teamId", teamId);
      if (from) params.set("from", `${from}T00:00:00`);
      if (to) params.set("to", `${to}T23:59:59`);
      if (filterUserId) params.set("userId", filterUserId);
      if (filterModel) params.set("model", filterModel);
      if (filterProviderId) params.set("providerId", filterProviderId);
      if (filterStatus) params.set("status", filterStatus);
      if (cursor) params.set("cursor", cursor);
      return api<UsagePageData>(`/usage${params.size > 0 ? `?${params.toString()}` : ""}`);
    },
    [teamId, from, to, filterUserId, filterModel, filterProviderId, filterStatus],
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
  const totalInputTokens = aggregates.reduce((sum, row) => sum + row.inputTokens, 0);
  const totalOutputTokens = aggregates.reduce((sum, row) => sum + row.outputTokens, 0);
  const totalCacheTokens = aggregates.reduce(
    (sum, row) => sum + row.cacheReadTokens + row.cacheCreationTokens,
    0,
  );
  const totalLatencyMs = aggregates.reduce((sum, row) => sum + row.latencyMsSum, 0);
  // 平均耗时仅在既有延迟记录时给出（部分错误/中断记录无 latency）。
  const avgLatencyS =
    totalLatencyMs > 0 && totalRequests > 0 ? totalLatencyMs / totalRequests / 1000 : null;

  // 趋势图当前范围（上海墙钟），预设/粒度切换后随 days 一起刷新。
  const chartRange = useMemo(() => presetRange(chartPreset), [chartPreset]);
  const presetLabel = CHART_PRESETS.find((item) => item.value === chartPreset)?.label ?? "";

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
      top.push({ model: `其他 ${rest.length} 模型`, value: rest.reduce((sum, row) => sum + row.value, 0) });
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

  const maxDay = days.reduce((max, day) => Math.max(max, day.creditsMicro), 0);
  const yStops = [1, 0.75, 0.5, 0.25, 0].map((k) =>
    (maxDay * k / MICRO_CREDITS_PER_CREDIT).toLocaleString("en-US", { maximumFractionDigits: 2 }),
  );

  // 缓存率图整体口径：分母用同一批趋势数据的 prompt token。
  const chartCacheRead = days.reduce((sum, day) => sum + day.cacheReadTokens, 0);
  const chartCachePrompt = days.reduce((sum, day) => sum + day.promptTokens, 0);
  const chartCacheRate = cacheRate(chartCacheRead, chartCachePrompt);

  // x 轴标签：天 = MM-DD；小时 = HH 点。桶多时隔步显示，防止重叠。
  const labelStep = Math.max(1, Math.ceil(days.length / 16));

  // 详情弹窗的设备显示：名称 + 客户端上报的系统；凭据已删除时回退 ID 前缀。
  const deviceLabel = (deviceTokenId: string | null): string => {
    if (!deviceTokenId) return "—";
    const device = devices.find((item) => item.id === deviceTokenId);
    if (!device) return deviceTokenId.slice(0, 8);
    return device.platform ? `${device.name} · ${device.platform}` : device.name;
  };

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

      <section className="stat-grid" aria-label="所选范围汇总">
        <div className="stat-card">
          <p className="stat-label">总请求数</p>
          <p className="stat-value">
            {totalRequests.toLocaleString("en-US")}
            <span className="unit">次</span>
          </p>
          <p className="stat-sub">所选范围内{from || to ? "（含日期筛选）" : ""}</p>
        </div>
        <div className="stat-card">
          <p className="stat-label">总 Token</p>
          <p className="stat-value">
            {compactToken(totalInputTokens + totalOutputTokens + totalCacheTokens)}
            <span className="unit">tok</span>
          </p>
          <p className="stat-sub">
            输入 {compactToken(totalInputTokens)} · 输出 {compactToken(totalOutputTokens)} · 缓存{" "}
            {compactToken(totalCacheTokens)}
          </p>
        </div>
        <div className="stat-card">
          <p className="stat-label">总消费</p>
          <p className="stat-value stat-amber">
            {credits(totalCredits)}
            <span className="unit">CR</span>
          </p>
          <p className="stat-sub">网关计量 · 团队账单口径</p>
        </div>
        <div className="stat-card">
          <p className="stat-label">平均耗时</p>
          <p className="stat-value">
            {avgLatencyS === null ? "—" : avgLatencyS.toFixed(2)}
            <span className="unit">s</span>
          </p>
          <p className="stat-sub">{avgLatencyS === null ? "范围内暂无带延迟记录" : "成功请求均值"}</p>
        </div>
      </section>

      <section className="pane">
        <div className="pane-b">
          <div className="form-row">
            <Select
              ariaLabel="按团队筛选"
              style={{ width: 150 }}
              value={teamId}
              onChange={setTeamId}
              options={[
                { value: "", label: "全部团队" },
                ...teams.map((team) => ({ value: team.id, label: team.name })),
              ]}
            />
            <Select
              ariaLabel="按用户筛选"
              style={{ width: 180 }}
              value={filterUserId}
              onChange={setFilterUserId}
              options={[
                { value: "", label: "全部用户" },
                ...[...userEmails.entries()].map(([id, email]) => ({ value: id, label: email })),
              ]}
            />
            <Select
              ariaLabel="按模型筛选"
              style={{ width: 200 }}
              value={filterModel}
              onChange={setFilterModel}
              options={[
                { value: "", label: "全部模型" },
                ...modelOptions.map((model) => ({
                  value: model.modelId,
                  label: model.enabled ? model.displayName : `${model.displayName}（停用）`,
                })),
              ]}
            />
            <Select
              ariaLabel="按供应商筛选"
              style={{ width: 170 }}
              value={filterProviderId}
              onChange={setFilterProviderId}
              options={[
                { value: "", label: "全部供应商" },
                ...providers.map((provider) => ({ value: provider.id, label: provider.name })),
              ]}
            />
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
                if (preset !== "today" && preset !== "yesterday" && preset !== "7d") {
                  setChartGranularity("day");
                }
              }}
              options={CHART_PRESETS.map((preset) => ({ value: preset.value, label: preset.label }))}
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
              {chartRange.from} ~ {chartRange.to} · 单位 CR
            </span>
          </div>
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
                {days.map((day, index) => (
                  <span key={day.date} className="chart-x-label">
                    {index % labelStep === 0
                      ? chartGranularity === "day"
                        ? day.date.slice(5, 10)
                        : day.date.slice(11, 16)
                      : ""}
                  </span>
                ))}
              </div>
            </div>
          </div>
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
          <div className="chart">
            <div className="chart-y" aria-hidden="true">
              <span>100%</span>
              <span>75%</span>
              <span>50%</span>
              <span>25%</span>
              <span>0</span>
            </div>
            <div className="chart-main">
              <div className="chart-plot">
                <div className="bars">
                  {days.map((day, index) => {
                    const rate = cacheRate(day.cacheReadTokens, day.promptTokens);
                    return (
                      <div
                        key={day.date}
                        className={index === days.length - 1 ? "bar today" : "bar"}
                        style={{ "--h": `${rate === null ? 0 : rate * 100}%` } as CSSProperties}
                      />
                    );
                  })}
                </div>
              </div>
              <div className="chart-x" aria-hidden="true">
                {days.map((day, index) => (
                  <span key={day.date}>
                    {index % labelStep === 0
                      ? chartGranularity === "day"
                        ? day.date.slice(5, 10)
                        : day.date.slice(11, 16)
                      : ""}
                  </span>
                ))}
              </div>
            </div>
          </div>
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
                className="donut-ring"
                role="img"
                aria-label={`按${donutBy === "tokens" ? "Token" : "实际消费"}的模型分布`}
                style={{
                  background: `conic-gradient(${(() => {
                    let acc = 0;
                    return donutSegments.segments
                      .map((segment, index) => {
                        const from = (acc / donutSegments.total) * 100;
                        acc += segment.value;
                        const to = (acc / donutSegments.total) * 100;
                        return `var(--donut-color-${(index % 6) + 1}) ${from}% ${to}%`;
                      })
                      .join(", ");
                  })()})`,
                }}
              >
                <div className="donut-hole">
                  <span className="donut-total">{compactToken(donutSegments.total)}</span>
                  <span className="donut-total-unit">
                    {donutBy === "tokens" ? "tok" : "CR"}
                  </span>
                </div>
              </div>
              <ul className="donut-legend">
                {donutSegments.segments.map((segment, index) => (
                  <li key={segment.model}>
                    <span className={`donut-dot donut-color-${(index % 6) + 1}`} aria-hidden="true" />
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
                  费用（CR）
                </th>
              </tr>
            </thead>
            <tbody>
              {aggregates.map((row) => {
                const prompt = row.inputTokens + row.cacheReadTokens + row.cacheCreationTokens;
                return (
                  <tr key={row.model}>
                    <td>{row.model}</td>
                    <td className="num">{row.requests.toLocaleString("en-US")}</td>
                    <td className="num">{row.inputTokens.toLocaleString("en-US")}</td>
                    <td className="num">{row.outputTokens.toLocaleString("en-US")}</td>
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
                  <td className="num">{totalRequests.toLocaleString("en-US")}</td>
                  <td className="num">
                    {aggregates.reduce((sum, row) => sum + row.inputTokens, 0).toLocaleString("en-US")}
                  </td>
                  <td className="num">
                    {aggregates.reduce((sum, row) => sum + row.outputTokens, 0).toLocaleString("en-US")}
                  </td>
                  <td className="num">
                    {cacheRateText(
                      aggregates.reduce((sum, row) => sum + row.cacheReadTokens, 0),
                      aggregates.reduce(
                        (sum, row) => sum + row.inputTokens + row.cacheReadTokens + row.cacheCreationTokens,
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
                <th scope="col">真实IP</th>
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
                    <td className="num">{formatShanghaiTime(record.createdAt)}</td>
                    <td>{userEmails.get(record.userId) ?? record.userId.slice(0, 8)}</td>
                    <td>{record.clientIp ?? "—"}</td>
                    <td>{record.model}</td>
                    <td>
                      <Chip tone={status.tone}>{status.label}</Chip>
                    </td>
                    <td className="num">
                      {record.inputTokens.toLocaleString("en-US")} / {record.outputTokens.toLocaleString("en-US")}
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
                  <td colSpan={8} className="readout">
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
                ? providers.find((item) => item.id === detail.providerId)?.name ?? "（该供应商已删除）"
                : "网关默认上游（env 未绑定）";
              return (
                <>
                  <DetailRow k="状态">
                    <Chip tone={status.tone}>{status.label}</Chip>
                    {detail.errorType ? <code style={{ marginLeft: 8 }}>{detail.errorType}</code> : null}
                  </DetailRow>
                  <DetailRow k="时间">{formatShanghaiTime(detail.createdAt, true)}</DetailRow>
                  <DetailRow k="请求 ID">
                    <code>{detail.requestId}</code>
                  </DetailRow>
                  <DetailRow k="用户">{userEmails.get(detail.userId) ?? detail.userId}</DetailRow>
                  <DetailRow k="团队">
                    {teams.find((team) => team.id === detail.teamId)?.name ?? detail.teamId.slice(0, 8)}
                  </DetailRow>
                  <DetailRow k="设备">{deviceLabel(detail.deviceTokenId)}</DetailRow>
                  <DetailRow k="模型映射">
                    {detail.model} → {detail.upstreamModel || "—"}
                  </DetailRow>
                  <DetailRow k="供应商">{provider}</DetailRow>
                  <DetailRow k="Token（入 / 出 / 缓存读 / 缓存写）">
                    {detail.inputTokens.toLocaleString("en-US")} / {detail.outputTokens.toLocaleString("en-US")} /{" "}
                    {detail.cacheReadTokens.toLocaleString("en-US")} /{" "}
                    {detail.cacheCreationTokens.toLocaleString("en-US")}
                  </DetailRow>
                  <DetailRow k="费用">{credits(detail.creditsMicro)} CR</DetailRow>
                  <DetailRow k="延迟">{detail.latencyMs === null ? "—" : `${detail.latencyMs} ms`}</DetailRow>
                  <DetailRow k="客户端 IP">{detail.clientIp ?? "—"}</DetailRow>
                </>
              );
            })()}
            <div className="dialog-actions">
              <button className="btn btn-ghost" type="button" onClick={() => setDetail(null)}>
                关闭
              </button>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  );
}
