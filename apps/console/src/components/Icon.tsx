import type { ComponentType, ReactNode } from "react";
import type { LucideProps } from "lucide-react";

/**
 * 全站图标唯一的来源：客户端同款 lucide-react（线性图标、圆角端点）。
 * 规格与客户端侧栏对齐——默认 16px、2 描边、继承 currentColor；
 * 页头标题 20px。不要在页面里手写 SVG path，也不要点性 unicode 字形。
 */
export type GlyphIcon = ComponentType<LucideProps>;

export function Glyph({
  icon: Icon,
  size = 16,
  strokeWidth = 2,
  className,
  ...rest
}: { icon: GlyphIcon } & Omit<LucideProps, "ref">) {
  return (
    <Icon
      size={size}
      strokeWidth={strokeWidth}
      className={className ? `glyph ${className}` : "glyph"}
      aria-hidden="true"
      focusable="false"
      {...rest}
    />
  );
}

/** 页头标题：图标 + 标题，和客户端工作区头部的「图标 + 标题」同构。 */
export function PageTitle({
  icon,
  children,
}: {
  icon: GlyphIcon;
  children: ReactNode;
}) {
  return (
    <h1 className="page-title">
      <Glyph icon={icon} size={20} strokeWidth={2} />
      <span>{children}</span>
    </h1>
  );
}
