import { useEffect, useId, useRef, useState } from "react";
import { formatShanghaiTime } from "../time.js";
import { PolarTooltip, type PolarTooltipData } from "./PolarTooltip.js";
import "./polar-signatures.css";

function arc(cx: number, cy: number, inner: number, outer: number, start: number, end: number): string {
  const point = (radius: number, angle: number) => [cx + radius * Math.sin(angle), cy - radius * Math.cos(angle)];
  const large = end - start > Math.PI ? 1 : 0;
  const [x0, y0] = point(outer, start), [x1, y1] = point(outer, end), [x2, y2] = point(inner, end), [x3, y3] = point(inner, start);
  return `M${x0},${y0}A${outer},${outer} 0 ${large} 1 ${x1},${y1}L${x2},${y2}A${inner},${inner} 0 ${large} 0 ${x3},${y3}Z`;
}

const integer = (value: number) => value.toLocaleString("en-US", { maximumFractionDigits: 0 });
const compact = (value: number) => value >= 1_000_000_000 ? `${(value / 1_000_000_000).toFixed(2)}B` : value >= 1_000_000 ? `${(value / 1_000_000).toFixed(2)}M` : value >= 10_000 ? `${(value / 1000).toFixed(1)}K` : integer(value);

export function NightClock({ hours, now = new Date(), todayRequests }: {
  hours: { hour: number; requests: number; credits?: number; periods?: string[] }[];
  now?: Date;
  todayRequests?: number;
}) {
  const container = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(420);
  const [tip, setTip] = useState<PolarTooltipData | null>(null);
  const id = useId().replace(/[^a-zA-Z0-9_-]/gu, "");
  useEffect(() => {
    const element = container.current;
    if (!element) return;
    const resize = () => setWidth(element.clientWidth);
    resize();
    const observer = new ResizeObserver(resize);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  const size = Math.min(Math.max(width, 1), 300), center = size / 2, radius = center - 22, inner = radius * .38;
  const wall = formatShanghaiTime(now.toISOString());
  const currentHour = Number(wall.slice(11, 13));
  const readings = new Map(hours.map((hour) => [hour.hour, hour]));
  const values = Array.from({ length: 24 }, (_, index) => {
    const hour = (currentHour + 1 + index) % 24;
    return { hour, requests: 0, credits: 0, ...readings.get(hour) };
  });
  const max = Math.max(1, ...values.map((hour) => hour.requests));
  const total = todayRequests ?? values.filter((hour) => hour.hour <= currentHour).reduce((sum, hour) => sum + hour.requests, 0);
  const yesterday = formatShanghaiTime(new Date(now.getTime() - 86_400_000).toISOString()).slice(5, 10);
  return <div className="night-clock" ref={container}>
    <svg width={width} height={size} viewBox={`${(size - width) / 2} 0 ${width} ${size}`} role="img" aria-label="近24小时请求极坐标图，扇形长度表示请求数">
      <defs><filter id={`night-clock-glow-${id}`}><feGaussianBlur stdDeviation={3} result="blur" /><feMerge><feMergeNode in="blur" /><feMergeNode in="SourceGraphic" /></feMerge></filter></defs>
      {[.25, .5, .75, 1].map((fraction) => <circle key={fraction} cx={center} cy={center} r={inner + (radius - inner) * fraction} fill="none" stroke="var(--grid)" />)}
      {values.map((hour, index) => {
        const start = hour.hour / 24 * Math.PI * 2 + .012, end = (hour.hour + 1) / 24 * Math.PI * 2 - .012;
        const outer = inner + (radius - inner) * hour.requests / max;
        const current = hour.hour === currentHour, previous = hour.hour > currentHour;
        const title = hour.periods?.length ? hour.periods.join(" / ") : `${previous ? yesterday : wall.slice(5, 10)} ${String(hour.hour).padStart(2, "0")}:00`;
        const rows = [{ color: "var(--accent)", label: "请求", value: integer(hour.requests) }, { label: "消耗 credit", value: (hour.credits ?? 0).toLocaleString("en-US", { maximumFractionDigits: 4 }) }];
        return <path key={hour.hour} className="night-clock-sector" tabIndex={0} aria-label={`${title} · ${integer(hour.requests)} 次请求`} d={arc(center, center, inner, outer, start, end)}
          fill={current ? "var(--accent)" : previous ? "color-mix(in srgb, var(--accent) 18%, transparent)" : "color-mix(in srgb, var(--accent) 55%, transparent)"}
          filter={current ? `url(#night-clock-glow-${id})` : undefined} style={{ animationDelay: `${index * 25}ms` }}
          onMouseMove={(event) => setTip({ x: event.clientX, y: event.clientY, title, rows })} onMouseLeave={() => setTip(null)}
          onFocus={(event) => { const bounds = event.currentTarget.getBoundingClientRect(); setTip({ x: bounds.right, y: bounds.top, title, rows }); }} onBlur={() => setTip(null)} />;
      })}
      {[0, 6, 12, 18].map((hour) => {
        const angle = hour / 24 * Math.PI * 2;
        return <text key={hour} x={center + Math.sin(angle) * (radius + 12)} y={center - Math.cos(angle) * (radius + 12) + 4} textAnchor="middle" className="night-clock-axis">{String(hour).padStart(2, "0")}</text>;
      })}
      <text x={center} y={center - 4} textAnchor="middle" className="night-clock-total">{compact(total)}</text>
      <text x={center} y={center + 14} textAnchor="middle" className="night-clock-axis">今日请求</text>
    </svg>
    <PolarTooltip data={tip} />
  </div>;
}
