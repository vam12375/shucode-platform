import { useId, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent, type MouseEvent, type RefObject } from "react";
import type { ChartDatum, ChartSeries } from "./charts.js";
import { entityColor } from "./polar-colors.js";
import { PolarTooltip, type PolarTooltipData } from "./PolarTooltip.js";
import "./polar-plots.css";

export interface PolarPlotSeries extends ChartSeries {
  width?: number;
  dash?: string;
  area?: boolean;
  colorAt?: (index: number, value: number) => string;
}

interface ReferenceLine { value: number; label: string }
interface PlotProps {
  data: ChartDatum[];
  series: PolarPlotSeries[];
  height?: number;
  stacked?: boolean;
  emptyText?: string;
  ariaLabel?: string;
  showLegend?: boolean;
  yMin?: number;
  yMax?: number;
  reference?: ReferenceLine;
  band?: [number, number];
  tipTitle?: string;
  tipExtra?: (index: number) => PolarTooltipData["rows"];
}

type Point = [number, number];
const number = (value: number) => Math.round(value).toLocaleString("en-US");

export function polarCompact(value: number): string {
  return value >= 1e9 ? `${(value / 1e9).toFixed(2)}B`
    : value >= 1e6 ? `${(value / 1e6).toFixed(2)}M`
      : value >= 1e4 ? `${(value / 1e3).toFixed(1)}K` : number(value);
}

export function polarCredits(value: number, decimals = 2): string {
  return value.toLocaleString("en-US", { minimumFractionDigits: decimals, maximumFractionDigits: decimals });
}

function niceMax(value: number): number {
  if (!(value > 0)) return 1;
  const power = 10 ** Math.floor(Math.log10(value));
  const fraction = value / power;
  return ([1, 1.2, 1.6, 2, 2.4, 3.2, 4, 5, 6, 8, 10].find((step) => fraction <= step + 1e-9) ?? 10) * power;
}

function linePath(points: Point[]): string {
  return points.map(([x, y], index) => `${index ? "L" : "M"}${x.toFixed(1)},${y.toFixed(1)}`).join("");
}

function smoothPath(points: Point[]): string {
  if (points.length < 2) return linePath(points);
  let path = `M${points[0][0]},${points[0][1]}`;
  for (let index = 0; index < points.length - 1; index += 1) {
    const [x0, y0] = points[Math.max(0, index - 1)];
    const [x1, y1] = points[index];
    const [x2, y2] = points[index + 1];
    const [x3, y3] = points[Math.min(points.length - 1, index + 2)];
    path += ` C${x1 + (x2 - x0) * .18},${y1 + (y2 - y0) * .18} ${x2 - (x3 - x1) * .18},${y2 - (y3 - y1) * .18} ${x2},${y2}`;
  }
  return path;
}

function finite(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value);
}

function usePlotWidth(minimum = 240): [RefObject<HTMLDivElement | null>, number] {
  const ref = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(minimum);
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    const measure = () => setWidth(Math.max(minimum, element.clientWidth));
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [minimum]);
  return [ref, width];
}

function Axes({ labels, width, height, left, right, top = 12, bottom = 26, yMin = 0, yMax, yFormat, x, ticks = 4, minGap = 64 }: {
  labels: string[]; width: number; height: number; left: number; right: number; top?: number; bottom?: number;
  yMin?: number; yMax: number; yFormat: (value: number) => string; x: (index: number) => number; ticks?: number; minGap?: number;
}) {
  const innerHeight = height - top - bottom;
  const innerWidth = width - left - right;
  const step = Math.max(1, Math.ceil(labels.length / Math.max(2, innerWidth / minGap)));
  return <>
    {Array.from({ length: ticks + 1 }, (_, index) => {
      const value = yMin + (yMax - yMin) * index / ticks;
      const y = top + innerHeight - innerHeight * index / ticks;
      return <g key={index}><line className="polar-plot-grid" x1={left} x2={width - right} y1={y} y2={y} /><text className="polar-plot-axis" x={left - 8} y={y + 3.5} textAnchor="end">{yFormat(value)}</text></g>;
    })}
    {labels.map((label, index) => {
      if (index % step && index !== labels.length - 1) return null;
      if (index === labels.length - 1 && index % step && labels.length - 1 - Math.floor((labels.length - 1) / step) * step < step * .6) return null;
      return <text key={index} className="polar-plot-axis" x={x(index)} y={height - 6} textAnchor={index === 0 ? "start" : index === labels.length - 1 ? "end" : "middle"}>{label}</text>;
    })}
  </>;
}

