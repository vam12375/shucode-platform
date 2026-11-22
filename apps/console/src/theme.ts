import { useSyncExternalStore } from "react";

export type ThemePreference = "system" | "light" | "dark";
export type ResolvedTheme = "light" | "dark";

const STORAGE_KEY = "console-theme";
const SYSTEM_QUERY = "(prefers-color-scheme: dark)";
const listeners = new Set<() => void>();
let media: MediaQueryList;
let snapshot: { preference: ThemePreference; resolved: ResolvedTheme } = {
  preference: "system",
  resolved: "light",
};

function normalizePreference(value: string | null): ThemePreference {
  return value === "light" || value === "dark" ? value : "system";
}

function readPreference(): ThemePreference {
  try {
    return normalizePreference(window.localStorage.getItem(STORAGE_KEY));
  } catch {
    return "system";
  }
}

function apply(preference: ThemePreference): void {
  const resolved = preference === "system" ? (media.matches ? "dark" : "light") : preference;
  document.documentElement.dataset.theme = resolved;
  document.documentElement.style.colorScheme = resolved;
  if (snapshot.preference === preference && snapshot.resolved === resolved) return;
  snapshot = { preference, resolved };
  listeners.forEach((listener) => listener());
}

/** 入口统一初始化，覆盖登录、工作台和部署页，系统切换无需刷新页面。 */
export function initializeTheme(): () => void {
  media = window.matchMedia(SYSTEM_QUERY);
  apply(readPreference());
  const onSystemChange = () => apply(snapshot.preference);
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
  media.addEventListener("change", onSystemChange);
  window.addEventListener("storage", onStorage);
  return () => {
    media.removeEventListener("change", onSystemChange);
    window.removeEventListener("storage", onStorage);
  };
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
