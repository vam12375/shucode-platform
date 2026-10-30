import { CircleCheck, CircleX, TriangleAlert, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Glyph } from "./Icon.js";

/**
 * 全局操作反馈：Toast（操作成功/失败提示）与确认弹窗（替代系统 confirm / prompt）。
 * 模块级状态 + Host 组件渲染，页面直接 import 函数调用，不经过 Context，不引入状态库。
 * 样式走 styles.css 的「计量台」令牌（--surface / --line / --amber / --signal）。
 */

// ---- Toast ----

export type ToastTone = "ok" | "warn" | "stop";

export interface ToastOptions {
  tone?: ToastTone;
  /** 加粗前缀，如「已保存」。 */
  k?: string;
}

interface ToastItem {
  id: number;
  tone: ToastTone;
  k?: string;
  message: string;
}

type ToastListener = (items: ToastItem[]) => void;

const TOAST_DURATION_MS: Record<ToastTone, number> = { ok: 2600, warn: 4200, stop: 5200 };
const TOAST_MAX = 4;

let toastItems: ToastItem[] = [];
let feedbackSeq = 0;
const toastListeners = new Set<ToastListener>();

function emitToasts(): void {
  for (const listener of toastListeners) listener(toastItems);
}

export function dismissToast(id: number): void {
  toastItems = toastItems.filter((item) => item.id !== id);
  emitToasts();
}

export function showToast(message: string, options: ToastOptions = {}): void {
  const tone = options.tone ?? "ok";
  const item: ToastItem = { id: ++feedbackSeq, tone, k: options.k, message };
  toastItems = [...toastItems.slice(-(TOAST_MAX - 1)), item];
  emitToasts();
  setTimeout(() => dismissToast(item.id), TOAST_DURATION_MS[tone]);
}

export function toastOk(message: string, k?: string): void {
  showToast(message, { tone: "ok", k });
}

export function toastErr(message: string, k = "操作未完成"): void {
  showToast(message, { tone: "stop", k });
}

export function ToastHost() {
  const [items, setItems] = useState<ToastItem[]>([]);
  useEffect(() => {
    const listener: ToastListener = (next) => setItems(next);
    toastListeners.add(listener);
    return () => {
      toastListeners.delete(listener);
    };
  }, []);
  return (
    <div className="toast-stack" role="status" aria-live="polite">
      {items.map((item) => (
        <div
          key={item.id}
          className={item.tone === "ok" ? "toast" : `toast ${item.tone}`}
        >
          <Glyph
            icon={
              item.tone === "ok"
                ? CircleCheck
                : item.tone === "warn"
                  ? TriangleAlert
                  : CircleX
            }
            size={15}
            strokeWidth={2}
          />
          {item.k ? <span className="k">{item.k}</span> : null}
          <span className="msg">{item.message}</span>
          <button
            className="toast-close"
            type="button"
            aria-label="关闭提示"
            onClick={() => dismissToast(item.id)}
          >
            <Glyph icon={X} size={13} strokeWidth={2} />
          </button>
        </div>
      ))}
    </div>
  );
}

// ---- 确认弹窗 ----

export interface ConfirmInput {
  label: string;
  type?: "text" | "password";
  placeholder?: string;
  initial?: string;
  minLength?: number;
}

export interface ConfirmOptions {
  /** 眉标（readout 小字），如「删除模型」。 */
  k?: string;
  title: string;
  message?: string;
  confirmText?: string;
  cancelText?: string;
  danger?: boolean;
  /** 提供后弹窗带一个输入框（替代系统 prompt），value 随结果返回。 */
  input?: ConfirmInput;
}

export interface ConfirmResult {
  confirmed: boolean;
  /** 仅在 confirmed 且 options.input 存在时有意义。 */
  value: string;
}

type DialogState = { options: ConfirmOptions; seq: number };

let dialogState: DialogState | null = null;
let dialogResolve: ((result: ConfirmResult) => void) | null = null;
const dialogListeners = new Set<(state: DialogState | null) => void>();

function settleDialog(result: ConfirmResult): void {
  dialogResolve?.(result);
  dialogResolve = null;
  dialogState = null;
  for (const listener of dialogListeners) listener(null);
}

/**
 * Promise 化确认弹窗：`const r = await confirmDialog({...}); if (!r.confirmed) return;`。
 * 未结算的旧弹窗按取消结算，保证同一时刻只有一个弹窗、不悬挂旧 Promise。
 */
export function confirmDialog(options: ConfirmOptions): Promise<ConfirmResult> {
  dialogResolve?.({ confirmed: false, value: "" });
  return new Promise((resolve) => {
    dialogResolve = resolve;
    dialogState = { options, seq: ++feedbackSeq };
    for (const listener of dialogListeners) listener(dialogState);
  });
}

/** 供其它浮层（如账号详情抽屉）判断是否已有确认弹窗在场，避免 Esc 联动关闭。 */
export function isDialogOpen(): boolean {
  return dialogState !== null;
}

export function DialogHost() {
  const [state, setState] = useState<DialogState | null>(null);
  const [value, setValue] = useState("");
  const [inputError, setInputError] = useState<string | null>(null);
  const confirmRef = useRef<HTMLButtonElement | null>(null);

  useEffect(() => {
    const listener = (next: DialogState | null): void => {
      setState(next);
      if (next) {
        setValue(next.options.input?.initial ?? "");
        setInputError(null);
      }
    };
    dialogListeners.add(listener);
    return () => {
      dialogListeners.delete(listener);
    };
  }, []);

  useEffect(() => {
    // 无输入框时聚焦确认按钮，保证 Enter / 操作键可控。
    if (state && !state.options.input) confirmRef.current?.focus();
  }, [state]);

  if (!state) return null;
  const { options } = state;
  const cancel = (): void => settleDialog({ confirmed: false, value: "" });
  const confirm = (): void => {
    if (options.input) {
      const min = options.input.minLength ?? 0;
      if (value.length < min) {
        setInputError(`至少 ${min} 个字符`);
        return;
      }
    }
    settleDialog({ confirmed: true, value });
  };

  return (
    <div
      className="dialog-backdrop"
      onClick={cancel}
      onKeyDown={(event) => {
        if (event.key === "Escape") cancel();
      }}
    >
      <div
        className={options.danger ? "dialog danger" : "dialog"}
        role="dialog"
        aria-modal="true"
        aria-label={options.title}
        onClick={(event) => event.stopPropagation()}
      >
        {options.k ? <p className="readout dialog-k">{options.k}</p> : null}
        <h3 className="dialog-title">
          {options.danger ? <Glyph icon={TriangleAlert} size={17} strokeWidth={2} /> : null}
          {options.title}
        </h3>
        {options.message ? <p className="dialog-desc">{options.message}</p> : null}
        <form
          onSubmit={(event) => {
            event.preventDefault();
            confirm();
          }}
        >
          {options.input ? (
            <input
              className="input"
              style={{ width: "100%", marginTop: 10 }}
              autoFocus
              type={options.input.type ?? "text"}
              autoComplete="new-password"
              placeholder={options.input.placeholder}
              value={value}
              aria-label={options.input.label}
              onChange={(event) => {
                setValue(event.target.value);
                setInputError(null);
              }}
            />
          ) : null}
          {inputError ? <p className="dialog-err">{inputError}</p> : null}
          <div className="dialog-actions">
            <button className="btn btn-ghost" type="button" onClick={cancel}>
              {options.cancelText ?? "取消"}
            </button>
            <button
              ref={confirmRef}
              className={options.danger ? "btn btn-danger" : "btn"}
              type="submit"
            >
              {options.confirmText ?? "确认"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
