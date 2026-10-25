import { Fragment, useCallback, useEffect, useState, type ReactNode } from "react";
import { api, navigate, setApiTeamScope } from "./api.js";
import { AuthProvider } from "./auth.js";
import {
  isTeamManager,
  routeAllowed,
  resolveConsoleSection,
  type ConsoleTeam,
  type ConsoleUser,
  type Membership,
  type SessionData,
} from "./permissions.js";
import { CatalogPage, InvitePage, ProfilePage, TenantPage } from "./pages/Tenant.js";
import { Notice } from "./components/StatusChip.js";
import { DialogHost, ToastHost } from "./components/Feedback.js";
import { MfaSettings } from "./components/MfaSettings.js";
import { ReadoutRail } from "./components/ReadoutRail.js";
import { AuditPage } from "./pages/Audit.js";
import { ConfigPage } from "./pages/ConfigPage.js";
import { DevicesPage } from "./pages/Devices.js";
import { LoginPage } from "./pages/Login.js";
import { ModelsPage } from "./pages/Models.js";
import { ModelDisplayPage } from "./pages/ModelDisplay.js";
import { MediaModelsPage } from "./pages/MediaModels.js";
import { OpsPage } from "./pages/Ops.js";
import { OrgPage } from "./pages/Org.js";
import { QuotaRequestsPage } from "./pages/QuotaRequests.js";
import { PricingPage } from "./pages/Pricing.js";
import { ProvidersPage } from "./pages/Providers.js";
import { ReleasesPage } from "./pages/Releases.js";
import { RootSetupPage } from "./pages/RootSetup.js";
import { TeamDetailPage } from "./pages/TeamDetail.js";
import { UsagePage } from "./pages/Usage.js";
import { UiLibraryPage } from "./pages/UiLibrary.js";

export type AdminUser = ConsoleUser;

const NAV_GROUPS = [
  {
    label: "组织",
    items: [
      { path: "/org", label: "团队与账号" },
      { path: "/audit", label: "审计日志" },
      { path: "/quota-requests", label: "额度申请审批" },
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
      { path: "/media-models", label: "媒体模型" },
      { path: "/model-display", label: "模型展示" },
      { path: "/devices", label: "设备凭据" },
    ],
  },
] as const;

