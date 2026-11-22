import { useEffect, useRef, useState } from "react";
import { TURNSTILE_LOGIN_ACTION } from "../turnstile.js";
import { useTheme } from "../theme.js";

interface TurnstileRenderOptions {
  sitekey: string;
  action: string;
  theme: "light" | "dark" | "auto";
  language: string;
  callback: (token: string) => void;
  "expired-callback": () => void;
  "error-callback": () => void;
}

interface TurnstileApi {
  render: (container: HTMLElement, options: TurnstileRenderOptions) => string;
  reset: (widgetId?: string) => void;
  remove: (widgetId?: string) => void;
}

declare global {
  interface Window {
    turnstile?: TurnstileApi;
  }
}

const TURNSTILE_SCRIPT_SRC =
  "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";

let scriptPromise: Promise<TurnstileApi> | null = null;

function loadTurnstile(): Promise<TurnstileApi> {
  if (typeof window === "undefined") {
    return Promise.reject(new Error("Turnstile 只能在浏览器中加载"));
  }
  if (window.turnstile) return Promise.resolve(window.turnstile);
  if (scriptPromise) return scriptPromise;

  scriptPromise = new Promise<TurnstileApi>((resolve, reject) => {
    const existing = document.querySelector<HTMLScriptElement>(
      "script[data-shucode-turnstile]",
    );
    const script = existing ?? document.createElement("script");
    const cleanupListeners = (): void => {
      script.removeEventListener("load", finish);
      script.removeEventListener("error", fail);
    };
    const finish = (): void => {
      cleanupListeners();
      if (window.turnstile) {
        resolve(window.turnstile);
        return;
      }
      script.remove();
      scriptPromise = null;
      reject(new Error("Turnstile 未初始化"));
    };
    const fail = (): void => {
      cleanupListeners();
      // 失败节点会让重建组件等待已经结束的事件；移除后，下次加载才会真正重试。
      script.remove();
      scriptPromise = null;
      reject(new Error("Turnstile 脚本加载失败"));
    };
    script.addEventListener("load", finish, { once: true });
    script.addEventListener("error", fail, { once: true });
    if (existing) return;
    script.src = TURNSTILE_SCRIPT_SRC;
    script.async = true;
    script.defer = true;
    script.dataset.shucodeTurnstile = "1";
    document.head.appendChild(script);
  });
  return scriptPromise;
}

/**
 * 登录页人机验证。siteKey 为空时不应渲染；resetKey 变化（例如登录失败后）
 * 会重置挑战并清空 token。
 */
export function Turnstile({
  siteKey,
  onToken,
  resetKey = 0,
}: {
  siteKey: string;
  onToken: (token: string | null) => void;
  resetKey?: number;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const widgetIdRef = useRef<string | null>(null);
  const onTokenRef = useRef(onToken);
  const [failed, setFailed] = useState(false);
  const { resolved: theme } = useTheme();

  useEffect(() => {
    onTokenRef.current = onToken;
  }, [onToken]);

  useEffect(() => {
    let cancelled = false;
    const container = containerRef.current;
    if (!container) return;
    setFailed(false);
    // 主题切换会重建验证组件，旧挑战的 token 不能继续用于登录。
    onTokenRef.current(null);
    void loadTurnstile()
      .then((api) => {
        if (cancelled || !container.isConnected) return;
        widgetIdRef.current = api.render(container, {
          sitekey: siteKey,
          action: TURNSTILE_LOGIN_ACTION,
          theme,
          language: "zh-cn",
          callback: (token) => { if (!cancelled) onTokenRef.current(token); },
          "expired-callback": () => { if (!cancelled) onTokenRef.current(null); },
          "error-callback": () => {
            if (cancelled) return;
            onTokenRef.current(null);
            setFailed(true);
          },
        });
      })
      .catch(() => {
        if (!cancelled) setFailed(true);
      });
    return () => {
      cancelled = true;
      if (widgetIdRef.current) {
        window.turnstile?.remove(widgetIdRef.current);
        widgetIdRef.current = null;
      }
    };
  }, [siteKey, theme]);

  useEffect(() => {
    if (!widgetIdRef.current) return;
    window.turnstile?.reset(widgetIdRef.current);
    onTokenRef.current(null);
    setFailed(false);
  }, [resetKey]);

  return (
    <div className="turnstile-slot">
      <div ref={containerRef} />
      {failed ? (
        <p className="turnstile-error">人机验证加载失败，请刷新页面重试。</p>
      ) : null}
    </div>
  );
}
