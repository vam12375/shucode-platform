import { useState, type FormEvent } from "react";
import { MailPlus } from "lucide-react";
import { api, navigate } from "../api.js";
import { toastOk } from "../components/Feedback.js";
import { PageTitle } from "../components/Icon.js";
import { Notice } from "../components/StatusChip.js";
import type { ConsoleUser } from "../permissions.js";

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "操作失败";
}

export function InvitePage({
  onAccepted,
  user,
}: {
  onAccepted: () => Promise<void>;
  user?: ConsoleUser;
}) {
  const token =
    new URLSearchParams(window.location.hash.split("?")[1] ?? "").get(
      "token",
    ) ?? "";
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const accept = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api("/invitations/accept", {
        method: "POST",
        body: JSON.stringify({
          token,
          ...(!user ? { email, displayName, password } : {}),
        }),
      });
      sessionStorage.removeItem("console-invitation");
      await onAccepted();
      navigate("/usage");
      toastOk("已加入团队");
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className={user ? "" : "login-page"}>
      <form
        className={user ? "pane pane-b tenant-form" : "login-card"}
        onSubmit={(e) => void accept(e)}
      >
        <p className="eyebrow">MENDAO / 团队邀请</p>
        <PageTitle icon={MailPlus}>接受团队邀请</PageTitle>
        {error ? <Notice tone="stop">{error}</Notice> : null}
        {!token ? (
          <Notice tone="stop">
            邀请链接缺少凭据，请向管理员索取完整链接。
          </Notice>
        ) : user ? (
          <p>以 {user.email} 加入受邀团队。</p>
        ) : (
          <>
            <p className="readout">使用受邀邮箱创建账号。已有账号请先登录。</p>
            <label>
              受邀邮箱
              <input
                className="input"
                type="email"
                required
                autoComplete="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
              />
            </label>
            <label>
              显示名称
              <input
                className="input"
                required
                autoComplete="name"
                value={displayName}
                onChange={(e) => setDisplayName(e.target.value)}
              />
            </label>
            <label>
              设置密码
              <input
                className="input"
                type="password"
                minLength={10}
                required
                autoComplete="new-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </label>
            <a
              href="#/login"
              onClick={() =>
                sessionStorage.setItem("console-invitation", token)
              }
            >
              已有账号，先登录
            </a>
          </>
        )}
        <button className="btn" disabled={busy || !token}>
          {busy ? "接受中…" : "接受邀请"}
        </button>
      </form>
    </div>
  );
}