function normalizeHash(hash: string): string {
  const raw = hash.replace(/^#/u, "");
  return raw.startsWith("/") ? raw : "/usage";
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
  return [section.split("?")[0], param.split("?")[0]];
}

export function App() {
  const [user, setUser] = useState<AdminUser | null | undefined>(undefined);
  const [memberships, setMemberships] = useState<Membership[]>([]);
  const [rootTeams, setRootTeams] = useState<ConsoleTeam[]>([]);
  const [teamId, setTeamId] = useState("");
  const [setupRequired, setSetupRequired] = useState(false);
  const [mfaSetupRequired, setMfaSetupRequired] = useState(false);
  const [route, setRoute] = useState(() => normalizeHash(window.location.hash));
  const [ready, setReady] = useState(false);
  useAutoHideScrollbars();

  const receiveSession = useCallback((data: SessionData) => {
    const active = (data.memberships ?? []).filter(
      (item) => !item.memberDisabledAt && !item.team.disabledAt,
    );
    const saved = localStorage.getItem(`console-team:${data.user.id}`) ?? "";
    const next =
      data.user.role === "root"
        ? saved
        : active.some((item) => item.team.id === saved)
          ? saved
          : (active[0]?.team.id ?? "");
    setMemberships(active);
    setTeamId(next);
    setApiTeamScope(next);
    setMfaSetupRequired(data.mfaSetupRequired === true);
    setUser(data.user);
  }, []);
  const refreshSession = useCallback(async () => {
    const session = await api<SessionData>("/auth/me");
    receiveSession(session);
    if (session.user.role === "root" && !session.mfaSetupRequired) {
      const data = await api<{ teams: ConsoleTeam[] }>("/teams");
      setRootTeams(data.teams);
    }
  }, [receiveSession]);
  useEffect(() => {
    refreshSession().catch(async () => {
      setUser(null);
      try {
        const status = await api<{ rootInitialized: boolean }>("/setup/status");
        setSetupRequired(!status.rootInitialized);
      } catch {
        /* 保留普通登录页，允许用户重试。 */
      }
    });
  }, [refreshSession]);

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
    setApiTeamScope("");
    setTeamId("");
    setMemberships([]);
    setMfaSetupRequired(false);
    setUser(null);
    navigate("/usage");
  }, []);

  if (user === undefined) {
    return <div className="center">加载中…</div>;
  }
  if (!user) {
    if (setupRequired) {
      return (
        <RootSetupPage
          onSetup={(next) => {
            receiveSession(next);
            void refreshSession();
            setSetupRequired(false);
          }}
        />
      );
    }
    if (route.startsWith("/invite"))
      return (
        <>
          <ToastHost />
          <DialogHost />
          <InvitePage onAccepted={refreshSession} />
        </>
      );
    return (
      <LoginPage
        onLogin={(next) => {
          receiveSession(next);
          void refreshSession();
          const invitation = sessionStorage.getItem("console-invitation");
          if (invitation) {
            sessionStorage.removeItem("console-invitation");
            navigate(`/invite?token=${encodeURIComponent(invitation)}`);
          }
        }}
      />
    );
  }

  if (mfaSetupRequired) {
    return (
      <AuthProvider
        value={{
          user,
          memberships,
          teams: [],
          teamId: "",
          team: undefined,
          membership: undefined,
          isRoot: true,
          canManageTeam: true,
          selectTeam: () => undefined,
          refreshSession,
        }}
      >
        <ToastHost />
        <DialogHost />
        <div className="login-page">
          <div className="login-card mfa-gate-card">
            <h1 className="login-title">MENDAO</h1>
            <p className="login-sub">超级管理员安全绑定 · MANDATORY MFA</p>
            <p className="readout">
              当前部署已启用强制双重验证：请先绑定身份验证器；完成绑定前，除安全设置外的控制台接口都会被拒绝。
            </p>
            <MfaSettings />
            <button
              className="btn btn-ghost"
              type="button"
              onClick={() => void logout()}
            >
              退出登录
            </button>
          </div>
        </div>
      </AuthProvider>
    );
  }

  const isRoot = user.role === "root";
  const teams = isRoot ? rootTeams : memberships.map((item) => item.team);
  const membership = memberships.find((item) => item.team.id === teamId);
  const team = teams.find((item) => item.id === teamId);
  const canManageTeam = isTeamManager(user, membership);
  const selectTeam = (id: string) => {
    localStorage.setItem(`console-team:${user.id}`, id);
    setApiTeamScope(id);
    setTeamId(id);
  };
  const [requestedSection, param] = splitRoute(route);
  const section = resolveConsoleSection(requestedSection, isRoot, canManageTeam);
  const allowed = routeAllowed(section, isRoot, canManageTeam);
  const navGroups = isRoot
    ? [...NAV_GROUPS]
    : [
        {
          label: canManageTeam ? "团队工作台" : "我的空间",
          items: [
            ...(canManageTeam
              ? [
                  { path: "/team", label: "团队概览" },
                  { path: "/members", label: "成员管理" },
                  { path: "/policy", label: "预算与限制" },
                  { path: "/quota-requests", label: "额度申请" },
                  { path: "/audit", label: "团队审计" },
                  { path: "/team-settings", label: "团队设置" },
                ]
              : []),
            {
              path: "/usage",
              label: canManageTeam ? "用量与消费" : "我的用量",
            },
            { path: "/catalog", label: "可用模型" },
            { path: "/devices", label: "本人设备" },
            { path: "/profile", label: "个人设置" },
          ],
        },
      ];
  let page: ReactNode;
  switch (section) {
    case "team":
    case "members":
    case "policy":
    case "team-settings":
      page = <TenantPage section={section} />;
      break;
    case "quota-requests":
      page = <QuotaRequestsPage />;
      break;
    case "catalog":
      page = <CatalogPage />;
      break;
    case "profile":
      page = <ProfilePage />;
      break;
    case "invite":
      page = <InvitePage onAccepted={refreshSession} user={user} />;
      break;
    case "releases":
      page = <ReleasesPage />;
      break;
    case "models":
      page = <ModelsPage />;
      break;
    case "model-display":
      page = <ModelDisplayPage />;
      break;
    case "media-models":
      page = <MediaModelsPage />;
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
    case "org":
    case "users":
      // 「用户」已并入「团队与账号」；旧书签 #/users 直接落到合并页。
      page = <OrgPage currentUserId={user.id} />;
      break;
    case "audit":
      page = <AuditPage />;
      break;
    case "teams":
      page = param ? <TeamDetailPage teamId={param} /> : <OrgPage currentUserId={user.id} />;
      break;
    default:
      page = isRoot ? <OrgPage currentUserId={user.id} /> : <UsagePage />;
  }

  if (
    !isRoot &&
    !teamId &&
    ["team", "usage", "members", "policy", "team-settings", "audit"].includes(section)
  )
    page = (
      <Notice k="尚未加入团队">
        请联系团队管理员获取邀请链接。你可以先管理本人设备和个人资料。
      </Notice>
    );
  if (!allowed)
    page = (
      <Notice tone="stop" k="无权访问">
        此页面需要相应管理权限。<a href="#/usage">返回我的用量</a>
      </Notice>
    );
  return (
    <AuthProvider
      value={{
        user,
        memberships,
        teams,
        teamId,
        team,
        membership,
        isRoot,
        canManageTeam,
        selectTeam,
        refreshSession,
      }}
    >
      <div className={ready ? "app ready" : "app"}>
        <ToastHost />
        <DialogHost />
        <ReadoutRail key={`${user.id}:${teamId}`} />
        <aside className="side">
          <div className="brand">
            <div className="brand-name">MENDAO</div>
            <div className="brand-sub">计量控制台 · METERING</div>
          </div>
          <nav aria-label="控制台导航">
            {navGroups.map((group) => (
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
          {isRoot ? (
            <div className="side-ref">
              <p className="nav-group">参考</p>
              <a href="#/ui" className={section === "ui" ? "nav-item active" : "nav-item"}>
                UI 组件库
              </a>
            </div>
          ) : null}
          <div className="side-foot">
            {isRoot ? (
              <a href="#/profile" className="nav-item">
                个人设置
              </a>
            ) : null}
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
                    {isRoot ? "超级管理员 · " : canManageTeam ? "团队管理员 · " : "成员 · "}
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
          <div className="wrap" key={`${user.id}:${teamId}:${section}`}>
            {page}
          </div>
        </main>
      </div>
    </AuthProvider>
  );
}
