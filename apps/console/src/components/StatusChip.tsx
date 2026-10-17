import type { ReactNode } from "react";

/** 状态点：多维度编码（色点 + 文字）。tone 缺省为生效/正常，hot 近额/中断，stop 告警，mute 降噪。 */
export function Chip({
  tone,
  children,
}: {
  tone?: "hot" | "stop" | "mute";
  children: ReactNode;
}) {
  return <span className={tone ? `chip ${tone}` : "chip"}>{children}</span>;
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
