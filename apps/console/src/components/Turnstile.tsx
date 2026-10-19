import { useEffect, useRef, useState } from "react";
import { TURNSTILE_LOGIN_ACTION } from "../turnstile.js";

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
    const finish = (): void => {
      if (window.turnstile) {
        resolve(window.turnstile);
        return;
      }
      scriptPromise = null;
      reject(new Error("Turnstile 未初始化"));
    };
    const fail = (): void => {
      scriptPromise = null;
      reject(new Error("Turnstile 脚本加载失败"));
    };
    const existing = document.querySelector<HTMLScriptElement>(
      "script[data-shucode-turnstile]",
    );
    if (existing) {
      existing.addEventListener("load", finish, { once: true });
      existing.addEventListener("error", fail, { once: true });
      return;
    }
    const script = document.createElement("script");
    script.src = TURNSTILE_SCRIPT_SRC;
    script.async = true;
    script.defer = true;
    script.dataset.shucodeTurnstile = "1";
    script.addEventListener("load", finish, { once: true });
    script.addEventListener("error", fail, { once: true });
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

  useEffect(() => {
    onTokenRef.current = onToken;
  }, [onToken]);

  useEffect(() => {
    let cancelled = false;
    const container = containerRef.current;
    if (!container) return;
    setFailed(false);
    void loadTurnstile()
      .then((api) => {
        if (cancelled || !container.isConnected) return;
        widgetIdRef.current = api.render(container, {
          sitekey: siteKey,
          action: TURNSTILE_LOGIN_ACTION,
          theme: "light",
          language: "zh-cn",
          callback: (token) => onTokenRef.current(token),
          "expired-callback": () => onTokenRef.current(null),
          "error-callback": () => {
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
  }, [siteKey]);

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
