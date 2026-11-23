import { useState, type FormEvent } from "react";
import { api } from "../api.js";
import { ArrowRight, CircleAlert } from "lucide-react";
import { Glyph } from "../components/Icon.js";
import { PolarAuthBrand, PolarAuthFrame } from "../components/PolarAuth.js";
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
    <PolarAuthFrame>
      <form className="login-card" onSubmit={(event) => void submit(event)}>
        <PolarAuthBrand />
        <h1 className="login-title">创建管理员</h1>
        <p className="login-sub">设置首个系统管理账号</p>
        <label className="polar-auth-field" htmlFor="setup-email">
          <span>系统管理员邮箱</span>
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
        </label>
        <label className="polar-auth-field" htmlFor="setup-name">
          <span>显示名称</span>
        <input
          className="input"
          id="setup-name"
          placeholder="显示名"
          maxLength={80}
          value={displayName}
          onChange={(event) => setDisplayName(event.target.value)}
          required
        />
        </label>
        <label className="polar-auth-field" htmlFor="setup-pass">
          <span>口令</span>
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
        </label>
        <label className="polar-auth-field" htmlFor="setup-confirm">
          <span>确认口令</span>
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
        </label>
        {error ? <p className="polar-auth-error" role="alert"><Glyph icon={CircleAlert} size={15} /><span>{error}</span></p> : null}
        <button className="btn polar-auth-submit" type="submit" disabled={busy}>
          {busy ? "创建中…" : <>创建系统管理员<Glyph icon={ArrowRight} size={15} /></>}
        </button>
        <p className="login-foot">首次使用 · 创建完成后进入控制台</p>
      </form>
    </PolarAuthFrame>
  );
}
