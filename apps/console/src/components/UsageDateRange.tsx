import { useEffect, useId, useRef, useState } from "react";
import {
  resolveUsageChartRange,
  USAGE_CHART_PRESETS,
  type UsageChartPreset,
} from "../usage-range.js";

export function UsageDateRange({ preset, from, to, label, onApply }: {
  preset: UsageChartPreset;
  from: string;
  to: string;
  label: string;
  onApply: (preset: UsageChartPreset, from: string, to: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [draftPreset, setDraftPreset] = useState<UsageChartPreset>(preset);
  const [draftFrom, setDraftFrom] = useState(from);
  const [draftTo, setDraftTo] = useState(to);
  const [error, setError] = useState("");
  const [anchor, setAnchor] = useState({ left: 16, top: 16, width: 360, maxHeight: 420 });
  const root = useRef<HTMLDivElement | null>(null);
  const trigger = useRef<HTMLButtonElement | null>(null);
  const firstPreset = useRef<HTMLButtonElement | null>(null);
  const panelId = useId();

  const close = (): void => {
    setOpen(false);
    trigger.current?.focus({ preventScroll: true });
  };
  const show = (): void => {
    if (open) { close(); return; }
    const rect = trigger.current?.getBoundingClientRect();
    if (rect) {
      const width = Math.min(360, window.innerWidth - 32);
      const top = rect.bottom + 8 + 320 < window.innerHeight
        ? rect.bottom + 8
        : Math.max(16, rect.top - 360);
      setAnchor({
        left: Math.max(16, Math.min(rect.right - width, window.innerWidth - width - 16)),
        top,
        width,
        maxHeight: Math.max(160, window.innerHeight - top - 16),
      });
    }
    setDraftPreset(preset);
    setDraftFrom(from);
    setDraftTo(to);
    setError("");
    setOpen(true);
  };

  useEffect(() => {
    if (!open) return;
    firstPreset.current?.focus({ preventScroll: true });
    const onPointerDown = (event: PointerEvent): void => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key === "Escape") {
        event.preventDefault();
        close();
      }
    };
    const onViewportChange = (event: Event): void => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    window.addEventListener("resize", onViewportChange);
    window.addEventListener("scroll", onViewportChange, true);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
      window.removeEventListener("resize", onViewportChange);
      window.removeEventListener("scroll", onViewportChange, true);
    };
  }, [open]);

  const selectPreset = (value: UsageChartPreset): void => {
    const range = resolveUsageChartRange(value);
    setDraftPreset(value);
    setDraftFrom(range.from);
    setDraftTo(range.to);
    setError("");
  };
  const apply = (): void => {
    try {
      // 非法自然日或倒置范围只在本地报错，不能产生一次错误查询或修改当前图表。
      resolveUsageChartRange(draftPreset, draftFrom, draftTo);
      onApply(draftPreset, draftFrom, draftTo);
      close();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "请选择有效的开始和结束日期。");
    }
  };

  return <div ref={root} style={{ position: "relative" }}>
    <button
      ref={trigger}
      className="btn btn-ghost"
      type="button"
      aria-label="趋势时间范围"
      aria-haspopup="dialog"
      aria-expanded={open}
      aria-controls={panelId}
      onClick={show}
    >{label} <span aria-hidden="true">▾</span></button>
    {open ? <div
      id={panelId}
      role="dialog"
      aria-label="趋势日期范围"
      style={{ position: "fixed", ...anchor, zIndex: 80, overflow: "auto", boxSizing: "border-box", padding: 16, border: "1px solid var(--line)", borderRadius: "var(--radius)", background: "var(--surface)", boxShadow: "0 8px 28px rgba(0, 0, 0, 0.14)" }}
    >
      <p className="readout" style={{ margin: "0 0 10px" }}>选择趋势范围 · 上海时间</p>
      <div role="group" aria-label="趋势范围预设" style={{ display: "grid", gridTemplateColumns: `repeat(${anchor.width < 336 ? 3 : 4}, minmax(0, 1fr))`, gap: 6 }}>
        {USAGE_CHART_PRESETS.map((item, index) => <button
          key={item.value}
          ref={index === 0 ? firstPreset : undefined}
          className={"btn btn-sm" + (draftPreset === item.value ? "" : " btn-ghost")}
          type="button"
          style={{ minWidth: 0, padding: "6px 4px", whiteSpace: "normal" }}
          aria-pressed={draftPreset === item.value}
          onClick={() => selectPreset(item.value)}
        >{item.label}</button>)}
      </div>
      <div style={{ display: "grid", gridTemplateColumns: "repeat(2, minmax(0, 1fr))", gap: 10, margin: "16px 0 10px" }}>
        <label className="field" style={{ minWidth: 0 }}>
          <span className="field-label">开始日期</span>
          <input className="input" type="date" aria-label="趋势开始日期" style={{ width: "100%", minWidth: 0 }} value={draftFrom} onChange={(event) => { setDraftFrom(event.target.value); setDraftPreset("custom"); setError(""); }} />
        </label>
        <label className="field" style={{ minWidth: 0 }}>
          <span className="field-label">结束日期</span>
          <input className="input" type="date" aria-label="趋势结束日期" style={{ width: "100%", minWidth: 0 }} value={draftTo} onChange={(event) => { setDraftTo(event.target.value); setDraftPreset("custom"); setError(""); }} />
        </label>
      </div>
      <p className="readout" style={{ margin: "0 0 12px", whiteSpace: "normal" }}>单个自然日和近 24 小时按小时；跨自然日按天。近 24 小时为实时滚动范围。</p>
      {error ? <p role="alert" className="readout" style={{ color: "var(--signal)", whiteSpace: "normal" }}>{error}</p> : null}
      <div className="form-row" style={{ justifyContent: "flex-end" }}>
        <button className="btn btn-ghost" type="button" onClick={close}>取消</button>
        <button className="btn" type="button" onClick={apply}>应用</button>
      </div>
    </div> : null}
  </div>;
}
