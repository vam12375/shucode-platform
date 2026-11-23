import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { formatCreditsDisplay } from "@shucode-platform/contracts/credits";
import { entityColor } from "../components/PolarCharts.js";

export interface OrganizationQuota {
  id: string;
  name: string;
  usedCredits: string;
  quotaCredits: string;
  unlimited: boolean;
  memberCount: number;
}

export const organizationSeriesColor = entityColor;

function quotaRatio(used: number, quota: number): number {
  return quota > 0 ? Math.max(used / quota, 0) : used > 0 ? 1 : 0;
}

function quotaTone(ratio: number, quota: number | null): string {
  return quota !== null && (quota <= 0 || ratio >= 1)
    ? "var(--crit)"
    : ratio >= 0.85 ? "var(--warn)" : "var(--accent)";
}

interface OrganizationTooltip {
  title: string;
  rows: Array<{ label: string; value: string; color?: string }>;
  x: number;
  y: number;
}

function ChartTooltip({ tip }: { tip: OrganizationTooltip | null }) {
  if (!tip) return null;
  return createPortal(<div className="organization-native-tooltip" role="tooltip" style={{ left: Math.min(Math.max(12, tip.x + 14), window.innerWidth - 280), top: Math.min(tip.y + 14, window.innerHeight - 110) }}><div className="organization-tip-heading">{tip.title}</div>{tip.rows.map((row) => <div className="organization-tip-row" key={row.label}><i style={{ background: row.color ?? "transparent" }} /><span>{row.label}</span><b>{row.value}</b></div>)}</div>, document.body);
}

function annularPath(cx: number, cy: number, inner: number, outer: number, start: number, end: number): string {
  const point = (radius: number, angle: number) => [cx + radius * Math.sin(angle), cy - radius * Math.cos(angle)];
  const [x0, y0] = point(outer, start), [x1, y1] = point(outer, end), [x2, y2] = point(inner, end), [x3, y3] = point(inner, start);
  const large = end - start > Math.PI ? 1 : 0;
  return `M${x0},${y0}A${outer},${outer} 0 ${large} 1 ${x1},${y1}L${x2},${y2}A${inner},${inner} 0 ${large} 0 ${x3},${y3}Z`;
}

export function QuotaBullet({ used, quota }: { used: number; quota: number | null }) {
  const [tip, setTip] = useState<OrganizationTooltip | null>(null);
  const ratio = quotaRatio(used, quota ?? 0);
  if (quota === null) return <span className="organization-unlimited">不限额度 · 已用 {formatCreditsDisplay(used)}</span>;
  const label = `已用 ${formatCreditsDisplay(used)} / 额度 ${formatCreditsDisplay(quota)} credit · ${(ratio * 100).toFixed(1)}%`;
  const rows = [{ label: "已用", value: formatCreditsDisplay(used), color: quotaTone(ratio, quota) }, { label: "额度", value: formatCreditsDisplay(quota) }, { label: "使用率", value: `${(ratio * 100).toFixed(1)}%` }];
  return <div className="organization-bullet" role="img" aria-label={label} tabIndex={0} onMouseMove={(event) => setTip({ title: "团队额度", rows, x: event.clientX, y: event.clientY })} onMouseLeave={() => setTip(null)} onFocus={(event) => { const rect = event.currentTarget.getBoundingClientRect(); setTip({ title: "团队额度", rows, x: rect.x, y: rect.y }); }} onBlur={() => setTip(null)}>
    <div className="organization-bullet-track">
      <i style={{ width: `${Math.min(ratio, 1) * 100}%`, background: quotaTone(ratio, quota) }} />
      <span className="organization-bullet-threshold" />
    </div>
    <span className="organization-bullet-value">{Math.round(ratio * 100)}%</span>
    <ChartTooltip tip={tip} />
  </div>;
}

