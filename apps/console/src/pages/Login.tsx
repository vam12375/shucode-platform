import { useState, type FormEvent } from "react";
import { api } from "../api.js";
import { Gauge } from "../components/Gauge.js";
import type { AdminUser } from "../App.js";

export function LoginPage({ onLogin }: { onLogin: (user: AdminUser) => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [recoveryCode, setRecoveryCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const data = await api<{ user: AdminUser }>("/auth/login", {
        method: "POST",
        body: JSON.stringify({
          email,
          password,
          ...(code.trim() ? { code: code.trim() } : {}),
          ...(recoveryCode.trim() ? { recoveryCode: recoveryCode.trim() } : {}),
        }),
      });
      onLogin(data.user);
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : "登录失败");
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
        <button className="btn" type="submit" disabled={busy}>
          {busy ? "登录中…" : "登录"}
        </button>
        <p className="login-foot">团队与个人工作台 · SECURE ACCESS</p>
      </form>
    </div>
  );
}