export function PolarLegend({ rows, percentages }: { rows: { key?: string; label: string; color?: string }[]; percentages?: number[] }) {
  return <div className="polar-plot-legend">{rows.map((row, index) => <span key={row.key ?? row.label}><i style={{ background: row.color ?? entityColor(row.key ?? row.label) }} />{row.label}{percentages ? <b>{percentages[index].toFixed(0)}%</b> : null}</span>)}</div>;
}

export function PolarTrendChart({ data, series, height = 250, stacked = false, emptyText = "当前范围暂无数据。", ariaLabel, showLegend = series.length > 1, yMin = 0, yMax, reference, band, tipTitle, tipExtra, curve = false, area, fillOpacity = .88, gradTop = .32, endLabel, padL = 46, padR = 14, padT = 12 }: PlotProps & {
  curve?: boolean; area?: boolean; fillOpacity?: number; gradTop?: number; endLabel?: "value" | "dot"; padL?: number; padR?: number; padT?: number;
}) {
  if (series.length > 0 && series.every((item) => item.kind === "bar")) {
    return <PolarBarChart data={data} series={series} height={height} stacked={stacked} emptyText={emptyText} ariaLabel={ariaLabel} showLegend={showLegend} yMax={yMax} reference={reference} tipTitle={tipTitle} tipExtra={tipExtra} padL={padL} />;
  }
  return <PolarLinePlot data={data} series={series} height={height} stacked={stacked} emptyText={emptyText} ariaLabel={ariaLabel} showLegend={showLegend} yMin={yMin} yMax={yMax} reference={reference} band={band} tipTitle={tipTitle} tipExtra={tipExtra} curve={curve} area={area} fillOpacity={fillOpacity} gradTop={gradTop} endLabel={endLabel} padL={padL} padR={padR} padT={padT} />;
}

