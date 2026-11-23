import { Component, lazy, Suspense, useCallback, useEffect, useState, type ReactNode } from "react";
import { api, navigate } from "./api.js";
import { LoginPage } from "./pages/Login.js";
import { InvitePage } from "./pages/Invite.js";
import { DialogHost, ToastHost } from "./components/Feedback.js";
import { PolarAuthBrand, PolarAuthFrame } from "./components/PolarAuth.js";
import type { SessionData } from "./permissions.js";

// 登录前只交付认证入口；动态模块的文件权限由构建清单与服务端共同执行。
const Workspace = lazy(() => import("./App.js").then((module) => ({ default: module.App })));
const Setup = lazy(() => import("./pages/RootSetup.js"));
const reload = () => window.location.reload();

class LoadBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() { return { failed: true }; }
  render() {
    return this.state.failed
      ? <div className="center"><p>页面加载失败或会话已失效。</p><button className="btn" onClick={reload}>重新加载</button></div>
      : this.props.children;
  }
}

export function Entry() {
  const [session, setSession] = useState<SessionData | null | undefined>(undefined);
  const [route, setRoute] = useState(window.location.hash);
  const [setupAccess, setSetupAccess] = useState(false);
  const [credential, setCredential] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const refreshSession = useCallback(async () => setSession(await api<SessionData>("/auth/me")), []);
  useEffect(() => {
    void refreshSession().catch(() => setSession(null));
    const onHash = () => setRoute(window.location.hash);
    // 浏览器后退缓存可能恢复已退出的工作台，恢复时重新核验会话。
    const onPageShow = (event: PageTransitionEvent) => { if (event.persisted) reload(); };
    window.addEventListener("hashchange", onHash);
    window.addEventListener("pageshow", onPageShow);
    return () => {
      window.removeEventListener("hashchange", onHash);
      window.removeEventListener("pageshow", onPageShow);
    };
  }, [refreshSession]);
  useEffect(() => {
    if (!session) return;
    window.addEventListener("console-session-expired", reload);
    return () => window.removeEventListener("console-session-expired", reload);
  }, [session]);
  const onLogin = (next: SessionData) => {
    setSession(next);
    const invitation = sessionStorage.getItem("console-invitation");
    if (invitation) {
      sessionStorage.removeItem("console-invitation");
      navigate(`/invite?token=${encodeURIComponent(invitation)}`);
    }
  };
  let content: ReactNode;
  if (session === undefined) content = <div className="center">加载中…</div>;
  else if (session) content = <Workspace session={session} onSignedOut={reload} />;
  else if (route === "#/setup") content = setupAccess ? <Setup onSetup={onLogin} /> : (
    <PolarAuthFrame><form className="login-card" onSubmit={async (event) => {
      event.preventDefault();
      setBusy(true); setError("");
      try {
        await api("/setup/access", { method: "POST", headers: { "x-bootstrap-secret": credential } });
        setCredential(""); setSetupAccess(true);
      } catch { setError("无法访问，请核对凭据或联系部署人员。"); }
      finally { setBusy(false); }
    }}>
      <PolarAuthBrand />
      <h1 className="login-title">验证部署凭据</h1>
      <p className="login-sub">使用部署时配置的凭据继续</p>
      <label className="polar-auth-field">部署凭据<input className="input" type="password" autoComplete="off" value={credential} onChange={(event) => setCredential(event.target.value)} /></label>
      {error ? <p className="error">{error}</p> : null}
      <button className="btn polar-auth-submit" disabled={busy}>{busy ? "验证中…" : "继续"}</button>
    </form></PolarAuthFrame>
  );
  else if (route.startsWith("#/invite")) content = <><ToastHost /><DialogHost /><InvitePage onAccepted={refreshSession} /></>;
  else content = <LoginPage onLogin={onLogin} />;
  return <>
    <LoadBoundary><Suspense fallback={<div className="center">加载中…</div>}>{content}</Suspense></LoadBoundary>
  </>;
}
