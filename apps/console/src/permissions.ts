export interface ConsoleUser {
  id: string;
  email: string;
  displayName: string;
  role?: "root" | "admin" | "user";
}

export interface ConsoleTeam {
  id: string;
  name: string;
  quotaCredits: string;
  usedCredits: string;
  reservedCredits?: string;
  unlimited: boolean;
  rpmLimit?: number;
  tpmLimit?: number;
  concurrencyLimit?: number;
  disabledAt?: string | null;
}

export interface Membership {
  team: ConsoleTeam;
  role: "owner" | "admin" | "member";
  memberDisabledAt: string | null;
  monthlyBudgetCredits: string;
}

export interface SessionData {
  user: ConsoleUser;
  memberships?: Membership[];
  mfaSetupRequired?: boolean;
}

export function isTeamManager(
  user: ConsoleUser,
  membership?: Membership,
): boolean {
  // 全局 admin 不代表拥有任意团队；管理权必须来自当前团队的有效成员关系。
  return (
    user.role === "root" ||
    Boolean(
      membership &&
        !membership.memberDisabledAt &&
        !membership.team.disabledAt &&
        ["owner", "admin"].includes(membership.role),
    )
  );
}

export function resolveConsoleSection(
  section: string,
  isRoot: boolean,
  canManageTeam: boolean,
): string {
  if (section === "teams" && !isRoot) return canManageTeam ? "team" : "usage";
  return section;
}

export function routeAllowed(
  section: string,
  isRoot: boolean,
  canManageTeam: boolean,
): boolean {
  if (section === "model-display") return isRoot;
  if (["usage", "devices", "profile", "invite", "catalog"].includes(section))
    return true;
  if (
    [
      "team",
      "members",
      "policy",
      "team-settings",
      "audit",
      "quota-requests",
    ].includes(section)
  )
    return canManageTeam;
  return isRoot;
}

export function scopedPath(path: string, teamId: string): string {
  if (
    !teamId ||
    !/^\/(usage(?:\/|\?|$)|audit(?:\?|$)|users(?:\?|$)|models(?:\?|$)|devices(?:\?|$)|quota-requests(?:\?|$))/u.test(
      path,
    )
  )
    return path;
  const [pathname, query = ""] = path.split("?");
  const params = new URLSearchParams(query);
  if (!params.has("teamId")) params.set("teamId", teamId);
  return `${pathname}?${params.toString()}`;
}