function PolarLinePlot({ data, series, height, stacked, emptyText, ariaLabel, showLegend, yMin = 0, yMax, reference, band, tipTitle, tipExtra, curve, area, fillOpacity, gradTop, endLabel, padL: left, padR: right, padT: top }: Required<Pick<PlotProps, "data" | "series" | "height" | "stacked" | "emptyText" | "showLegend">> & Omit<PlotProps, "data" | "series" | "height" | "stacked" | "emptyText" | "showLegend"> & {
  curve: boolean; area?: boolean; fillOpacity: number; gradTop: number; endLabel?: "value" | "dot"; padL: number; padR: number; padT: number;
}) {
  const [ref, width] = usePlotWidth();
  const id = useId().replaceAll(":", "");
  const [hover, setHover] = useState<{ index: number; x: number; y: number } | null>(null);
  const labels = data.map((row) => row.label);
  const innerWidth = width - left - right;
  const innerHeight = height - top - 26;
  const cumulative = Array.from({ length: data.length }, () => 0);
  const layers = series.map((definition) => {
    const low = cumulative.slice();
    const values = data.map((row) => finite(row[definition.key]) ? row[definition.key] as number : null);
    const high = values.map((value, index) => value === null ? null : stacked ? (cumulative[index] += value) : value);
    return { definition, values, low, high };
  });
  const maximum = yMax ?? niceMax(Math.max(0, ...layers.flatMap((layer) => layer.high.filter(finite)), reference?.value ?? 0) * 1.08);
  const upper = maximum > yMin ? maximum : yMin + 1;
  const x = (index: number) => left + (data.length === 1 ? innerWidth / 2 : index * innerWidth / (data.length - 1));
  const y = (value: number) => top + innerHeight - (value - yMin) / (upper - yMin) * innerHeight;
  const formatter = series.find((item) => item.format)?.format ?? polarCompact;
  const axisFormat = series.find((item) => item.axisFormat)?.axisFormat ?? formatter;
  const path = curve ? smoothPath : linePath;
  const available = data.length > 0 && layers.some((layer) => layer.values.some(finite));
  const index = hover ? Math.min(hover.index, data.length - 1) : null;
  const tooltip: PolarTooltipData | null = hover && index !== null && index >= 0 ? {
    x: hover.x, y: hover.y, title: `${tipTitle ? `${tipTitle} · ` : ""}${labels[index]}`,
    rows: [...layers].reverse().map<PolarTooltipData["rows"][number]>((layer) => ({ color: layer.definition.color, label: layer.definition.label, value: layer.values[index] === null ? "—" : (layer.definition.format ?? formatter)(layer.values[index]!) }))
      .concat(stacked && layers.length > 1 ? [{ color: "", label: "合计", value: formatter(layers.reduce((sum, layer) => sum + (layer.values[index] ?? 0), 0)) }] : [])
      .concat(tipExtra?.(index) ?? []),
  } : null;
  const move = (event: MouseEvent<SVGRectElement>) => {
    const box = event.currentTarget.ownerSVGElement!.getBoundingClientRect();
    const local = (event.clientX - box.left) * width / box.width;
    const selected = Math.max(0, Math.min(data.length - 1, Math.round((local - left) / innerWidth * (data.length - 1))));
    setHover({ index: selected, x: event.clientX, y: event.clientY });
  };
  const focusIndex = (selected: number) => {
    const box = ref.current!.getBoundingClientRect();
    setHover({ index: selected, x: box.left + x(selected), y: box.top + top });
  };
  const keyboard = (event: KeyboardEvent<SVGRectElement>) => {
    if (event.key === "Escape") { setHover(null); return; }
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    focusIndex(event.key === "Home" ? 0 : event.key === "End" ? data.length - 1 : Math.max(0, Math.min(data.length - 1, (index ?? 0) + (event.key === "ArrowRight" ? 1 : -1))));
  };
  return <div ref={ref} className="polar-plot polar-trend">
    {!available ? <div className="polar-chart-empty" style={{ minHeight: height }}>{emptyText}</div> : <>
      <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={ariaLabel ?? series.map((item) => item.label).join("、")} onMouseLeave={() => setHover(null)}>
        <defs>{layers.map((layer, layerIndex) => <linearGradient key={layer.definition.key} id={`${id}g${layerIndex}`} x1="0" x2="0" y1="0" y2="1"><stop offset="0" stopColor={layer.definition.color} stopOpacity={gradTop} /><stop offset="1" stopColor={layer.definition.color} stopOpacity={0} /></linearGradient>)}</defs>
        <Axes labels={labels} width={width} height={height} left={left} right={right} top={top} yMin={yMin} yMax={upper} yFormat={axisFormat} x={x} />
        {band ? <rect className="polar-plot-band" x={x(band[0])} y={top} width={x(band[1]) - x(band[0])} height={innerHeight} /> : null}
        {layers.map((layer, layerIndex) => {
          // 缺少 usage 或 prompt 的桶保持断点，不能连线把未知值暗示为实测读数。
          const groups: number[][] = [];
          layer.high.forEach((value, itemIndex) => {
            if (value === null) return;
            if (itemIndex === 0 || layer.high[itemIndex - 1] === null) groups.push([]);
            groups[groups.length - 1].push(itemIndex);
          });
          return <g key={layer.definition.key}>{groups.map((group, groupIndex) => {
            const points: Point[] = group.map((itemIndex) => [x(itemIndex), y(layer.high[itemIndex]!)]);
            if (stacked) {
              const bottom = group.map<Point>((itemIndex) => [x(itemIndex), y(layer.low[itemIndex])]).reverse();
              return <path key={groupIndex} d={`${path(points)}L${path(bottom).slice(1)}Z`} fill={layer.definition.color} fillOpacity={fillOpacity} stroke="var(--surface)" strokeWidth={1.5} className="polar-plot-in" style={{ "--d": `${layerIndex * 60}ms` } as CSSProperties} />;
            }
            return <g key={groupIndex}>
              {/* 原型折线默认带渐变面积；只有显式 area=false 才关闭，不能按 line 类型删掉阴影。 */}
              {layer.definition.area !== false && area !== false ? <path d={`${path(points)}L${x(group[group.length - 1])},${y(yMin)}L${x(group[0])},${y(yMin)}Z`} fill={`url(#${id}g${layerIndex})`} className="polar-plot-in" /> : null}
              <path d={path(points)} fill="none" stroke={layer.definition.color} strokeWidth={layer.definition.width ?? 2} strokeLinejoin="round" strokeLinecap="round" className={`polar-plot-line${layer.definition.dash ? " dashed" : ""}`} style={layer.definition.dash ? { strokeDasharray: layer.definition.dash } : undefined} />
            </g>;
          })}{(() => {
            let last = layer.high.length - 1;
            while (last >= 0 && !finite(layer.high[last])) last -= 1;
            if (stacked || !endLabel || last < 0) return null;
            return <><circle cx={x(last)} cy={y(layer.high[last]!)} r={4} fill={layer.definition.color} stroke="var(--surface)" strokeWidth={2} />{endLabel === "value" ? <text className="polar-plot-end" x={x(last) - 8} y={y(layer.high[last]!) - 10} textAnchor="end">{(layer.definition.format ?? formatter)(layer.values[last]!)}</text> : null}</>;
          })()}</g>;
        })}
        {reference ? <><line className="polar-plot-ref" x1={left} x2={width - right} y1={y(reference.value)} y2={y(reference.value)} /><text className="polar-plot-ref-label" x={left + 6} y={y(reference.value) - 6}>{reference.label}</text></> : null}
        {index !== null ? <g className="polar-plot-cross"><line x1={x(index)} x2={x(index)} y1={top} y2={top + innerHeight} />{layers.map((layer) => finite(layer.high[index]) ? <circle key={layer.definition.key} cx={x(index)} cy={y(layer.high[index]!)} r={4.5} fill={layer.definition.color} stroke="var(--surface)" strokeWidth={2} /> : null)}</g> : null}
        <rect className="polar-plot-hit" x={left} y={top} width={innerWidth} height={innerHeight} fill="transparent" tabIndex={0} aria-label="悬停或按左右键查看分桶明细" onMouseMove={move} onFocus={() => focusIndex(0)} onBlur={() => setHover(null)} onKeyDown={keyboard} />
      </svg>
      {showLegend ? <PolarLegend rows={series.map((item) => ({ key: item.key, label: item.label, color: item.color }))} /> : null}
      <PolarTooltip data={tooltip} />
    </>}
  </div>;
}

