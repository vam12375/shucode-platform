import { useEffect, useState } from "react";
import { formatCreditsDisplay } from "@shucode-platform/contracts/credits";

function prefersReducedMotion(): boolean {
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

function withDecimals(value: number, decimals: number): string {
  return value.toLocaleString("en-US", { minimumFractionDigits: decimals, maximumFractionDigits: decimals });
}

function clampPct(value: number, quota: number): number {
  if (!(quota > 0)) return 0;
  return Math.min(Math.max(value / quota, 0), 1);
}

/** 百分比读数：保留一位小数并去掉末尾的 .0（42.9% / 100%）。 */
export function formatPct(ratio: number): string {
  const rounded = Math.round(ratio * 1000) / 10;
  return `${rounded.toFixed(1).replace(/\.0$/u, "")}%`;
}

function compact(value: number): string {
  return value.toLocaleString("en-US", { maximumFractionDigits: 2 });
}

/** 数字进位（count-up）：700ms 三次缓出；reduced-motion 时直接落终值。 */
export function useCountUp(target: number, decimals = 0): string {
  const [text, setText] = useState(() => withDecimals(prefersReducedMotion() ? target : 0, decimals));

  useEffect(() => {
    if (prefersReducedMotion()) {
      setText(withDecimals(target, decimals));
      return;
    }
    let raf = 0;
    const t0 = performance.now();
    const tick = (t: number) => {
      const p = Math.min((t - t0) / 700, 1);
      const ease = 1 - (1 - p) ** 3;
      setText(withDecimals(target * ease, decimals));
      if (p < 1) raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [target, decimals]);

  return text;
}

interface GaugeProps {
  /** 已用额度（credit）。 */
  used: number;
  /** 额度上限（credit）；null 表示不限额度，此时按原型渲染「不限额度」读数。 */
  quota: number | null;
  /** 近额阈值（默认 0.9），达到后填充与读数转为信号色。 */
  hotAt?: number;
  /** 登录卡等装饰场景：不渲染刻度与读数。 */
  bare?: boolean;
}

/**
 * 指针刻度尺（签名元素）：填充 + 指针 + 刻度 + 读数四位一体。
 * 挂载时指针从 0 扫到现值（0.9s 缓动，reduced-motion 直达）。
 */
export function Gauge({ used, quota, hotAt = 0.9, bare = false }: GaugeProps) {
  const unlimited = quota === null;
  const ratio = unlimited ? 0 : clampPct(used, quota);
  const hot = ratio >= hotAt;
  const [armed, setArmed] = useState(() => unlimited || prefersReducedMotion());

  useEffect(() => {
    if (armed) return;
    const raf = requestAnimationFrame(() => setArmed(true));
    return () => cancelAnimationFrame(raf);
  }, [armed]);

  if (unlimited && !bare) {
    return <span className="readout">不限额度</span>;
  }

  const boundedQuota = unlimited ? 0 : (quota ?? 0);
  const width = `${(ratio * 100).toFixed(2)}%`;
  const stops = unlimited ? [] : [0, 0.25, 0.5, 0.75, 1].map((k) => compact(boundedQuota * k));

  return (
    <div
      className="gauge"
      role="img"
      aria-label={unlimited ? "不限额度" : `已用 ${formatCreditsDisplay(used)} / ${formatCreditsDisplay(quota)}，${formatPct(ratio)}`}
    >
      <div className="gauge-body" style={bare ? { width: "100%" } : undefined}>
        <div className="gauge-track" aria-hidden="true">
          <div className={hot ? "gauge-fill hot" : "gauge-fill"} style={{ width: armed ? width : "0%" }} />
          <div className="gauge-needle" style={{ left: armed ? width : "0%" }} />
        </div>
        {!bare ? (
          <div className="gauge-scale" aria-hidden="true">
            {stops.map((label, index) => (
              <span key={index}>{label}</span>
            ))}
          </div>
        ) : null}
      </div>
      {!bare ? (
        <div className="gauge-read">
          <b>{formatCreditsDisplay(used)}</b> / {formatCreditsDisplay(boundedQuota)}
          <span className={hot ? "pct hot" : "pct"}>{formatPct(ratio)}</span>
        </div>
      ) : null}
    </div>
  );
}
