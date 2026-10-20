import { useEffect, useRef, type KeyboardEvent as ReactKeyboardEvent } from "react";

/**
 * 点击编辑单元格：展示态与编辑态同高（.cell-box），表格配合 table-layout: fixed
 * 使用可保证编辑时行/列都不抖动。
 * - 展示态：纯文本，hover 细边框提示可点击；
 * - 编辑态：无边框透明输入框，Enter/失焦提交（值已写入 onChange 草稿），Esc 触发 onRevert 还原。
 * - onCommit 只在 Enter/失焦时触发，Esc 不触发；手动保存按钮仍可独立调用页面保存逻辑。
 */
export function EditableCell({
  value,
  editing,
  num = false,
  ariaLabel,
  onStartEdit,
  onEndEdit,
  onChange,
  onRevert,
  onCommit,
}: {
  value: string;
  editing: boolean;
  num?: boolean;
  ariaLabel: string;
  onStartEdit(): void;
  onEndEdit(): void;
  onChange(next: string): void;
  onRevert(): void;
  onCommit?(): void;
}) {
  const committedRef = useRef(false);
  useEffect(() => {
    if (editing) committedRef.current = false;
  }, [editing]);

  if (!editing) {
    return (
      <div className={"cell-box" + (num ? " num" : "")} title="点击编辑" onClick={onStartEdit}>
        <span>{value}</span>
      </div>
    );
  }

  // Enter 先收起编辑态；收起后输入框卸载可能再触发 blur，committedRef 保证只提交一次。
  const commit = (): void => {
    if (committedRef.current) return;
    committedRef.current = true;
    onCommit?.();
  };
  const onKeyDown = (event: ReactKeyboardEvent<HTMLInputElement>): void => {
    if (event.key === "Enter") {
      onEndEdit();
      commit();
    }
    if (event.key === "Escape") {
      onRevert();
      onEndEdit();
    }
  };
  return (
    <div className={"cell-box editing" + (num ? " num" : "")}>
      <input
        className="cell-input"
        aria-label={ariaLabel}
        autoFocus
        value={value}
        onChange={(event) => onChange(event.target.value)}
        onBlur={() => {
          onEndEdit();
          commit();
        }}
        onKeyDown={onKeyDown}
      />
    </div>
  );
}