function roundTop(x: number, y: number, width: number, height: number, radius: number): string {
  if (height <= 0) return "";
  const r = Math.min(radius, width / 2, height);
  return `M${x},${y + height}V${y + r}Q${x},${y} ${x + r},${y}H${x + width - r}Q${x + width},${y} ${x + width},${y + r}V${y + height}Z`;
}

export function PolarBarChart({ data, series, height = 200, stacked = false, emptyText = "当前范围暂无数据。", ariaLabel, showLegend = series.length > 1, yMax, reference, tipTitle, tipExtra, ratio = .64, maxBar = 40, ticks = 4, minGap = 56, padL: left = 46 }: PlotProps & {
  ratio?: number; maxBar?: number; ticks?: number; minGap?: number; padL?: number;
}) {
  const [ref, width] = usePlotWidth();
  const [tooltip, setTooltip] = useState<PolarTooltipData | null>(null);
  const right = 10, top = 12;
  const innerWidth = width - left - right, innerHeight = height - top - 26;
  const band = innerWidth / Math.max(1, data.length);
  const barWidth = Math.max(2, Math.min(maxBar, band * ratio));
  const totals = data.map((row) => stacked ? series.reduce((sum, item) => sum + (finite(row[item.key]) ? row[item.key] as number : 0), 0) : Math.max(0, ...series.map((item) => finite(row[item.key]) ? row[item.key] as number : 0)));
  const maximum = yMax ?? niceMax(Math.max(0, ...totals, reference?.value ?? 0) * 1.08);
  const y = (value: number) => top + innerHeight - value / maximum * innerHeight;
  const x = (index: number) => left + band * index + band / 2;
  const formatter = series.find((item) => item.format)?.format ?? polarCompact;
  const axisFormat = series.find((item) => item.axisFormat)?.axisFormat ?? formatter;
  const available = data.length > 0 && series.some((item) => data.some((row) => finite(row[item.key])));
  const show = (index: number, pointer: { x: number; y: number }) => setTooltip({ ...pointer, title: `${tipTitle ? `${tipTitle} · ` : ""}${data[index].label}`, rows: [...series].reverse().map<PolarTooltipData["rows"][number]>((item) => {
    const value = data[index][item.key];
    return { color: item.colorAt?.(index, finite(value) ? value : 0) ?? item.color, label: item.label, value: finite(value) ? (item.format ?? formatter)(value) : "—" };
  }).concat(tipExtra?.(index) ?? []) });
  return <div className="polar-plot" ref={ref}>{!available ? <div className="polar-chart-empty" style={{ minHeight: height }}>{emptyText}</div> : <>
    <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={ariaLabel ?? series.map((item) => item.label).join("、")} onMouseLeave={() => setTooltip(null)}>
      <Axes labels={data.map((row) => row.label)} width={width} height={height} left={left} right={right} yMax={maximum} yFormat={axisFormat} x={x} ticks={ticks} minGap={minGap} />
      <g className="polar-plot-bars">{data.map((row, index) => {
        let accumulated = 0;
        const groupWidth = stacked ? barWidth : barWidth / series.length;
        return <g key={index} className="polar-plot-col" tabIndex={0} aria-label={`${row.label} ${series.map((item) => `${item.label} ${finite(row[item.key]) ? (item.format ?? formatter)(row[item.key] as number) : "—"}`).join("，")}`} onMouseMove={(event) => show(index, { x: event.clientX, y: event.clientY })} onFocus={(event) => { const box = event.currentTarget.getBoundingClientRect(); show(index, { x: box.right, y: box.top }); }} onBlur={() => setTooltip(null)}>
          <rect x={left + band * index} y={top} width={band} height={innerHeight} fill="transparent" />
          {series.map((item, seriesIndex) => {
            const value = row[item.key];
            if (!finite(value) || value <= 0) return null;
            const barHeight = value / maximum * innerHeight;
            const isTop = !stacked || seriesIndex === series.length - 1 || series.slice(seriesIndex + 1).every((remaining) => !row[remaining.key]);
            const bx = stacked ? x(index) - barWidth / 2 : x(index) - barWidth / 2 + groupWidth * seriesIndex;
            const by = stacked ? y(accumulated + value) : y(value);
            const h = stacked && accumulated > 0 ? Math.max(0, barHeight - 2) : barHeight;
            const color = item.colorAt?.(index, value) ?? item.color;
            if (stacked) accumulated += value;
            return isTop ? <path className="polar-plot-bar" key={item.key} d={roundTop(bx, by, groupWidth - (stacked ? 0 : 2), h, 4)} fill={color} /> : <rect className="polar-plot-bar" key={item.key} x={bx} y={by} width={groupWidth} height={h} fill={color} />;
          })}
        </g>;
      })}</g>
      {reference ? <><line className="polar-plot-ref" x1={left} x2={width - right} y1={y(reference.value)} y2={y(reference.value)} /><text className="polar-plot-ref-label" x={left + 6} y={y(reference.value) - 6}>{reference.label}</text></> : null}
    </svg>
    {showLegend ? <PolarLegend rows={series.map((item) => ({ key: item.key, label: item.label, color: item.color }))} /> : null}<PolarTooltip data={tooltip} />
  </>}</div>;
}