export function CreditRing({ used, quota, label = "额度使用率" }: { used: number; quota: number | null; label?: string }) {
  const [tip, setTip] = useState<OrganizationTooltip | null>(null);
  const ratio = quotaRatio(used, quota ?? 0);
  const start = -Math.PI * 0.75, end = Math.PI * 0.75;
  const filledEnd = start + Math.PI * 1.5 * (quota === null ? 1 : Math.min(ratio, 1));
  const accessible = quota === null ? "不限额度" : `${label} ${(ratio * 100).toFixed(1)}% · 已用 ${formatCreditsDisplay(used)} credit`;
  const rows = [{ label: "已用", value: `${formatCreditsDisplay(used)} credit`, color: quotaTone(ratio, quota) }, { label: "额度", value: quota === null ? "不限" : `${formatCreditsDisplay(quota)} credit` }];
  return <div className="organization-ring">
    <svg viewBox="0 0 170 146.2" role="img" aria-label={accessible} tabIndex={0} onMouseMove={(event) => setTip({ title: label, rows, x: event.clientX, y: event.clientY })} onMouseLeave={() => setTip(null)} onFocus={(event) => { const rect = event.currentTarget.getBoundingClientRect(); setTip({ title: label, rows, x: rect.x, y: rect.y }); }} onBlur={() => setTip(null)}>
      <path d={annularPath(85, 85, 60, 72, start, end)} fill="rgba(220,234,244,.07)" />
      <path d={annularPath(85, 85, 60, 72, start, Math.max(start + 0.02, filledEnd))} fill={quotaTone(ratio, quota)} />
      <text x="85" y="89" textAnchor="middle" className="organization-ring-number">{quota === null ? "∞" : `${Math.round(ratio * 100)}%`}</text>
      <text x="85" y="107" textAnchor="middle" className="organization-ring-label">{label}</text>
    </svg>
    <ChartTooltip tip={tip} />
  </div>;
}

export function QuotaDistribution({ teams }: { teams: OrganizationQuota[] }) {
  const [tip, setTip] = useState<OrganizationTooltip | null>(null);
  const [width, setWidth] = useState(720);
  const container = useRef<HTMLDivElement>(null);
  const bounded = teams.filter((team) => !team.unlimited && Number(team.quotaCredits) > 0);
  useEffect(() => {
    const element = container.current;
    if (!element) return;
    const observer = new ResizeObserver(() => setWidth(element.clientWidth));
    setWidth(element.clientWidth);
    observer.observe(element);
    return () => observer.disconnect();
  }, [bounded.length]);
  const x = (value: number) => 30 + (width - 60) * Math.min(1.05, value) / 1.05;
  const ticks = [0, 0.25, 0.5, 0.75, 1];
  if (bounded.length === 0) return <div className="organization-chart-empty">{teams.length ? "当前团队均未设置有限额度。" : "创建团队后查看额度分布。"}</div>;
  return <div ref={container} className="organization-distribution">
    <svg width={width} height={130} viewBox={`0 0 ${width} 130`} role="img" aria-label="团队额度使用率分布，圆点大小对应成员数">
      <rect x={x(0.85)} y="20" width={x(1.05) - x(0.85)} height="70" className="organization-risk-region" />
      <text x={x(0.85) + 6} y="32" className="organization-axis">近额区</text>
      {ticks.map((tick) => <g key={tick}>
        <line x1={x(tick)} x2={x(tick)} y1="20" y2="90" className="organization-gridline" />
        <text x={x(tick)} y="108" textAnchor="middle" className="organization-axis">{Math.round(tick * 100)}%</text>
      </g>)}
      {bounded.map((team, index) => {
        const quota = Number(team.quotaCredits);
        const ratio = quotaRatio(Number(team.usedCredits), quota);
        const y = 55 + ((index % 3) - 1) * 16;
        const radius = 5 + team.memberCount / 8;
        const rows = [{ label: "使用率", value: `${(ratio * 100).toFixed(1)}%`, color: quotaTone(ratio, quota) }, { label: "余额", value: formatCreditsDisplay(quota - Number(team.usedCredits)) }];
        return <g key={team.id} tabIndex={0} className="organization-dot" aria-label={`${team.name} · ${Math.round(ratio * 100)}% · ${team.memberCount} 名成员`} onMouseMove={(event) => setTip({ title: team.name, rows, x: event.clientX, y: event.clientY })} onMouseLeave={() => setTip(null)} onFocus={(event) => { const rect = event.currentTarget.getBoundingClientRect(); setTip({ title: team.name, rows, x: rect.x, y: rect.y }); }} onBlur={() => setTip(null)}>
          <circle cx={x(ratio)} cy={y} r={Math.max(14, radius + 4)} fill="transparent" />
          <circle cx={x(ratio)} cy={y} r={radius} style={{ fill: quotaTone(ratio, quota) }} />
          {bounded.length <= 10 ? <text x={x(ratio)} y={y - 10 - team.memberCount / 8} textAnchor="middle" className="organization-dot-label">{team.name}</text> : null}
        </g>;
      })}
    </svg>
    <div className="organization-chart-legend"><span>点大小 = 成员数</span><span><i style={{ background: "var(--accent)" }} />生效</span><span><i style={{ background: "var(--warn)" }} />近额</span><span><i style={{ background: "var(--crit)" }} />额度用尽</span>{teams.some((team) => team.unlimited) ? <span>不限额团队未列入</span> : null}</div>
    <ChartTooltip tip={tip} />
  </div>;
}

