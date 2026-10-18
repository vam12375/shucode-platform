import type { ReactNode } from "react";

/** 五级状态语义（design.md §9）：ok 成功、info 进行中、hot 告警（近额/中断）、stop 失败、mute 中性。 */
export type ChipTone = "ok" | "info" | "hot" | "stop" | "mute";

/** 状态点：多维度编码（色点 + 文字）。缺省墨点为与状态无关的通用标签（如模型 ID），不要用于业务状态。title 为可选原生提示。 */
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
      {k ? <span className="k">{k}</span> : null}
      {children}
      {action ? <a href={action.href}>{action.label}</a> : null}
    </div>
  );
}