export function PolarBars({ rows, format, rank = false, emptyText = "暂无排行数据。", max, unit = "数值", color = "var(--accent)" }: {
  rows: { key?: string; label: string; value: number; color?: string; detail?: string; swatch?: boolean }[];
  format: (value: number) => string; rank?: boolean; emptyText?: string; max?: number; unit?: string; color?: string;
}) {
  const [tooltip, setTooltip] = useState<PolarTooltipData | null>(null);
  const maximum = max || Math.max(...rows.map((row) => row.value)) || 1;
  const show = (row: typeof rows[number], x: number, y: number) => setTooltip({ x, y, title: row.label, rows: [{ color: row.color ?? color, label: unit, value: format(row.value) }].concat(row.detail ? [{ color: "", label: "明细", value: row.detail }] : []) });
  if (!rows.length) return <div className="polar-chart-empty">{emptyText}</div>;
  return <div className="polar-hbars" onMouseLeave={() => setTooltip(null)}>{rows.map((row, index) => <div className="polar-hbar-row" key={row.key ?? row.label} tabIndex={0} aria-label={`${row.label}：${format(row.value)}`} onMouseMove={(event) => show(row, event.clientX, event.clientY)} onFocus={(event) => { const box = event.currentTarget.getBoundingClientRect(); show(row, box.right, box.top); }} onBlur={() => setTooltip(null)}>
    <span className="polar-hbar-label">{rank ? <em>{String(index + 1).padStart(2, "0")}</em> : null}{row.swatch ? <i style={{ background: row.color }} /> : null}{row.label}</span>
    <span className="polar-hbar-track"><span className="polar-hbar-fill" style={{ width: `${Math.max(.5, row.value / maximum * 100)}%`, background: row.color ?? color }} /></span>
    <span className="polar-hbar-value">{format(row.value)}</span>
  </div>)}<PolarTooltip data={tooltip} /></div>;
}

