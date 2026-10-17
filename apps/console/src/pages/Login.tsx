import { useState, type FormEvent } from "react";
import { api } from "../api.js";
import { Gauge } from "../components/Gauge.js";
import type { AdminUser } from "../App.js";

export function LoginPage({ onLogin }: { onLogin: (user: AdminUser) => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const data = await api<{ user: AdminUser }>("/auth/login", {
        method: "POST",
        body: JSON.stringify({ email, password }),
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
          管理员邮箱
        </label>
        <input
          className="input"
          id="lg-email"
          type="email"
          placeholder="管理员邮箱"
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
        {error ? <p className="error">{error}</p> : null}
        <button className="btn" type="submit" disabled={busy}>
          {busy ? "登录中…" : "登录"}
        </button>
        <p className="login-foot">仅限平台管理员 · ADMIN ONLY</p>
      </form>
    </div>
  );
}