export function CreditComposition({ rows }: { rows: Array<{ id: string; label: string; value: number }> }) {
  const total = rows.reduce((sum, row) => sum + Math.max(0, row.value), 0);
  if (total <= 0) return <p className="organization-chart-empty">暂无已结算用量。</p>;
  const top = [...rows].sort((a, b) => b.value - a.value).slice(0, rows.length > 6 ? 5 : 6);
  const rest = rows.reduce((sum, row) => sum + Math.max(row.value, 0), 0) - top.reduce((sum, row) => sum + Math.max(row.value, 0), 0);
  const shown = rest > 0 ? [...top, { id: "other", label: "其他", value: rest }] : top;
  return <CreditSegments rows={shown.map((row) => ({ ...row, color: organizationSeriesColor(row.id) }))} label="模型消费构成" />;
}

export function CreditSegments({ rows, label }: { rows: Array<{ id: string; label: string; value: number; color: string }>; label: string }) {
  const [tip, setTip] = useState<OrganizationTooltip | null>(null);
  const total = rows.reduce((sum, row) => sum + Math.max(0, row.value), 0);
  return <div className="organization-composition"><div className="organization-composition-track" role="img" aria-label={label}>
    {rows.map((row) => <span key={row.id} tabIndex={0} style={{ flex: Math.max(0, row.value) / (total || 1), background: row.color }} onMouseMove={(event) => setTip({ title: row.label, rows: [{ label: "credit", value: formatCreditsDisplay(row.value), color: row.color }, { label: "占比", value: total > 0 ? `${(row.value / total * 100).toFixed(1)}%` : "—" }], x: event.clientX, y: event.clientY })} onMouseLeave={() => setTip(null)} onFocus={(event) => { const rect = event.currentTarget.getBoundingClientRect(); setTip({ title: row.label, rows: [{ label: "credit", value: formatCreditsDisplay(row.value), color: row.color }], x: rect.x, y: rect.y }); }} onBlur={() => setTip(null)} />)}
  </div><div className="organization-composition-legend">{rows.map((row) => <span key={row.id}><i style={{ background: row.color }} /><span>{row.label}</span><b>{total > 0 ? `${Math.round(row.value / total * 100)}%` : "—"}</b></span>)}</div><ChartTooltip tip={tip} /></div>;
}

export interface CreditSample { label: string; value: number }

export function CreditSparkline({ rows }: { rows: CreditSample[] }) {
  const [tip, setTip] = useState<OrganizationTooltip | null>(null);
  const width = 120, height = 24, max = Math.max(...rows.map((row) => row.value), 0), range = max || 1;
  const points = rows.map((row, index) => [index / Math.max(1, rows.length - 1) * (width - 4) + 2, height - 3 - row.value / range * (height - 6)]);
  const path = points.map(([x, y], index) => `${index ? "L" : "M"}${x!.toFixed(1)},${y!.toFixed(1)}`).join("");
  if (!rows.length) return <span className="readout">—</span>;
  return <><svg className="organization-spark" width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label="近30天消耗趋势" tabIndex={0} onMouseMove={(event) => { const rect = event.currentTarget.getBoundingClientRect(); const index = Math.min(rows.length - 1, Math.max(0, Math.round((event.clientX - rect.left) / rect.width * (rows.length - 1)))); const row = rows[index]!; setTip({ title: row.label, rows: [{ label: "消耗", value: `${formatCreditsDisplay(row.value)} credit`, color: "var(--accent)" }], x: event.clientX, y: event.clientY }); }} onMouseLeave={() => setTip(null)} onBlur={() => setTip(null)} onFocus={(event) => { const row = rows.at(-1)!; const rect = event.currentTarget.getBoundingClientRect(); setTip({ title: row.label, rows: [{ label: "消耗", value: `${formatCreditsDisplay(row.value)} credit`, color: "var(--accent)" }], x: rect.x, y: rect.y }); }}>
    <path d={path} fill="none" stroke="var(--accent)" strokeWidth="1.5" strokeLinejoin="round" /><circle cx={points.at(-1)![0]} cy={points.at(-1)![1]} r="2.2" fill="var(--accent)" />
  </svg><ChartTooltip tip={tip} /></>;
}

function niceMaximum(value: number): number {
  if (!(value > 0)) return 1;
  const power = 10 ** Math.floor(Math.log10(value)), fraction = value / power;
  return ([1, 1.2, 1.6, 2, 2.4, 3.2, 4, 5, 6, 8, 10].find((step) => fraction <= step + 1e-9) ?? 10) * power;
}

function roundedTopPath(x: number, y: number, width: number, height: number): string {
  const radius = Math.min(4, width / 2, height);
  return `M${x},${y + height}V${y + radius}Q${x},${y} ${x + radius},${y}H${x + width - radius}Q${x + width},${y} ${x + width},${y + radius}V${y + height}Z`;
}

