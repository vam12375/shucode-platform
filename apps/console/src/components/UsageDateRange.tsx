import { useEffect, useId, useLayoutEffect, useRef, useState, type CSSProperties } from "react";
import { createPortal } from "react-dom";
import {
  resolveUsageChartRange,
  USAGE_CHART_PRESETS,
  type UsageChartPreset,
} from "../usage-range.js";

const viewportMargin = 16;
const triggerGap = 8;

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
  const [placement, setPlacement] = useState<CSSProperties>({});
  const panel = useRef<HTMLDivElement | null>(null);
  const trigger = useRef<HTMLButtonElement | null>(null);
  const firstPreset = useRef<HTMLButtonElement | null>(null);
  const panelId = useId();
  const width = open ? Math.min(360, document.documentElement.clientWidth - 2 * viewportMargin) : 360;

  // 毛玻璃卡片会成为 fixed 的定位基准。弹层挂到 body，在绘制前按按钮的视口坐标和实际内容高度定位。
  useLayoutEffect(() => {
    if (!open || !trigger.current || !panel.current) return;
    const rect = trigger.current.getBoundingClientRect();
    const viewport = document.documentElement;
    const spaceBelow = viewport.clientHeight - viewportMargin - rect.bottom - triggerGap;
    const spaceAbove = rect.top - triggerGap - viewportMargin;
    const dialog = panel.current;
    const height = dialog.scrollHeight + dialog.offsetHeight - dialog.clientHeight;
    const above = height > spaceBelow && spaceAbove > spaceBelow;
    setPlacement({
      left: Math.max(viewportMargin, Math.min(rect.right - width, viewport.clientWidth - viewportMargin - width)),
      ...(above ? { bottom: viewport.clientHeight - rect.top + triggerGap } : { top: rect.bottom + triggerGap }),
      maxHeight: Math.max(0, above ? spaceAbove : spaceBelow),
    });
  }, [open, width, error]);

  const close = (): void => {
    setOpen(false);
    trigger.current?.focus({ preventScroll: true });
  };
  const show = (): void => {
    if (open) { close(); return; }
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
      const target = event.target as Node;
      if (!trigger.current?.contains(target) && !panel.current?.contains(target)) setOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key === "Escape") {
        event.preventDefault();
        close();
      }
    };
    const openingRect = trigger.current?.getBoundingClientRect();
    const onResize = (): void => setOpen(false);
    const onScroll = (event: Event): void => {
      if (event.target instanceof Node && panel.current?.contains(event.target)) return;
      // 浏览器先将按钮滚入视口，滚动事件可能在点击后才送达；只在按钮实际移位时收起。
      const rect = trigger.current?.getBoundingClientRect();
      if (rect?.top !== openingRect?.top || rect?.left !== openingRect?.left) setOpen(false);
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    window.addEventListener("resize", onResize);
    window.addEventListener("scroll", onScroll, true);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
      window.removeEventListener("resize", onResize);
      window.removeEventListener("scroll", onScroll, true);
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

  return <>
    <button
      ref={trigger}
      className="btn btn-ghost"
      type="button"
      aria-label="趋势时间范围"
      aria-haspopup="dialog"
      aria-expanded={open}
      aria-controls={open ? panelId : undefined}
      onClick={show}
    >{label} <span aria-hidden="true">▾</span></button>
    {open ? createPortal(<div
      ref={panel}
      id={panelId}
      role="dialog"
      aria-label="趋势日期范围"
      style={{ position: "fixed", ...placement, width, zIndex: 80, overflow: "auto", boxSizing: "border-box", padding: 16, border: "1px solid var(--line)", borderRadius: "var(--radius)", background: "var(--surface)", boxShadow: "0 8px 28px var(--shadow-color)" }}
    >
      <p className="readout" style={{ margin: "0 0 10px" }}>选择趋势范围 · 上海时间</p>
      <div role="group" aria-label="趋势范围预设" style={{ display: "grid", gridTemplateColumns: `repeat(${width < 336 ? 3 : 4}, minmax(0, 1fr))`, gap: 6 }}>
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
    </div>, document.body) : null}
  </>;
}
