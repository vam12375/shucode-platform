import { useState, type FormEvent } from "react";
import { api } from "../api.js";
import type { SessionData } from "../permissions.js";

/**
 * 首次初始化系统管理员（root）。
 * 只在数据库里没有 root 时由服务端放行；口令经 scrypt 哈希入库，不写入 .env。
 */
export default function SetupPage({ onSetup }: { onSetup: (data: SessionData) => void }) {
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    if (password !== confirm) {
      setError("两次输入的密码不一致");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const data = await api<SessionData>("/setup/root", {
        method: "POST",
        body: JSON.stringify({ email, displayName, password }),
      });
      onSetup(data);
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : "初始化失败");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-page">
      <form className="login-card" onSubmit={(event) => void submit(event)}>
        <h1 className="login-title">MENDAO</h1>
        <p className="login-sub">初始化系统管理</p>
        <p className="readout" style={{ margin: "0 0 14px" }}>
          请设置系统管理账号。
        </p>
        <label className="sr-only" htmlFor="setup-email">
          系统管理员邮箱
        </label>
        <input
          className="input"
          id="setup-email"
          type="email"
          placeholder="系统管理员邮箱"
          autoComplete="username"
          value={email}
          onChange={(event) => setEmail(event.target.value)}
          required
        />
        <label className="sr-only" htmlFor="setup-name">
          显示名
        </label>
        <input
          className="input"
          id="setup-name"
          placeholder="显示名"
          maxLength={80}
          value={displayName}
          onChange={(event) => setDisplayName(event.target.value)}
          required
        />
        <label className="sr-only" htmlFor="setup-pass">
          密码
        </label>
        <input
          className="input"
          id="setup-pass"
          type="password"
          placeholder="密码（至少 12 位）"
          autoComplete="new-password"
          minLength={12}
          value={password}
          onChange={(event) => setPassword(event.target.value)}
          required
        />
        <label className="sr-only" htmlFor="setup-confirm">
          确认密码
        </label>
        <input
          className="input"
          id="setup-confirm"
          type="password"
          placeholder="再次输入密码"
          autoComplete="new-password"
          minLength={12}
          value={confirm}
          onChange={(event) => setConfirm(event.target.value)}
          required
        />
        {error ? <p className="error">{error}</p> : null}
        <button className="btn" type="submit" disabled={busy}>
          {busy ? "创建中…" : "创建系统管理员"}
        </button>
        <p className="login-foot">仅可初始化一次 · 密码只以哈希存入数据库</p>
      </form>
    </div>
  );
}
