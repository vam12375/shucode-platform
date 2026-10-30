import type { ReactNode } from "react";
import { CircleAlert, CircleCheck, CircleDashed, CircleMinus, CircleX, Info, TriangleAlert } from "lucide-react";
import { Glyph, type GlyphIcon } from "./Icon.js";

/** 五级状态语义（design.md §9）：ok 成功、info 进行中、hot 告警（近额/中断）、stop 失败、mute 中性。 */
export type ChipTone = "ok" | "info" | "hot" | "stop" | "mute";

/** 语义图标与 tone 一一对应：形状 + 颜色 + 文字三重编码，不单靠颜色。 */
const TONE_ICONS: Record<ChipTone, GlyphIcon> = {
  ok: CircleCheck,
  info: CircleDashed,
  hot: TriangleAlert,
  stop: CircleX,
  mute: CircleMinus,
};

/** 状态标签：语义 tone 用客户端同款图标；缺省墨点为与状态无关的通用标签（如模型 ID），不要用于业务状态。 */
export function Chip({
  tone,
  title,
  children,
}: {
  tone?: ChipTone;
  title?: string;
  children: ReactNode;
}) {
  return (
    <span className={tone ? `chip ${tone}` : "chip"} title={title}>
      {tone ? <Glyph icon={TONE_ICONS[tone]} size={12} strokeWidth={2} /> : null}
      {children}
    </span>
  );
}

/** 提示条：琥珀为提示，tone="stop" 为错误。k 为加粗前缀，action 渲染右侧链接。 */
export function Notice({
  k,
  tone,
  action,
  children,
}: {
  k?: string;
  tone?: "stop";
  action?: { href: string; label: string };
  children: ReactNode;
}) {
  return (
    <div className={tone === "stop" ? "notice stop" : "notice"} role={tone === "stop" ? "alert" : "status"}>
      <Glyph icon={tone === "stop" ? CircleAlert : Info} size={15} strokeWidth={2} />
      {k ? <span className="k">{k}</span> : null}
      {children}
      {action ? <a href={action.href}>{action.label}</a> : null}
    </div>
  );
}
