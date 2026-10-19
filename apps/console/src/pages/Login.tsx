import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "../api.js";
import { Gauge } from "../components/Gauge.js";
import { Turnstile } from "../components/Turnstile.js";
import { normalizeTurnstileSiteKey, turnstileRequired } from "../turnstile.js";
import type { AdminUser } from "../App.js";

interface PublicConfig {
  turnstileSiteKey?: unknown;
  turnstileEnabled?: unknown;
}

export function LoginPage({ onLogin }: { onLogin: (user: AdminUser) => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [recoveryCode, setRecoveryCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [configReady, setConfigReady] = useState(false);
  const [turnstileSiteKey, setTurnstileSiteKey] = useState("");
  const [turnstileEnabled, setTurnstileEnabled] = useState(false);
  const [turnstileToken, setTurnstileToken] = useState<string | null>(null);
  const [turnstileResetKey, setTurnstileResetKey] = useState(0);

  useEffect(() => {
    let cancelled = false;
    void api<PublicConfig>("/public-config")
      .then((config) => {
        if (cancelled) return;
        const siteKey = normalizeTurnstileSiteKey(config?.turnstileSiteKey);
        setTurnstileSiteKey(siteKey);
        setTurnstileEnabled(config?.turnstileEnabled === true && turnstileRequired(siteKey));
      })
      .catch(() => {
        // 公开配置读取失败时按未启用渲染；服务端仍会拒绝缺少 token 的登录。
      })
      .finally(() => {
        if (!cancelled) setConfigReady(true);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const handleTurnstileToken = useCallback((token: string | null) => {
    setTurnstileToken(token);
  }, []);

  const resetTurnstile = useCallback(() => {
    setTurnstileToken(null);
    setTurnstileResetKey((value) => value + 1);
  }, []);

  const submit = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    if (!configReady) {
      setError("安全验证加载中，请稍候");
      return;
    }
    if (turnstileEnabled && !turnstileToken) {
      setError("请先完成人机验证");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const data = await api<{ user: AdminUser }>("/auth/login", {
        method: "POST",
        body: JSON.stringify({
          email,
          password,
          ...(turnstileEnabled && turnstileToken
            ? { turnstileToken }
            : {}),
          ...(code.trim() ? { code: code.trim() } : {}),
          ...(recoveryCode.trim() ? { recoveryCode: recoveryCode.trim() } : {}),
        }),
      });
      onLogin(data.user);
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : "登录失败");
      resetTurnstile();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-page">
      <form className="login-card" onSubmit={(event) => void submit(event)}>
        <h1 className="login-title">MENDAO</h1>
        <p className="login-sub">计量控制台 · METERING CONSOLE</p>
        <div className="login-gauge">
          <Gauge used={62} quota={100} bare />
        </div>
        <label className="sr-only" htmlFor="lg-email">
          账号邮箱
        </label>
        <input
          className="input"
          id="lg-email"
          type="email"
          placeholder="账号邮箱"
          autoComplete="username"
          value={email}
          onChange={(event) => setEmail(event.target.value)}
          required
        />
        <label className="sr-only" htmlFor="lg-pass">
          密码
        </label>
        <input
          className="input"
          id="lg-pass"
          type="password"
          placeholder="密码"
          autoComplete="current-password"
          value={password}
          onChange={(event) => setPassword(event.target.value)}
          required
        />
        {turnstileEnabled ? (
          <Turnstile
            siteKey={turnstileSiteKey}
            onToken={handleTurnstileToken}
            resetKey={turnstileResetKey}
          />
        ) : null}
        {!configReady ? (
          <p className="turnstile-loading">正在加载安全验证…</p>
        ) : null}
        <details className="mfa-login">
          <summary>双重验证（已启用时填写）</summary>
          <label>
            动态验证码
            <input
              className="input"
              inputMode="numeric"
              autoComplete="one-time-code"
              maxLength={6}
              value={code}
              onChange={(event) => {
                setCode(event.target.value);
                setRecoveryCode("");
              }}
            />
          </label>
          <label>
            或使用恢复码
            <input
              className="input"
              autoComplete="off"
              value={recoveryCode}
              onChange={(event) => {
                setRecoveryCode(event.target.value);
                setCode("");
              }}
            />
          </label>
        </details>
        {error ? <p className="error">{error}</p> : null}
        <button
          className="btn"
          type="submit"
          disabled={busy || !configReady || (turnstileEnabled && !turnstileToken)}
        >
          {busy ? "登录中…" : "登录"}
        </button>
        <p className="login-foot">团队与个人工作台 · SECURE ACCESS</p>
      </form>
    </div>
  );
}
