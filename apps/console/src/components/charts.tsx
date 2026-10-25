import type { ReactNode } from "react";
import {
  Area,
  Bar,
  CartesianGrid,
  Cell,
  ComposedChart,
  Legend,
  Line,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";

export interface ChartDatum {
  label: string;
  [key: string]: string | number | null;
}

export interface ChartSeries {
  key: string;
  label: string;
  color: string;
  kind: "bar" | "line" | "area";
  axis?: "left" | "right";
  /** Tooltip 使用的完整格式。 */
  format?: (value: number) => string;
  /** 坐标轴刻度使用的紧凑格式（避免长单位撑出绘图区）。 */
  axisFormat?: (value: number) => string;
}

const DONUT_COLORS = [
  "var(--donut-color-1, #e8a008)",
  "var(--donut-color-2, #8a5c03)",
  "var(--donut-color-3, #f2ac2c)",
  "var(--donut-color-4, #b07804)",
  "var(--donut-color-5, #f2cf7a)",
  "var(--donut-color-6, #666d70)",
];

interface TooltipPayloadItem {
  dataKey?: unknown;
  value?: unknown;
  name?: unknown;
}

function TrendTooltip({
  active,
  payload,
  label,
  series,
}: {
  active?: boolean;
  payload?: readonly TooltipPayloadItem[];
  label?: unknown;
  series: ChartSeries[];
}) {
  if (!active || !payload || payload.length === 0) return null;
  const byKey = new Map(series.map((item) => [item.key, item]));
  return (
    <div className="chart-tooltip">
      <p className="chart-tooltip-label">{label === undefined ? "" : String(label)}</p>
      {payload.map((item, index) => {
        const key = typeof item.dataKey === "string" || typeof item.dataKey === "number" ? String(item.dataKey) : "";
        const definition = byKey.get(key);
        if (!definition || item.value === null || item.value === undefined) return null;
        const numeric = typeof item.value === "number" ? item.value : Number(item.value);
        const formatted = definition.format ? definition.format(numeric) : numeric.toLocaleString("en-US");
        return (
          <p key={`${key}-${index}`} className="chart-tooltip-row">
            <span className="chart-tooltip-dot" style={{ background: definition.color }} />
            <span className="chart-tooltip-name">{definition.label}</span>
            <span className="chart-tooltip-value">{formatted}</span>
          </p>
        );
      })}
    </div>
  );
}

export function ChartFrame({
  title,
  subtitle,
  action,
  height = 260,
  children,
}: {
  title: string;
  subtitle?: string;
  action?: ReactNode;
  height?: number;
  children: ReactNode;
}) {
  return (
    <div className="chart-card">
      <div className="chart-card-head">
        <div>
          <p className="chart-card-title">{title}</p>
          {subtitle ? <p className="chart-card-sub">{subtitle}</p> : null}
        </div>
        {action}
      </div>
      <div className="chart-card-body" style={{ height }}>
        {children}
      </div>
    </div>
  );
}

export function ComposedTrendChart({
  data,
  series,
  height = 260,
  emptyText = "当前范围暂无可展示的数据。",
}: {
  data: ChartDatum[];
  series: ChartSeries[];
  height?: number;
  emptyText?: string;
}) {
  const hasData = data.some((row) =>
    series.some((item) => {
      const value = row[item.key];
      return typeof value === "number" && value !== 0;
    }),
  );
  if (!hasData) {
    return <p className="chart-empty">{emptyText}</p>;
  }
  const leftSeries = series.filter((item) => (item.axis ?? "left") === "left");
  const rightSeries = series.filter((item) => item.axis === "right");
  const leftFormat = leftSeries.find((item) => item.axisFormat)?.axisFormat ??
    leftSeries.find((item) => item.format)?.format;
  const rightFormat = rightSeries.find((item) => item.axisFormat)?.axisFormat ??
    rightSeries.find((item) => item.format)?.format;
  return (
    <ResponsiveContainer width="100%" height={height}>
      <ComposedChart data={data} margin={{ top: 14, right: rightSeries.length > 0 ? 14 : 16, bottom: 0, left: 4 }}>
        <CartesianGrid stroke="var(--line-soft)" strokeDasharray="3 3" vertical={false} />
        <XAxis
          dataKey="label"
          tick={{ fontSize: 11, fill: "var(--ink-soft)" }}
          tickLine={false}
          axisLine={{ stroke: "var(--line)" }}
          minTickGap={24}
          interval="preserveStartEnd"
        />
        <YAxis
          yAxisId="left"
          tick={{ fontSize: 11, fill: "var(--ink-soft)" }}
          tickLine={false}
          axisLine={false}
          width={62}
          tickMargin={6}
          tickFormatter={(value: number) => (leftFormat ? leftFormat(value) : value.toLocaleString("en-US"))}
        />
        {rightSeries.length > 0 ? (
          <YAxis
            yAxisId="right"
            orientation="right"
            tick={{ fontSize: 11, fill: "var(--ink-soft)" }}
            tickLine={false}
            axisLine={false}
            width={68}
            tickMargin={6}
            tickFormatter={(value: number) => (rightFormat ? rightFormat(value) : value.toLocaleString("en-US"))}
          />
        ) : null}
        <Tooltip
          cursor={{ fill: "color-mix(in srgb, var(--amber) 8%, transparent)" }}
          content={(props) => <TrendTooltip {...props} series={series} />}
        />
        <Legend
          wrapperStyle={{ fontSize: 12, color: "var(--ink-soft)", paddingTop: 6 }}
          iconType="circle"
          iconSize={7}
        />
        {series.map((item) => {
          const axis = item.axis ?? "left";
          if (item.kind === "bar") {
            return (
              <Bar
                key={item.key}
                yAxisId={axis}
                dataKey={item.key}
                name={item.label}
                fill={item.color}
                radius={[2, 2, 0, 0]}
                maxBarSize={30}
              />
            );
          }
          if (item.kind === "area") {
            return (
              <Area
                key={item.key}
                yAxisId={axis}
                type="monotone"
                dataKey={item.key}
                name={item.label}
                stroke={item.color}
                strokeWidth={1.5}
                fill={item.color}
                fillOpacity={0.18}
                dot={false}
              />
            );
          }
          return (
            <Line
              key={item.key}
              yAxisId={axis}
              type="monotone"
              dataKey={item.key}
              name={item.label}
              stroke={item.color}
              strokeWidth={1.8}
              dot={false}
              activeDot={{ r: 3 }}
            />
          );
        })}
      </ComposedChart>
    </ResponsiveContainer>
  );
}

export function ModelDonutChart({
  segments,
  total,
  totalUnit,
  formatValue,
}: {
  segments: { model: string; label?: string; value: number }[];
  total: number;
  totalUnit: string;
  formatValue: (value: number) => string;
}) {
  if (total <= 0 || segments.length === 0) {
    return <p className="chart-empty">当前范围暂无分布数据。</p>;
  }
  return (
    <div className="donut-chart">
      <ResponsiveContainer width="100%" height={260}>
        <PieChart>
          <Pie
            data={segments}
            dataKey="value"
            nameKey="model"
            innerRadius={66}
            outerRadius={98}
            paddingAngle={2}
            stroke="var(--surface)"
            strokeWidth={2}
          >
            {segments.map((segment, index) => (
              <Cell key={segment.model} fill={DONUT_COLORS[index % DONUT_COLORS.length]} />
            ))}
          </Pie>
          <Tooltip content={(props) => <DonutTooltip {...props} formatValue={formatValue} />} />
        </PieChart>
      </ResponsiveContainer>
      <div className="donut-chart-center">
        <span className="donut-total">{formatValue(total)}</span>
        <span className="donut-total-unit">{totalUnit}</span>
      </div>
    </div>
  );
}

function DonutTooltip({
  active,
  payload,
  formatValue,
}: {
  active?: boolean;
  payload?: ReadonlyArray<{ name?: unknown; value?: unknown; payload?: unknown }>;
  formatValue: (value: number) => string;
}) {
  if (!active || !payload || payload.length === 0) return null;
  return (
    <div className="chart-tooltip">
      {payload.map((item, index) => {
        const numeric = typeof item.value === "number" ? item.value : Number(item.value ?? 0);
        const payload = item.payload;
        const model =
          payload && typeof payload === "object" && "model" in payload
            ? String((payload as { label?: unknown; model?: unknown }).label ?? (payload as { model?: unknown }).model ?? "")
            : "";
        return (
          <p key={index} className="chart-tooltip-row">
            <span className="chart-tooltip-name">{model || (item.name === undefined ? "" : String(item.name))}</span>
            <span className="chart-tooltip-value">{formatValue(numeric)}</span>
          </p>
        );
      })}
    </div>
  );
}