export function PolarSegments({ rows, format, showLegend = true, height = 12, ariaLabel = "消费构成" }: {
  rows: { key?: string; label: string; value: number; color?: string }[];
  format: (value: number) => string; showLegend?: boolean; height?: number; ariaLabel?: string;
}) {
  const [tooltip, setTooltip] = useState<PolarTooltipData | null>(null);
  const total = rows.reduce((sum, row) => sum + Math.max(0, row.value), 0);
  const show = (row: typeof rows[number], x: number, y: number) => setTooltip({ x, y, title: row.label, rows: [{ color: row.color ?? entityColor(row.key ?? row.label), label: format(row.value), value: `${(row.value / total * 100).toFixed(1)}%` }] });
  if (total <= 0) return <div className="polar-chart-empty">暂无构成数据。</div>;
  return <div className="polar-segments-wrap"><div className="polar-segments" role="img" aria-label={ariaLabel} style={{ height }} onMouseLeave={() => setTooltip(null)}>{rows.map((row) => <span key={row.key ?? row.label} tabIndex={0} aria-label={`${row.label} · ${format(row.value)} · ${(row.value / total * 100).toFixed(1)}%`} style={{ flex: Math.max(0, row.value) / total, background: row.color ?? entityColor(row.key ?? row.label) }} onMouseMove={(event) => show(row, event.clientX, event.clientY)} onFocus={(event) => { const box = event.currentTarget.getBoundingClientRect(); show(row, box.right, box.top); }} onBlur={() => setTooltip(null)} />)}</div>
    {showLegend ? <PolarLegend rows={rows} percentages={rows.map((row) => row.value / total * 100)} /> : null}<PolarTooltip data={tooltip} />
  </div>;
}

