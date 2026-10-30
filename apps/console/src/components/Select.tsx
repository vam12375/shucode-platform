import { ChevronDown } from "lucide-react";
import {
  useEffect,
  useId,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import { Glyph } from "./Icon.js";

export interface SelectOption {
  value: string;
  label: string;
}

/**
 * 自制下拉（替代原生 select，全站统一）：计量台令牌样式，键盘可达——
 * ↑/↓ 移动高亮、Enter/Space 展开（展开时选择）、Esc 关闭、Home/End 首尾；
 * 点击外部或页面滚动关闭。
 *
 * 菜单用 position: fixed 按触发器坐标计算：绝对定位会被祖先的层叠上下文
 * （面板入场动画）与滚动容器（表格 overflow）裁剪；视口下方空间不足时向上翻转。
 */
export function Select({
  options,
  value,
  onChange,
  ariaLabel,
  disabled = false,
  style,
}: {
  options: SelectOption[];
  value: string;
  onChange: (value: string) => void;
  ariaLabel: string;
  disabled?: boolean;
  style?: CSSProperties;
}) {
  const [open, setOpen] = useState(false);
  const [highlighted, setHighlighted] = useState(0);
  const [menuAnchor, setMenuAnchor] = useState<{
    left: number;
    width: number;
    top?: number;
    bottom?: number;
    maxHeight: number;
  } | null>(null);
  const rootRef = useRef<HTMLDivElement | null>(null);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const listId = useId();

  const current = options.find((option) => option.value === value) ?? options[0];

  useEffect(() => {
    const index = options.findIndex((option) => option.value === value);
    setHighlighted(index >= 0 ? index : 0);
  }, [value, options]);

  useEffect(() => {
    if (!open) return;
    const item = menuRef.current?.children[highlighted];
    (item as HTMLElement | undefined)?.scrollIntoView({ block: "nearest" });
  }, [open, highlighted]);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent): void => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    // 滚动/缩放时关闭（捕获阶段覆盖内部滚动容器）；菜单自身滚动不关闭。
    const onScroll = (event: Event): void => {
      if (menuRef.current?.contains(event.target as Node)) return;
      setOpen(false);
    };
    document.addEventListener("pointerdown", onPointerDown);
    window.addEventListener("scroll", onScroll, true);
    window.addEventListener("resize", onScroll);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      window.removeEventListener("scroll", onScroll, true);
      window.removeEventListener("resize", onScroll);
    };
  }, [open]);

  const openMenu = (): void => {
    const rect = triggerRef.current?.getBoundingClientRect();
    if (!rect) return;
    const viewportHeight = window.innerHeight;
    const spaceBelow = viewportHeight - rect.bottom - 8;
    const spaceAbove = rect.top - 8;
    const preferBelow = spaceBelow >= 160 || spaceBelow >= spaceAbove;
    setMenuAnchor({
      left: rect.left,
      width: rect.width,
      ...(preferBelow
        ? { top: rect.bottom + 4, maxHeight: Math.min(264, Math.max(spaceBelow, 80)) }
        : { bottom: viewportHeight - rect.top + 4, maxHeight: Math.min(264, Math.max(spaceAbove, 80)) }),
    });
    setOpen(true);
  };

  const commit = (index: number): void => {
    const option = options[index];
    if (!option) return;
    onChange(option.value);
    setOpen(false);
  };

  const onKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>): void => {
    if (disabled) return;
    switch (event.key) {
      case "Enter":
      case " ":
        event.preventDefault();
        if (open) {
          commit(highlighted);
        } else {
          openMenu();
        }
        break;
      case "Escape":
        if (open) {
          event.preventDefault();
          setOpen(false);
        }
        break;
      case "ArrowDown":
        event.preventDefault();
        if (open) setHighlighted((index) => Math.min(index + 1, options.length - 1));
        else openMenu();
        break;
      case "ArrowUp":
        event.preventDefault();
        if (open) setHighlighted((index) => Math.max(index - 1, 0));
        else openMenu();
        break;
      case "Home":
        if (open) {
          event.preventDefault();
          setHighlighted(0);
        }
        break;
      case "End":
        if (open) {
          event.preventDefault();
          setHighlighted(Math.max(options.length - 1, 0));
        }
        break;
      default:
        break;
    }
  };

  return (
    <div
      className="ui-select"
      style={style}
      ref={rootRef}
      onKeyDown={onKeyDown}
    >
      <button
        ref={triggerRef}
        type="button"
        className="ui-select-trigger"
        aria-label={ariaLabel}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        disabled={disabled}
        onClick={() => {
          if (open) setOpen(false);
          else openMenu();
        }}
      >
        <span className="label">{current?.label ?? "—"}</span>
        <Glyph
          icon={ChevronDown}
          size={12}
          strokeWidth={2}
          className="caret"
          style={{ transform: open ? "rotate(180deg)" : undefined }}
        />
      </button>
      {open && menuAnchor ? (
        <div
          className="ui-select-menu"
          ref={menuRef}
          role="listbox"
          id={listId}
          aria-label={ariaLabel}
          style={{
            left: menuAnchor.left,
            top: menuAnchor.top,
            bottom: menuAnchor.bottom,
            minWidth: menuAnchor.width,
            maxHeight: menuAnchor.maxHeight,
          }}
        >
          {options.map((option, index) => (
            <button
              type="button"
              key={option.value}
              role="option"
              aria-selected={option.value === value}
              className={
                "ui-select-option" +
                (index === highlighted ? " highlighted" : "") +
                (option.value === value ? " selected" : "")
              }
              onMouseEnter={() => setHighlighted(index)}
              onClick={() => commit(index)}
            >
              {option.label}
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}
