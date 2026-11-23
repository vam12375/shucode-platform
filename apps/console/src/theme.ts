import { useSyncExternalStore } from "react";

export type ThemePreference = "auto" | "system" | "light" | "dark";
export type ResolvedTheme = "light" | "dark";

const STORAGE_KEY = "console-theme";
const SYSTEM_QUERY = "(prefers-color-scheme: dark)";
const listeners = new Set<() => void>();
let media: MediaQueryList;
let scheduleClock: (() => void) | undefined;
let stopCurrent: (() => void) | undefined;
let snapshot: { preference: ThemePreference; resolved: ResolvedTheme } = {
  preference: "auto",
  resolved: "dark",
};

function normalizePreference(value: string | null): ThemePreference {
  return value === "auto" || value === "light" || value === "dark" || value === "system" ? value : "auto";
}

function readPreference(): ThemePreference {
  try {
    return normalizePreference(window.localStorage.getItem(STORAGE_KEY));
  } catch {
    return "auto";
  }
}

function apply(preference: ThemePreference): void {
  // 与控制台时钟一致使用上海墙钟，避免浏览器所在时区改变昼夜切换时间。
  const hour = new Date(Date.now() + 8 * 60 * 60 * 1000).getUTCHours();
  const resolved = preference === "auto"
    ? (hour >= 6 && hour < 18 ? "light" : "dark")
    : preference === "system" ? (media.matches ? "dark" : "light") : preference;
  if (document.documentElement.dataset.theme !== resolved) document.documentElement.dataset.theme = resolved;
  if (document.documentElement.style.colorScheme !== resolved) document.documentElement.style.colorScheme = resolved;
  const changed = snapshot.preference !== preference || snapshot.resolved !== resolved;
  if (changed) snapshot = { preference, resolved };
  scheduleClock?.();
  if (changed) listeners.forEach((listener) => listener());
}

/** 入口统一初始化，覆盖登录、工作台和部署页，昼夜/系统切换无需刷新页面。 */
export function initializeTheme(): () => void {
  stopCurrent?.();
  const systemMedia = window.matchMedia(SYSTEM_QUERY);
  media = systemMedia;
  let timer: number | undefined;
  const reschedule = () => {
    window.clearTimeout(timer);
    timer = undefined;
    if (snapshot.preference !== "auto") return;
    // 对齐整分钟，使 06:00/18:00 的边界准时生效；每分钟也重新读取系统时钟。
    timer = window.setTimeout(() => apply(snapshot.preference), 60_000 - Date.now() % 60_000);
  };
  scheduleClock = reschedule;
  apply(readPreference());
  const onSystemChange = () => apply(snapshot.preference);
  const onResume = () => apply(snapshot.preference);
  const onVisibility = () => { if (!document.hidden) onResume(); };
  const onStorage = (event: StorageEvent) => {
    // 同源其他页面可能使用 sessionStorage；只同步本地主题或 localStorage.clear()。
    if (event.key !== STORAGE_KEY && event.key !== null) return;
    try {
      if (event.storageArea !== window.localStorage) return;
    } catch {
      return;
    }
    apply(normalizePreference(event.newValue));
  };
  systemMedia.addEventListener("change", onSystemChange);
  window.addEventListener("storage", onStorage);
  window.addEventListener("focus", onResume);
  window.addEventListener("pageshow", onResume);
  document.addEventListener("visibilitychange", onVisibility);
  const stop = () => {
    window.clearTimeout(timer);
    timer = undefined;
    systemMedia.removeEventListener("change", onSystemChange);
    window.removeEventListener("storage", onStorage);
    window.removeEventListener("focus", onResume);
    window.removeEventListener("pageshow", onResume);
    document.removeEventListener("visibilitychange", onVisibility);
    if (scheduleClock === reschedule) scheduleClock = undefined;
    if (stopCurrent === stop) stopCurrent = undefined;
  };
  stopCurrent = stop;
  return stop;
}

export function setThemePreference(preference: ThemePreference): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, preference);
  } catch {
    // 存储受限不应阻止当前页面切换主题。
  }
  apply(preference);
}

export function getThemeSnapshot() {
  return snapshot;
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function useTheme() {
  return useSyncExternalStore(subscribe, getThemeSnapshot);
}
