import { useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import "./polar-signatures.css";

export interface PolarTooltipData {
  x: number;
  y: number;
  title: string;
  rows: { label: string; value: string; color?: string }[];
}

export function PolarTooltip({ data }: { data: PolarTooltipData | null }) {
  const ref = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ width: 210, height: 110 });
  useLayoutEffect(() => {
    if (ref.current) setSize({ width: ref.current.offsetWidth, height: ref.current.offsetHeight });
  }, [data?.title, data?.rows.length]);
  if (!data) return null;
  const left = data.x + 14 + size.width > window.innerWidth - 8 ? data.x - size.width - 14 : data.x + 14;
  const top = data.y + 14 + size.height > window.innerHeight - 8 ? data.y - size.height - 14 : data.y + 14;
  return createPortal(<div ref={ref} className="polar-tip" role="tooltip" style={{ transform: `translate(${Math.max(8, left)}px, ${Math.max(8, top)}px)` }}>
    <div className="polar-tip-head">{data.title}</div>
    {data.rows.map((row, index) => <div className="polar-tip-row" key={`${row.label}-${index}`}>
      {row.color ? <i style={{ background: row.color }} /> : null}<span>{row.label}</span><b>{row.value}</b>
    </div>)}
  </div>, document.body);
}