export function CreditBarChart({ rows, height = 170 }: { rows: CreditSample[]; height?: number }) {
  const container = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(510);
  const [tip, setTip] = useState<OrganizationTooltip | null>(null);
  useEffect(() => { const element = container.current; if (!element) return; const observer = new ResizeObserver(() => setWidth(Math.max(240, element.clientWidth))); setWidth(Math.max(240, element.clientWidth)); observer.observe(element); return () => observer.disconnect(); }, []);
  const max = niceMaximum(Math.max(0, ...rows.map((row) => row.value)) * 1.08);
  const padding = { left: 46, right: 10, top: 12, bottom: 26 };
  const innerWidth = width - padding.left - padding.right, innerHeight = height - padding.top - padding.bottom, band = innerWidth / Math.max(rows.length, 1);
  const barWidth = Math.max(2, Math.min(40, band * 0.6));
  const y = (value: number) => padding.top + innerHeight - value / max * innerHeight;
  const x = (index: number) => padding.left + band * index + band / 2;
  const step = Math.max(1, Math.ceil(rows.length / Math.max(2, innerWidth / 70)));
  return <div ref={container} className="organization-native-bar"><svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label="近30天每日credit消耗">
    {Array.from({ length: 5 }, (_, index) => <g key={index}><line x1={padding.left} x2={width - padding.right} y1={y(max * index / 4)} y2={y(max * index / 4)} className="organization-gridline" /><text x={padding.left - 8} y={y(max * index / 4) + 3.5} textAnchor="end" className="organization-axis">{Math.round(max * index / 4).toLocaleString("en-US")}</text></g>)}
    {rows.map((row, index) => <g key={row.label} className="organization-native-column" tabIndex={0} onMouseMove={(event) => setTip({ title: row.label, rows: [{ label: "消耗", value: `${formatCreditsDisplay(row.value)} credit`, color: "var(--accent)" }], x: event.clientX, y: event.clientY })} onMouseLeave={() => setTip(null)} onFocus={(event) => { const rect = event.currentTarget.getBoundingClientRect(); setTip({ title: row.label, rows: [{ label: "消耗", value: `${formatCreditsDisplay(row.value)} credit`, color: "var(--accent)" }], x: rect.x, y: rect.y }); }} onBlur={() => setTip(null)}><rect x={padding.left + band * index} y={padding.top} width={band} height={innerHeight} fill="transparent" />{row.value > 0 ? <path d={roundedTopPath(x(index) - barWidth / 2, y(row.value), Math.max(0, barWidth - 2), row.value / max * innerHeight)} fill="var(--accent)" /> : null}</g>)}
    {rows.map((row, index) => { if (index % step && index !== rows.length - 1) return null; if (index === rows.length - 1 && index % step && rows.length - 1 - Math.floor((rows.length - 1) / step) * step < step * 0.6) return null; return <text key={row.label} x={x(index)} y={height - 6} textAnchor={index === 0 ? "start" : index === rows.length - 1 ? "end" : "middle"} className="organization-axis">{row.label}</text>; })}
  </svg><ChartTooltip tip={tip} /></div>;
}

export function MemberCreditRanking({ rows }: { rows: Array<{ id: string; name: string; credits: number; requests: number }> }) {
  const [tip, setTip] = useState<OrganizationTooltip | null>(null);
  const max = Math.max(1, ...rows.map((row) => row.credits));
  if (!rows.length) return <p className="organization-chart-empty">暂无成员用量。</p>;
  return <div className="organization-ranking">{rows.map((row, index) => <div key={row.id} className="organization-ranking-row" tabIndex={0} onMouseMove={(event) => setTip({ title: row.name, rows: [{ label: "credit", value: formatCreditsDisplay(row.credits), color: "var(--accent)" }, { label: "请求", value: row.requests.toLocaleString("en-US") }], x: event.clientX, y: event.clientY })} onMouseLeave={() => setTip(null)} onFocus={(event) => { const rect = event.currentTarget.getBoundingClientRect(); setTip({ title: row.name, rows: [{ label: "credit", value: formatCreditsDisplay(row.credits), color: "var(--accent)" }, { label: "请求", value: row.requests.toLocaleString("en-US") }], x: rect.x, y: rect.y }); }} onBlur={() => setTip(null)}><span className="organization-ranking-label"><em>{String(index + 1).padStart(2, "0")}</em>{row.name}</span><span className="organization-ranking-track"><i style={{ width: `${row.credits > 0 ? Math.max(0.5, row.credits / max * 100) : 0}%` }} /></span><span className="organization-ranking-value">{row.credits.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 })}</span></div>)}<ChartTooltip tip={tip} /></div>;
}
