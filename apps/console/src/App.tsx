import { Fragment, useCallback, useEffect, useState, lazy, Suspense, type ReactNode } from "react";
import { Boxes, Building2, ChartColumn, HandCoins, LogOut, ScrollText, Settings2, ShieldCheck, Smartphone, UserCog, Users } from "lucide-react";
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
import { CatalogPage, ProfilePage, TenantPage } from "./pages/Tenant.js";
import { Notice } from "./components/StatusChip.js";
import { DialogHost, ToastHost } from "./components/Feedback.js";
import { MfaSettings } from "./components/MfaSettings.js";
import { Glyph } from "./components/Icon.js";
import { ReadoutRail } from "./components/ReadoutRail.js";
import { AuditPage } from "./pages/Audit.js";
import { DevicesPage } from "./pages/Devices.js";
import { InvitePage } from "./pages/Invite.js";
import type { NavGroup } from "./navigation.js";

import { PaymentsPage } from "./pages/Payments.js";
import { QuotaRequestsPage } from "./pages/QuotaRequests.js";
import { UsagePage } from "./pages/Usage.js";

// 页面隐藏无法阻止下载；独立模块由服务端按 root 会话授权交付。
const RootConsole = lazy(() => import("./RootConsole.js"));

export type AdminUser = ConsoleUser;

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

export function App({ session, onSignedOut }: { session: SessionData; onSignedOut: () => void }) {
  const [user, setUser] = useState<AdminUser>(session.user);
  const [memberships, setMemberships] = useState<Membership[]>([]);
  const [platformTeams, setPlatformTeams] = useState<ConsoleTeam[]>([]);
  const [teamId, setTeamId] = useState("");
  const [mfaSetupRequired, setMfaSetupRequired] = useState(session.mfaSetupRequired === true);
  const [route, setRoute] = useState(() => normalizeHash(window.location.hash));
  const [ready, setReady] = useState(false);
  useAutoHideScrollbars();

  const receiveSession = useCallback((data: SessionData) => {
    const active = (data.memberships ?? []).filter(
      (item) => !item.memberDisabledAt && !item.team.disabledAt,
    );
    const saved = localStorage.getItem(`console-team:${data.user.id}`) ?? "";
    const next =
      data.user.platformAccess === true
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
    if (session.user.platformAccess === true && !session.mfaSetupRequired) {
      const data = await api<{ teams: ConsoleTeam[] }>("/teams");
      setPlatformTeams(data.teams);
    }
  }, [receiveSession]);
  useEffect(() => {
    receiveSession(session);
    void refreshSession().catch(onSignedOut);
  }, [refreshSession, receiveSession, session, onSignedOut]);

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
    onSignedOut();
    navigate("/usage");
  }, [onSignedOut]);

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
          canManagePlatform: true,
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
            <p className="login-sub">系统管理员安全绑定 · MANDATORY MFA</p>
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

  const canManagePlatform = user.platformAccess === true;
  const teams = canManagePlatform ? platformTeams : memberships.map((item) => item.team);
  const membership = memberships.find((item) => item.team.id === teamId);
  const team = teams.find((item) => item.id === teamId);
  const canManageTeam = isTeamManager(user, membership);
  const selectTeam = (id: string) => {
    localStorage.setItem(`console-team:${user.id}`, id);
    setApiTeamScope(id);
    setTeamId(id);
  };
  const [requestedSection, param] = splitRoute(route);
  const section = resolveConsoleSection(requestedSection, canManagePlatform, canManageTeam);
  const allowed = routeAllowed(section, canManagePlatform, canManageTeam);
  const navGroups: NavGroup[] = [
        {
          label: canManageTeam ? "团队工作台" : "我的空间",
          items: [
            ...(canManageTeam
              ? [
                  { path: "/team", label: "团队概览", icon: Building2 },
                  { path: "/members", label: "成员管理", icon: Users },
                  { path: "/policy", label: "预算与限制", icon: ShieldCheck },
                  { path: "/quota-requests", label: "额度申请", icon: HandCoins },
                  { path: "/audit", label: "团队审计", icon: ScrollText },
                  { path: "/team-settings", label: "团队设置", icon: Settings2 },
                ]
              : []),
            {
              path: "/usage",
              label: canManageTeam ? "用量与消费" : "我的用量",
              icon: ChartColumn,
            },
            { path: "/catalog", label: "可用模型", icon: Boxes },
            { path: "/payments", label: "充值与余额", icon: HandCoins },
            { path: "/devices", label: "本人设备", icon: Smartphone },
            { path: "/profile", label: "个人设置", icon: UserCog },
          ],
        },
      ];
  let page: ReactNode;
  switch (section) {
    case "payments":
      page = <PaymentsPage />;
      break;
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
    case "devices":
      page = <DevicesPage />;
      break;
    case "usage":
      page = <UsagePage />;
      break;
    case "audit":
      page = <AuditPage />;
      break;
    default:
      page = canManagePlatform ? null : <UsagePage />;
  }

  if (
    !canManagePlatform &&
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
  const renderShell = (groups: NavGroup[], content: ReactNode, referenceNav: ReactNode = null) => (
    <AuthProvider
      value={{
        user,
        memberships,
        teams,
        teamId,
        team,
        membership,
        canManagePlatform,
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
            {groups.map((group) => (
              <Fragment key={group.label}>
                <p className="nav-group">{group.label}</p>
                {group.items.map((item) => (
                  <a
                    key={item.path}
                    href={`#${item.path}`}
                    className={section === item.path.slice(1) ? "nav-item active" : "nav-item"}
                  >
                    <Glyph icon={item.icon} />
                    <span>{item.label}</span>
                  </a>
                ))}
              </Fragment>
            ))}
          </nav>
          {referenceNav}
          <div className="side-foot">
            {canManagePlatform ? (
              <a href="#/profile" className="nav-item">
                <Glyph icon={UserCog} />
                <span>个人设置</span>
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
                    {canManagePlatform ? "系统管理员 · " : canManageTeam ? "团队管理员 · " : "成员 · "}
                    {user.email}
                  </span>
                </div>
              </div>
              <button className="logout" type="button" onClick={() => void logout()}>
                <Glyph icon={LogOut} size={13} strokeWidth={2} />
                退出登录
              </button>
            </div>
          </div>
        </aside>
        <main className="main">
          <div className="wrap" key={`${user.id}:${teamId}:${section}`}>
            {content}
          </div>
        </main>
      </div>
    </AuthProvider>
  );
  return (
    <Suspense fallback={<div className="center">加载中…</div>}>
      {canManagePlatform ? (
        <RootConsole section={section} param={param} userId={user.id} page={page}>
          {renderShell}
        </RootConsole>
      ) : renderShell(navGroups, page)}
    </Suspense>
  );
}