export function PolarSparkline({ values, width = 96, height = 28, color = "var(--accent)", area = false, zero = false, strokeWidth = 1.5 }: {
  values: (number | null)[]; width?: number; height?: number; color?: string; area?: boolean; zero?: boolean; strokeWidth?: number;
}) {
  const actual = values.filter(finite);
  if (!actual.length) return null;
  const maximum = Math.max(...actual), minimum = zero ? 0 : Math.min(...actual), range = maximum - minimum || 1;
  const points = values.map((value, index): Point | null => finite(value) ? [values.length === 1 ? width - 2 : index / (values.length - 1) * (width - 4) + 2, height - 3 - (value - minimum) / range * (height - 6)] : null);
  const groups: Point[][] = [];
  points.forEach((point, index) => { if (!point) return; if (index === 0 || !points[index - 1]) groups.push([]); groups[groups.length - 1].push(point); });
  const end = points[points.length - 1];
  return <svg className="polar-spark" width={width} height={height} viewBox={`0 0 ${width} ${height}`} aria-hidden="true">{groups.map((group, index) => <g key={index}>{area ? <path d={`${linePath(group)}L${group[group.length - 1][0]},${height}L${group[0][0]},${height}Z`} fill={color} fillOpacity={.14} /> : null}<path d={linePath(group)} fill="none" stroke={color} strokeWidth={strokeWidth} strokeLinejoin="round" /></g>)}{end ? <circle cx={end[0]} cy={end[1]} r={2.2} fill={color} /> : null}</svg>;
}

function lerpColor(from: string, to: string, ratio: number): string {
  const rgb = (hex: string) => [0, 2, 4].map((offset) => Number.parseInt(hex.replace("#", "").slice(offset, offset + 2), 16));
  const first = rgb(from), last = rgb(to);
  return `#${first.map((value, index) => Math.round(value + (last[index] - value) * ratio).toString(16).padStart(2, "0")).join("")}`;
}

export function PolarHeatmap({ rows, values, from = "#101B25", to = "#8FE3FF", cellHeight = 24, gap = 3, radius = 3, ariaLabel = "星期与小时请求热力图", format = number, unit = "请求" }: {
  rows: string[]; values: number[][]; from?: string; to?: string; cellHeight?: number; gap?: number; radius?: number; ariaLabel?: string; format?: (value: number) => string; unit?: string;
}) {
  const [ref, width] = usePlotWidth(280);
  const [tooltip, setTooltip] = useState<PolarTooltipData | null>(null);
  const columns = values[0]?.length ?? 24, labelWidth = 34, top = 16;
  const cellWidth = (width - labelWidth) / columns;
  const height = top + rows.length * cellHeight + 4;
  const all = values.flat(), maximum = Math.max(0, ...all), minimum = Math.min(0, ...all);
  return <div className="polar-plot polar-heatmap-wrap" ref={ref}><svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={ariaLabel} onMouseLeave={() => setTooltip(null)}>
    {Array.from({ length: Math.ceil(columns / 3) }, (_, index) => <text className="polar-plot-axis" key={index} x={labelWidth + index * 3 * cellWidth + cellWidth / 2} y={11} textAnchor="middle">{String(index * 3).padStart(2, "0")}</text>)}
    {rows.map((row, rowIndex) => <g key={row}><text className="polar-plot-axis" x={0} y={top + rowIndex * cellHeight + cellHeight / 2 + 4}>{row}</text>{(values[rowIndex] ?? []).map((value, column) => {
      // 全零网格的色阶分母也是零；此时使用最低色，保留真实的 0 请求提示。
      const ratio = maximum === minimum ? 0 : (value - minimum) / (maximum - minimum);
      const color = lerpColor(from, to, ratio ** .9);
      const show = (x: number, y: number) => setTooltip({ x, y, title: `${row} ${String(column).padStart(2, "0")}:00`, rows: [{ color, label: unit, value: format(value) }] });
      return <rect key={column} className="polar-heat-cell" x={labelWidth + column * cellWidth + gap / 2} y={top + rowIndex * cellHeight + gap / 2} width={cellWidth - gap} height={cellHeight - gap} rx={radius} fill={color} tabIndex={0} aria-label={`${row} ${String(column).padStart(2, "0")}:00 · ${format(value)} ${unit}`} onMouseMove={(event) => show(event.clientX, event.clientY)} onFocus={(event) => { const box = event.currentTarget.getBoundingClientRect(); show(box.right, box.top); }} onBlur={() => setTooltip(null)} />;
    })}</g>)}
  </svg><div className="polar-heat-scale"><span>低</span><i style={{ background: `linear-gradient(90deg,${from},${to})` }} /><span>高</span></div><PolarTooltip data={tooltip} /></div>;
}

