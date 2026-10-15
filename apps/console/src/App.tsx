import { useCallback, useEffect, useState, type ReactNode } from "react";
import { api, navigate } from "./api.js";
import { ConfigPage } from "./pages/ConfigPage.js";
import { DevicesPage } from "./pages/Devices.js";
import { LoginPage } from "./pages/Login.js";
import { ModelsPage } from "./pages/Models.js";
import { ReleasesPage } from "./pages/Releases.js";
import { TeamDetailPage } from "./pages/TeamDetail.js";
import { TeamsPage } from "./pages/Teams.js";

export interface AdminUser {
  id: string;
  email: string;
  displayName: string;
}

const NAV = [
  { path: "/teams", label: "团队" },
  { path: "/releases", label: "内置配置发布" },
  { path: "/models", label: "模型目录" },
  { path: "/devices", label: "设备凭据" },
  { path: "/config", label: "平台配置" },
] as const;

function normalizeHash(hash: string): string {
  const raw = hash.replace(/^#/u, "");
  return raw.startsWith("/") ? raw : "/teams";
}

function splitRoute(route: string): [string, string] {
  const [section = "teams", param = ""] = route.replace(/^\//u, "").split("/");
  return [section, param];
}

export function App() {
  const [user, setUser] = useState<AdminUser | null | undefined>(undefined);
  const [route, setRoute] = useState(() => normalizeHash(window.location.hash));

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

  const logout = useCallback(async () => {
    await api("/auth/logout", { method: "POST" }).catch(() => {});
    setUser(null);
    navigate("/teams");
  }, []);

  if (user === undefined) {
    return <div className="center muted">加载中…</div>;
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
    case "devices":
      page = <DevicesPage />;
      break;
    case "config":
      page = <ConfigPage />;
      break;
    case "teams":
      page = param ? <TeamDetailPage teamId={param} /> : <TeamsPage />;
      break;
    default:
      page = <TeamsPage />;
  }

  return (
    <div className="layout">
      <aside>
        <p className="logo">Mendao Console</p>
        <nav>
          {NAV.map((item) => (
            <a
              key={item.path}
              href={`#${item.path}`}
              className={section === item.path.slice(1) ? "active" : ""}
            >
              {item.label}
            </a>
          ))}
        </nav>
        <p className="muted" style={{ marginTop: 24, paddingLeft: 8, fontSize: 12 }}>
          {user.email}
        </p>
        <button className="secondary" style={{ marginLeft: 8 }} onClick={() => void logout()}>
          退出登录
        </button>
      </aside>
      <main>{page}</main>
    </div>
  );
}
