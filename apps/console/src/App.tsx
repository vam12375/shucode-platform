import { Fragment, useCallback, useEffect, useState, type ReactNode } from "react";
import { api, navigate } from "./api.js";
import { DialogHost, ToastHost } from "./components/Feedback.js";
import { ReadoutRail } from "./components/ReadoutRail.js";
import { AuditPage } from "./pages/Audit.js";
import { ConfigPage } from "./pages/ConfigPage.js";
import { DevicesPage } from "./pages/Devices.js";
import { LoginPage } from "./pages/Login.js";
import { ModelsPage } from "./pages/Models.js";
import { OpsPage } from "./pages/Ops.js";
import { PricingPage } from "./pages/Pricing.js";
import { ProvidersPage } from "./pages/Providers.js";
import { ReleasesPage } from "./pages/Releases.js";
import { TeamDetailPage } from "./pages/TeamDetail.js";
import { TeamsPage } from "./pages/Teams.js";
import { UsagePage } from "./pages/Usage.js";
import { UiLibraryPage } from "./pages/UiLibrary.js";
import { UsersPage } from "./pages/Users.js";

export interface AdminUser {
  id: string;
  email: string;
  displayName: string;
  role?: "admin" | "member";
}

const NAV_GROUPS = [
  {
    label: "组织",
    items: [
      { path: "/teams", label: "团队" },
      { path: "/users", label: "用户" },
      { path: "/audit", label: "审计日志" },
    ],
  },
  {
    label: "计量",
    items: [
      { path: "/usage", label: "用量" },
      { path: "/ops", label: "运维大盘" },
    ],
  },
  {
    label: "下发",
    items: [
      { path: "/releases", label: "内置配置发布" },
      { path: "/models", label: "模型目录" },
      { path: "/pricing", label: "模型定价库" },
      { path: "/config", label: "平台配置" },
    ],
  },
  {
    label: "接入",
    items: [
      { path: "/providers", label: "上游供应商" },
      { path: "/devices", label: "设备凭据" },
    ],
  },
] as const;

function normalizeHash(hash: string): string {
  const raw = hash.replace(/^#/u, "");
  return raw.startsWith("/") ? raw : "/teams";
}

/**
 * 滚动条自动隐现：任何内部滚动容器（主区/侧栏/长表/下拉菜单）滚动时加
 * `.is-scrolling`（Webkit/Firefox 滚动条随之浮现，轨道本身透明），
 * 静止 ~1s 后移除。捕获级监听覆盖所有滚动容器。
 */
function useAutoHideScrollbars(): void {
  useEffect(() => {
    const timers = new WeakMap<Element, number>();
    const onScroll = (event: Event): void => {
      const target = event.target;
      if (!(target instanceof Element)) return;
      target.classList.add("is-scrolling");
      const pending = timers.get(target);
      if (pending !== undefined) window.clearTimeout(pending);
      timers.set(
        target,
        window.setTimeout(() => target.classList.remove("is-scrolling"), 900),
      );
    };
    document.addEventListener("scroll", onScroll, true);
    return () => document.removeEventListener("scroll", onScroll, true);
  }, []);
}

function splitRoute(route: string): [string, string] {
  const [section = "teams", param = ""] = route.replace(/^\//u, "").split("/");
  return [section, param];
}

export function App() {
  const [user, setUser] = useState<AdminUser | null | undefined>(undefined);
  const [route, setRoute] = useState(() => normalizeHash(window.location.hash));
  const [ready, setReady] = useState(false);
  useAutoHideScrollbars();

  useEffect(() => {
    api<{ user: AdminUser }>("/auth/me")
      .then((data) => setUser(data.user))
      .catch(() => setUser(null));
  }, []);

  useEffect(() => {
    const onHashChange = () => setRoute(normalizeHash(window.location.hash));
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);

  useEffect(() => {
    const raf = requestAnimationFrame(() => setReady(true));
    return () => cancelAnimationFrame(raf);
  }, []);

  const logout = useCallback(async () => {
    await api("/auth/logout", { method: "POST" }).catch(() => {});
    setUser(null);
    navigate("/teams");
  }, []);

  if (user === undefined) {
    return <div className="center">加载中…</div>;
  }
  if (!user) {
    return <LoginPage onLogin={setUser} />;
  }

  const [section, param] = splitRoute(route);
  let page: ReactNode;
  switch (section) {
    case "releases":
      page = <ReleasesPage />;
      break;
    case "models":
      page = <ModelsPage />;
      break;
    case "pricing":
      page = <PricingPage />;
      break;
    case "devices":
      page = <DevicesPage />;
      break;
    case "providers":
      page = <ProvidersPage />;
      break;
    case "config":
      page = <ConfigPage />;
      break;
    case "usage":
      page = <UsagePage />;
      break;
    case "ops":
      page = <OpsPage />;
      break;
    case "ui":
      page = <UiLibraryPage />;
      break;
    case "users":
      page = <UsersPage currentUserId={user.id} />;
      break;
    case "audit":
      page = <AuditPage />;
      break;
    case "teams":
      page = param ? <TeamDetailPage teamId={param} /> : <TeamsPage />;
      break;
    default:
      page = <TeamsPage />;
  }

  return (
    <div className={ready ? "app ready" : "app"}>
      <ToastHost />
      <DialogHost />
      <ReadoutRail />
      <aside className="side">
        <div className="brand">
          <div className="brand-name">MENDAO</div>
          <div className="brand-sub">计量控制台 · METERING</div>
        </div>
        <nav aria-label="控制台导航">
          {NAV_GROUPS.map((group) => (
            <Fragment key={group.label}>
              <p className="nav-group">{group.label}</p>
              {group.items.map((item) => (
                <a
                  key={item.path}
                  href={`#${item.path}`}
                  className={section === item.path.slice(1) ? "nav-item active" : "nav-item"}
                >
                  {item.label}
                </a>
              ))}
            </Fragment>
          ))}
        </nav>
        <div className="side-ref">
          <p className="nav-group">参考</p>
          <a href="#/ui" className={section === "ui" ? "nav-item active" : "nav-item"}>
            UI 组件库
          </a>
        </div>
        <div className="side-foot">
          <div className="user-card">
            <div className="user-card-top">
              <div className="user-avatar" aria-hidden="true">
                {user.email.slice(0, 1).toUpperCase()}
              </div>
              <div className="user-meta">
                <span className="user-name" title={user.displayName || user.email}>
                  {user.displayName || user.email}
                </span>
                <span className="user-mail" title={user.email}>
                  {user.role === "admin" ? "管理员 · " : ""}
                  {user.email}
                </span>
              </div>
            </div>
            <button className="logout" type="button" onClick={() => void logout()}>
              <svg
                width="13"
                height="13"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                strokeLinecap="round"
                strokeLinejoin="round"
                aria-hidden="true"
              >
                <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
                <polyline points="16 17 21 12 16 7" />
                <line x1="21" y1="12" x2="9" y2="12" />
              </svg>
              退出登录
            </button>
          </div>
        </div>
      </aside>
      <main className="main">
        <div className="wrap">{page}</div>
      </main>
    </div>
  );
}