export function RecordHeatmap({ records }: { records: { createdAt: string }[] }) {
  const values = useMemo(() => {
    const grid = Array.from({ length: 7 }, () => Array.from({ length: 24 }, () => 0));
    for (const record of records) {
      const parsed = Date.parse(record.createdAt);
      if (!Number.isFinite(parsed)) continue;
      const wall = new Date(parsed + 8 * 3_600_000);
      grid[(wall.getUTCDay() + 6) % 7][wall.getUTCHours()] += 1;
    }
    return grid;
  }, [records]);
  return <PolarHeatmap rows={["周一", "周二", "周三", "周四", "周五", "周六", "周日"]} values={values} ariaLabel="本页记录按上海墙钟星期和小时的请求数" />;
}

export function PolarStatusHexes({ items }: { items: { key: string; label: string; tone: "ok" | "warn" | "crit" | "idle"; detail: string }[] }) {
  const [tooltip, setTooltip] = useState<PolarTooltipData | null>(null);
  return <div className="polar-status-hexes" onMouseLeave={() => setTooltip(null)}>{items.map((item) => {
    const show = (x: number, y: number) => setTooltip({ x, y, title: item.label, rows: [{ label: "状态", value: item.detail }] });
    return <span key={item.key} className={`polar-status-hex ${item.tone}`} tabIndex={0} aria-label={`${item.label} · ${item.detail}`} onMouseMove={(event) => show(event.clientX, event.clientY)} onFocus={(event) => { const box = event.currentTarget.getBoundingClientRect(); show(box.right, box.top); }} onBlur={() => setTooltip(null)} />;
  })}<PolarTooltip data={tooltip} /></div>;
}

export function PolarFlowBars({ rows, unit = "并发" }: { rows: { key: string; label: string; value: number | null; limit: number }[]; unit?: string }) {
  const [tooltip, setTooltip] = useState<PolarTooltipData | null>(null);
  return <div className="polar-ops-flow-list" onMouseLeave={() => setTooltip(null)}>{rows.map((row) => {
    const ratio = row.limit > 0 && row.value !== null ? row.value / row.limit : 0;
    const tone = ratio > .9 ? "crit" : ratio > .75 ? "warn" : "ok";
    const color = tone === "ok" ? "var(--accent)" : `var(--${tone})`;
    const value = row.value === null ? "—" : row.value;
    const show = (x: number, y: number) => setTooltip({ x, y, title: row.label, rows: [{ color, label: unit, value: `${value} / ${row.limit > 0 ? row.limit : "不限"}` }] });
    return <div key={row.key} className="polar-ops-flow-row" tabIndex={0} aria-label={`${row.label} · ${value} ${unit} · 上限 ${row.limit > 0 ? row.limit : "不限"}`} onMouseMove={(event) => show(event.clientX, event.clientY)} onFocus={(event) => { const box = event.currentTarget.getBoundingClientRect(); show(box.right, box.top); }} onBlur={() => setTooltip(null)}><div><span>{row.label}</span><b>{value} / {row.limit > 0 ? row.limit : "不限"}{tone !== "ok" ? <i style={{ color }}>●</i> : null}</b></div><div className="polar-ops-flow-track"><span style={{ width: `${Math.min(100, ratio * 100)}%`, background: color }} /><i /></div></div>;
  })}<PolarTooltip data={tooltip} /></div>;
}
