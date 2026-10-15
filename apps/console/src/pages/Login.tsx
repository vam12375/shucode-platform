import { useState, type FormEvent } from "react";
import { api } from "../api.js";
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
    <div className="center">
      <form className="login-card" onSubmit={(event) => void submit(event)}>
        <p className="muted" style={{ letterSpacing: "0.14em", textTransform: "uppercase", fontSize: 12 }}>
          Mendao Console
        </p>
        <h1>控制台登录</h1>
        <input
          type="email"
          placeholder="管理员邮箱"
          autoComplete="username"
          value={email}
          onChange={(event) => setEmail(event.target.value)}
          required
        />
        <input
          type="password"
          placeholder="密码"
          autoComplete="current-password"
          value={password}
          onChange={(event) => setPassword(event.target.value)}
          required
        />
        {error ? <p className="error">{error}</p> : null}
        <button type="submit" disabled={busy}>
          {busy ? "登录中…" : "登录"}
        </button>
      </form>
    </div>
  );
}
