import { ChevronDown, ChevronLeft, ChevronRight } from "lucide-react";
import { useMemo, useState, type CSSProperties } from "react";
import { cacheRate, promptTokens, usageCredits, type UsageAggregate, type UsageTotals } from "../usage-aggregate.js";
import { usageModelName } from "../usage-costs.js";
import { formatPct } from "./Gauge.js";
import { Glyph } from "./Icon.js";
import { entityColor } from "./polar-colors.js";
import "./usage-model-summary.css";

const PAGE_SIZE = 8;
const METRICS = [
  { key: "requests", label: "请求数", unit: "次" },
  { key: "tokens", label: "总 Token", unit: "Token" },
  { key: "credits", label: "积分", unit: "credit" },
] as const;
type Metric = typeof METRICS[number]["key"];

const TOKEN_PARTS = [
  { key: "inputTokens", label: "输入", color: "var(--donut-color-1)" },
  { key: "cacheReadTokens", label: "缓存读", color: "var(--donut-color-3)" },
  { key: "cacheCreationTokens", label: "缓存写", color: "var(--donut-color-4)" },
  { key: "outputTokens", label: "输出", color: "var(--donut-color-5)" },
] as const;

function metricValue(row: UsageTotals, metric: Metric) {
  if (metric === "credits") return row.creditsMicro;
  if (metric === "tokens") return promptTokens(row) + row.outputTokens;
  return row.requests;
}

function metricText(value: number, metric: Metric) {
  return metric === "credits" ? usageCredits(value) : value.toLocaleString("en-US");
}

function ModelReadings({ row }: { row: UsageTotals }) {
  const hitRate = cacheRate(row.cacheReadTokens, promptTokens(row));
  const totalTokens = metricValue(row, "tokens");
  return <div className="usage-model-readings">
    {totalTokens > 0 && <div className="usage-token-strip" aria-hidden="true">
      {TOKEN_PARTS.map(({ key, color }) =>
        <i key={key} style={{ width: `${row[key] / totalTokens * 100}%`, background: color }} />)}
    </div>}
    <dl>
      {TOKEN_PARTS.map(({ key, label, color }) =>
        <div key={key}>
          <dt><i style={{ background: color }} />{label} Token</dt>
          <dd>{row[key].toLocaleString("en-US")}</dd>
        </div>)}
      <div><dt>缓存命中率</dt><dd>{hitRate === null ? "—" : formatPct(hitRate)}</dd></div>
      <div><dt>请求数</dt><dd>{row.requests.toLocaleString("en-US")}</dd></div>
      <div><dt>积分（credit）</dt><dd>{usageCredits(row.creditsMicro)}</dd></div>
    </dl>
  </div>;
}

export function UsageModelSummary({ rows, totals }: { rows: UsageAggregate[]; totals: UsageTotals }) {
  const [metric, setMetric] = useState<Metric>("requests");
  const [page, setPage] = useState(0);
  const selected = METRICS.find((item) => item.key === metric)!;
  const ranked = useMemo(() => rows.map((row) => {
    const value = metricValue(row, metric);
    return { row, name: usageModelName(row.model, row.modelDisplayName), value, text: metricText(value, metric) };
  }).sort((a, b) => b.value - a.value || a.name.localeCompare(b.name, "zh-CN")), [rows, metric]);
  const pages = Math.ceil(ranked.length / PAGE_SIZE);
  const currentPage = Math.min(page, Math.max(0, pages - 1));
  const start = currentPage * PAGE_SIZE;
  const maximum = ranked[0]?.value ?? 0;
  // 读数位数不同也要共用同一条刻度轴，翻页时保持条形的起点和单位长度一致。
  const valueWidth = ranked.reduce((width, item) => Math.max(width, item.text.length), 5);

  return <>
    <div className="pane-h usage-model-head">
      <div><h2>按模型汇总</h2><p>网关计量 · 团队账单口径</p></div>
      <div className="polar-segmented" role="group" aria-label="模型排行指标">
        {METRICS.map((item) => <button key={item.key} type="button" aria-pressed={metric === item.key}
          onClick={() => { setMetric(item.key); setPage(0); }}>{item.label}</button>)}
      </div>
    </div>
    {ranked.length === 0 ? <p className="polar-chart-empty">所选范围内暂无用量。</p> : <>
      <details className="usage-model-total">
        <summary>
          <span className="usage-model-total-label">区间合计</span>
          <strong>{metricText(metricValue(totals, metric), metric)}</strong>
          <span>{selected.unit}</span>
          <Glyph icon={ChevronDown} size={14} />
        </summary>
        <ModelReadings row={totals} />
      </details>
      <div className="usage-rank-chart" key={metric} style={{ "--rank-value-width": `${valueWidth}ch` } as CSSProperties}>
        {ranked.slice(start, start + PAGE_SIZE).map(({ row, name, value, text }, index) =>
          <details key={row.model} className="usage-rank-row" style={{ "--model-color": entityColor(row.model) } as CSSProperties}>
            <summary>
              <span className="usage-rank-index">{String(start + index + 1).padStart(2, "0")}</span>
              <span className="usage-rank-name">{name}</span>
              <span className="usage-rank-track" aria-hidden="true">
                {value > 0 && <i style={{ width: `${value / maximum * 100}%` }} />}
              </span>
              <span className="usage-rank-value">{text}</span>
              <Glyph icon={ChevronDown} size={13} />
            </summary>
            <ModelReadings row={row} />
          </details>)}
      </div>
      <div className="usage-model-footer">
        <span aria-live="polite">{pages > 1 ? `${start + 1}–${Math.min(start + PAGE_SIZE, ranked.length)} / ` : ""}{ranked.length} 个模型</span>
        {pages > 1 && <div role="group" aria-label="模型排行翻页">
          <button type="button" aria-label="上一页模型" disabled={currentPage === 0} onClick={() => setPage(currentPage - 1)}><Glyph icon={ChevronLeft} size={15} /></button>
          <button type="button" aria-label="下一页模型" disabled={currentPage + 1 >= pages} onClick={() => setPage(currentPage + 1)}><Glyph icon={ChevronRight} size={15} /></button>
        </div>}
      </div>
    </>}
  </>;
}
