import {
  MICRO_CREDITS_PER_CREDIT,
  formatCreditsDisplay,
} from "@shucode-platform/contracts/credits";
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "../api.js";
import {
  confirmDialog,
  isDialogOpen,
  toastErr,
  toastOk,
} from "../components/Feedback.js";
import { Gauge } from "../components/Gauge.js";
import { Select } from "../components/Select.js";
import { Chip, Notice, type ChipTone } from "../components/StatusChip.js";
import { formatShanghaiDate, formatShanghaiTime } from "../time.js";

type UserRole = "root" | "admin" | "user";

interface TeamBrief {
  teamId: string;
  teamName: string;
  memberRole: string;
  monthlyBudgetCredits: string;
  memberDisabledAt: string | null;
  joinedAt: string;
}

interface AccountRow {
  id: string;
  email: string;
  displayName: string;
  role: UserRole;
  disabledAt: string | null;
  createdAt: string;
  team?: TeamBrief | null;
}

interface TeamRow {
  id: string;
  name: string;
  quotaCredits: string;
  usedCredits: string;
  reservedCredits: string;
  unlimited: boolean;
  memberCount: number;
  createdAt: string;
}

interface UsageAggregateRow {
  model: string;
  requests: number;
  creditsMicro: number;
}

interface DeviceRow {
  id: string;
  name: string;
  platform: string | null;
  hint: string;
  userEmail: string;
  createdAt: string;
  lastUsedAt: string | null;
  revokedAt: string | null;
}

const ROLE_LABEL: Record<UserRole, string> = {
  root: "超级管理员",
  admin: "管理员",
  user: "用户",
};

const MEMBER_ROLE_OPTIONS = [
  { value: "owner", label: "团队所有者" },
  { value: "admin", label: "团队管理员" },
  { value: "member", label: "成员" },
];

const MEMBER_ROLE_LABEL: Record<string, string> = {
  owner: "所有者",
  admin: "管理员",
  member: "成员",
};

const HOT_RATIO = 0.9;

/** 按「成功率状态」取印章语义：不限 → mute，近额 → hot，其余 → ok 生效。 */
function teamStatusChip(team: TeamRow): { tone?: ChipTone; label: string } {
  if (team.unlimited) return { tone: "mute", label: "不限" };
  const quota = Number.parseFloat(team.quotaCredits) || 0;
  const used = Number.parseFloat(team.usedCredits) || 0;
  const ratio = quota > 0 ? Math.min(used / quota, 1) : 0;
  return ratio >= HOT_RATIO
    ? { tone: "hot", label: "近额" }
    : { tone: "ok", label: "生效" };
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "操作失败";
}

/**
 * 「团队与账号」合并页（root 专属）：团队台账 + 账号主表同屏，
 * 账号的团队归属在行内直接调整，新建账号可一步加入团队；
 * 点账号行滑出详情抽屉（近 30 日用量 / 设备 / 成员预算 / 安全操作）。
 */
export function OrgPage({ currentUserId }: { currentUserId: string }) {
  const [teams, setTeams] = useState<TeamRow[] | null>(null);
  const [accounts, setAccounts] = useState<AccountRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const [teamFilter, setTeamFilter] = useState("");
  const [search, setSearch] = useState("");
  const [drawerId, setDrawerId] = useState<string | null>(null);

  const [showCreateAccount, setShowCreateAccount] = useState(false);
  const [showCreateTeam, setShowCreateTeam] = useState(false);
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<"admin" | "user">("user");
  const [joinTeamId, setJoinTeamId] = useState("");
  const [teamName, setTeamName] = useState("");
  const [teamQuota, setTeamQuota] = useState("100");

  const load = useCallback(async () => {
    try {
      const [teamData, accountData] = await Promise.all([
        api<{ teams: TeamRow[] }>("/teams"),
        api<{ users: AccountRow[] }>("/users"),
      ]);
      setTeams(teamData.teams);
      setAccounts(accountData.users);
      setError(null);
    } catch (loadError) {
      setError(errorMessage(loadError));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // 操作失败也刷新列表：换团队是「移出+加入」两段调用，失败时可能已移出。
  const run = async (
    action: () => Promise<unknown>,
    success?: string,
  ): Promise<void> => {
    setBusy(true);
    try {
      await action();
      await load();
      if (success) toastOk(success);
    } catch (actionError) {
      toastErr(errorMessage(actionError));
      await load().catch(() => {});
    } finally {
      setBusy(false);
    }
  };

  const createAccount = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    const accountEmail = email.trim();
    setBusy(true);
    try {
      await api("/users", {
        method: "POST",
        body: JSON.stringify({ email: accountEmail, displayName, password, role }),
      });
      if (joinTeamId) {
        try {
          await api(`/teams/${joinTeamId}/members`, {
            method: "POST",
            body: JSON.stringify({ email: accountEmail }),
          });
          toastOk(`已创建 ${accountEmail} 并加入团队`);
        } catch (joinError) {
          // 账号已创建成功，入团失败可在列表的「所属团队」列重试。
          toastErr(`账号已创建，但加入团队失败：${errorMessage(joinError)}`);
        }
      } else {
        toastOk(`已创建 ${accountEmail}`);
      }
      setEmail("");
      setDisplayName("");
      setPassword("");
      setRole("user");
      setJoinTeamId("");
      await load();
    } catch (createError) {
      toastErr(errorMessage(createError), "创建失败");
    } finally {
      setBusy(false);
    }
  };

  const createTeam = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    const name = teamName.trim();
    await run(async () => {
      await api("/teams", {
        method: "POST",
        body: JSON.stringify({ name, quotaCredits: teamQuota }),
      });
      setTeamName("");
    }, `已创建团队「${name}」`);
  };

  const changeTeam = async (
    account: AccountRow,
    nextTeamId: string,
  ): Promise<void> => {
    const current = account.team;
    if ((current?.teamId ?? "") === nextTeamId) return;
    if (nextTeamId === "" && current) {
      const answer = await confirmDialog({
        k: "移出团队",
        title: `将 ${account.email} 移出「${current.teamName}」？`,
        message:
          "移出后该账号不再归属任何团队：网关请求与团队成员立即失效，历史用量仍计入原团队。",
        confirmText: "移出",
        danger: true,
      });
      if (!answer.confirmed) return;
      await run(
        () =>
          api(`/teams/${current.teamId}/members/${account.id}`, {
            method: "DELETE",
          }),
        `已将 ${account.email} 移出团队`,
      );
      return;
    }
    if (current) {
      const answer = await confirmDialog({
        k: "更换团队",
        title: `把 ${account.email} 从「${current.teamName}」移到新团队？`,
        message:
          "更换 = 先移出原团队再加入新团队：原团队中的成员角色与月度预算不会带走，历史用量仍计入原团队。",
        confirmText: "更换",
        danger: true,
      });
      if (!answer.confirmed) return;
      await run(async () => {
        await api(`/teams/${current.teamId}/members/${account.id}`, {
          method: "DELETE",
        });
        await api(`/teams/${nextTeamId}/members`, {
          method: "POST",
          body: JSON.stringify({ email: account.email }),
        });
      }, `已将 ${account.email} 调整到新团队`);
      return;
    }
    await run(
      () =>
        api(`/teams/${nextTeamId}/members`, {
          method: "POST",
          body: JSON.stringify({ email: account.email }),
        }),
      `已将 ${account.email} 加入团队`,
    );
  };

  const resetPassword = async (
    account: AccountRow,
    isSelf: boolean,
  ): Promise<void> => {
    const minLength = account.role === "root" ? 12 : 8;
    const answer = await confirmDialog({
      k: isSelf ? "修改密码" : "重置密码",
      title: `为 ${account.email} 设置新密码`,
      message: isSelf
        ? "修改后当前登录会话也会失效，需要使用新密码重新登录。"
        : "重置后该账号全部后台会话与设备凭据立即吊销，需重新登录。",
      confirmText: isSelf ? "修改" : "重置",
      danger: true,
      input: {
        label: "新密码",
        type: "password",
        placeholder: `至少 ${minLength} 位`,
        minLength,
      },
    });
    if (!answer.confirmed) return;
    await run(
      () =>
        api(`/users/${account.id}`, {
          method: "PATCH",
          body: JSON.stringify({ password: answer.value }),
        }),
      isSelf ? "密码已更新，请重新登录" : `已重置 ${account.email} 的密码`,
    );
    if (isSelf) window.location.reload();
  };

  const togglePlatformRole = async (account: AccountRow): Promise<void> => {
    const promoting = account.role === "user";
    const answer = await confirmDialog({
      k: promoting ? "设为管理员" : "降为普通用户",
      title: `将 ${account.email} ${promoting ? "设为管理员" : "降为普通用户"}？`,
      message: promoting
        ? "该账号将获得控制台管理权限，但创建/升降管理员仍仅限超级管理员。"
        : "降级后该账号全部后台会话与设备凭据立即吊销，且不能再登录控制台管理页。",
      confirmText: promoting ? "设为管理员" : "降级",
      danger: !promoting,
    });
    if (!answer.confirmed) return;
    await run(
      () =>
        api(`/users/${account.id}`, {
          method: "PATCH",
          body: JSON.stringify({ role: promoting ? "admin" : "user" }),
        }),
      `已将 ${account.email} ${promoting ? "设为管理员" : "降为普通用户"}`,
    );
  };

  const toggleDisabled = async (account: AccountRow): Promise<void> => {
    const disabling = !account.disabledAt;
    if (disabling) {
      const answer = await confirmDialog({
        k: "禁用账号",
        title: `禁用 ${account.email}？`,
        message:
          "禁用即时生效：平台登录、控制台会话、网关请求全部 401，其后台会话与设备凭据一并吊销。",
        confirmText: "禁用",
        danger: true,
      });
      if (!answer.confirmed) return;
    }
    await run(
      () =>
        api(`/users/${account.id}`, {
          method: "PATCH",
          body: JSON.stringify({ disabled: disabling }),
        }),
      disabling ? `已禁用 ${account.email}` : `已启用 ${account.email}`,
    );
  };

  const teamOptions = [
    { value: "", label: "暂不加入团队" },
    ...(teams ?? []).map((team) => ({ value: team.id, label: team.name })),
  ];
  const teamCellOptions = [
    { value: "", label: "未入团队" },
    ...(teams ?? []).map((team) => ({ value: team.id, label: team.name })),
  ];
  const filterOptions = [
    { value: "", label: "全部团队" },
    { value: "none", label: "未入团队" },
    ...(teams ?? []).map((team) => ({ value: team.id, label: team.name })),
  ];
  const query = search.trim().toLowerCase();
  const filtered = (accounts ?? []).filter((account) => {
    if (teamFilter === "none" && account.team) return false;
    if (teamFilter && teamFilter !== "none") {
      if (account.team?.teamId !== teamFilter) return false;
    }
    if (
      query &&
      !account.email.toLowerCase().includes(query) &&
      !account.displayName.toLowerCase().includes(query)
    ) {
      return false;
    }
    return true;
  });
  const teamRows = teams ?? [];
  const totalMembers = teamRows.reduce((sum, team) => sum + team.memberCount, 0);
  const totalUsed = teamRows.reduce(
    (sum, team) => sum + (Number.parseFloat(team.usedCredits) || 0),
    0,
  );
  const drawerAccount = drawerId
    ? ((accounts ?? []).find((account) => account.id === drawerId) ?? null)
    : null;

  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">组织 / 台账</p>
          <h1>团队与账号</h1>
        </div>
        <div className="form-row">
          <button
            className="btn btn-ghost"
            type="button"
            onClick={() => {
              setShowCreateTeam(false);
              setShowCreateAccount((value) => !value);
            }}
          >
            {showCreateAccount ? "收起" : "+ 新建账号"}
          </button>
          <button
            className="btn btn-ghost"
            type="button"
            onClick={() => {
              setShowCreateAccount(false);
              setShowCreateTeam((value) => !value);
            }}
          >
            {showCreateTeam ? "收起" : "+ 新建团队"}
          </button>
        </div>
      </header>
      <p className="readout" style={{ margin: 0, marginBottom: 18 }}>
        团队是额度与计费单元，一个账号同一时间只归属一个团队；新建账号可直接选团队，
        账号的团队归属在「所属团队」列内调整。点账号行查看用量、设备与成员预算。
      </p>

      {error ? (
        <Notice tone="stop" k="操作未完成">
          {error}
        </Notice>
      ) : null}

      {showCreateAccount ? (
        <section className="pane">
          <div className="pane-h">
            <h2>新建账号</h2>
          </div>
          <div className="pane-b">
            <form
              className="form-row"
              onSubmit={(event) => void createAccount(event)}
            >
              <input
                className="input"
                style={{ width: 220 }}
                type="email"
                placeholder="邮箱"
                aria-label="邮箱"
                value={email}
                onChange={(event) => setEmail(event.target.value)}
                required
              />
              <input
                className="input"
                style={{ width: 140 }}
                placeholder="显示名"
                aria-label="显示名"
                value={displayName}
                onChange={(event) => setDisplayName(event.target.value)}
                required
              />
              <input
                className="input"
                style={{ width: 160 }}
                type="password"
                autoComplete="new-password"
                placeholder="初始密码（≥8 位）"
                aria-label="初始密码"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                required
                minLength={8}
              />
              <Select
                ariaLabel="平台角色"
                style={{ width: 110 }}
                value={role}
                onChange={(value) => setRole(value as "admin" | "user")}
                options={[
                  { value: "user", label: "用户" },
                  { value: "admin", label: "管理员" },
                ]}
              />
              <Select
                ariaLabel="加入团队"
                style={{ width: 170 }}
                value={joinTeamId}
                onChange={setJoinTeamId}
                options={teamOptions}
              />
              <button className="btn" type="submit" disabled={busy}>
                创建账号
              </button>
            </form>
            <p className="readout" style={{ margin: 0, marginTop: 10 }}>
              选择团队后创建即加入（成员角色默认「成员」）；未入团队的账号无法调用网关。
              超级管理员账号只能首次初始化创建。
            </p>
          </div>
        </section>
      ) : null}

      {showCreateTeam ? (
        <section className="pane">
          <div className="pane-h">
            <h2>新建团队</h2>
          </div>
          <div className="pane-b">
            <form
              className="form-row"
              onSubmit={(event) => void createTeam(event)}
            >
              <input
                className="input grow"
                placeholder="团队名称"
                aria-label="团队名称"
                value={teamName}
                onChange={(event) => setTeamName(event.target.value)}
                required
              />
              <input
                className="input num"
                style={{ width: 150 }}
                placeholder="额度 credit"
                aria-label="额度 credit"
                value={teamQuota}
                onChange={(event) => setTeamQuota(event.target.value)}
              />
              <button
                className="btn"
                type="submit"
                disabled={busy || !teamName.trim()}
              >
                新建团队
              </button>
            </form>
            <p className="readout" style={{ margin: 0, marginTop: 10 }}>
              额度以 credit 计（1 credit = ¥1，按模型单价折算）；团队成员共享同一额度池。
            </p>
          </div>
        </section>
      ) : null}

      <section className="pane">
        <div className="pane-h">
          <h2>团队</h2>
          <span className="readout">实时 · 网关计量</span>
        </div>
        <div className="pane-b flush table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">团队</th>
                <th scope="col" className="num">
                  成员
                </th>
                <th scope="col">额度尺</th>
                <th scope="col">状态</th>
                <th scope="col" className="num">
                  立账
                </th>
              </tr>
            </thead>
            <tbody>
              {teamRows.map((team) => {
                const used = Number.parseFloat(team.usedCredits) || 0;
                const quotaValue = Number.parseFloat(team.quotaCredits) || 0;
                const status = teamStatusChip(team);
                return (
                  <tr key={team.id}>
                    <td>
                      <a className="name" href={`#/teams/${team.id}`}>
                        {team.name}
                      </a>
                    </td>
                    <td className="num">{team.memberCount}</td>
                    <td>
                      <Gauge
                        used={used}
                        quota={team.unlimited ? null : quotaValue}
                        hotAt={HOT_RATIO}
                      />
                    </td>
                    <td>
                      <Chip tone={status.tone}>{status.label}</Chip>
                    </td>
                    <td className="num">{formatShanghaiDate(team.createdAt)}</td>
                  </tr>
                );
              })}
              {teamRows.length === 0 ? (
                <tr>
                  <td colSpan={5} className="readout">
                    还没有团队，先创建一个。
                  </td>
                </tr>
              ) : null}
            </tbody>
            <tfoot>
              <tr>
                <td>合计 · {teamRows.length} 个团队</td>
                <td className="num">{totalMembers}</td>
                <td colSpan={2}>
                  已用 {formatCreditsDisplay(totalUsed)} credit
                </td>
                <td />
              </tr>
            </tfoot>
          </table>
        </div>
      </section>

      <section className="pane d1">
        <div className="pane-h">
          <h2>账号</h2>
          <span className="readout">
            {filtered.length} / {(accounts ?? []).length} 个账号
          </span>
        </div>
        <div className="pane-b">
          <div className="tools">
            <Select
              ariaLabel="按团队筛选"
              style={{ width: 180 }}
              value={teamFilter}
              onChange={setTeamFilter}
              options={filterOptions}
            />
            <input
              className="input"
              style={{ width: 220 }}
              placeholder="搜索邮箱或显示名"
              aria-label="搜索邮箱或显示名"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </div>
        </div>
        <div className="pane-b flush table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">邮箱</th>
                <th scope="col">显示名</th>
                <th scope="col">平台角色</th>
                <th scope="col">所属团队</th>
                <th scope="col">状态</th>
                <th scope="col" className="num">
                  创建时间
                </th>
                <th scope="col" aria-label="操作" />
              </tr>
            </thead>
            <tbody>
              {filtered.map((account) => {
                const isSelf = account.id === currentUserId;
                const protectedRoot = account.role === "root";
                const brief = account.team ?? null;
                return (
                  <tr
                    key={account.id}
                    className="row-click"
                    onClick={(event) => {
                      if (
                        (event.target as HTMLElement).closest(
                          "button, a, input, .ui-select",
                        )
                      ) {
                        return;
                      }
                      setDrawerId(account.id);
                    }}
                  >
                    <td>{account.email}</td>
                    <td>{account.displayName}</td>
                    <td>{ROLE_LABEL[account.role]}</td>
                    <td>
                      {protectedRoot ? (
                        <span className="readout">—</span>
                      ) : (
                        <div className="team-cell">
                          <Select
                            ariaLabel={`${account.email} 的团队归属`}
                            style={{ width: 190 }}
                            disabled={busy}
                            value={brief?.teamId ?? ""}
                            onChange={(value) => void changeTeam(account, value)}
                            options={teamCellOptions}
                          />
                          {brief ? (
                            <span className="team-cell-notes">
                              {brief.memberRole !== "member" ? (
                                <Chip tone="info">
                                  {MEMBER_ROLE_LABEL[brief.memberRole] ??
                                    brief.memberRole}
                                </Chip>
                              ) : null}
                              {brief.memberDisabledAt ? (
                                <Chip tone="hot">团队内已暂停</Chip>
                              ) : null}
                              {brief.monthlyBudgetCredits ? (
                                <span className="readout">
                                  月预算{" "}
                                  {formatCreditsDisplay(
                                    brief.monthlyBudgetCredits,
                                  )}{" "}
                                  credit
                                </span>
                              ) : null}
                            </span>
                          ) : null}
                        </div>
                      )}
                    </td>
                    <td>
                      {account.disabledAt ? (
                        <Chip tone="mute">已禁用</Chip>
                      ) : (
                        <Chip tone="ok">正常</Chip>
                      )}
                    </td>
                    <td className="num">
                      {formatShanghaiDate(account.createdAt)}
                    </td>
                    <td>
                      <div
                        className="form-row"
                        style={{ justifyContent: "flex-end" }}
                      >
                        {isSelf ? (
                          <>
                            <span className="readout">当前登录</span>
                            <button
                              className="btn btn-ghost btn-sm"
                              disabled={busy}
                              onClick={() => void resetPassword(account, true)}
                            >
                              修改密码
                            </button>
                          </>
                        ) : protectedRoot ? (
                          <span className="readout">受保护</span>
                        ) : (
                          <>
                            <button
                              className="btn btn-ghost btn-sm"
                              disabled={busy}
                              onClick={() => setDrawerId(account.id)}
                            >
                              详情
                            </button>
                            <button
                              className="btn btn-ghost btn-sm"
                              disabled={busy}
                              onClick={() => void resetPassword(account, false)}
                            >
                              重置密码
                            </button>
                            <button
                              className="btn btn-ghost btn-sm"
                              disabled={busy}
                              onClick={() => void togglePlatformRole(account)}
                            >
                              {account.role === "admin"
                                ? "降为普通用户"
                                : "设为管理员"}
                            </button>
                            {account.disabledAt ? (
                              <button
                                className="btn btn-ghost btn-sm"
                                disabled={busy}
                                onClick={() => void toggleDisabled(account)}
                              >
                                启用
                              </button>
                            ) : (
                              <button
                                className="btn btn-danger btn-sm"
                                disabled={busy}
                                onClick={() => void toggleDisabled(account)}
                              >
                                禁用
                              </button>
                            )}
                          </>
                        )}
                      </div>
                    </td>
                  </tr>
                );
              })}
              {accounts !== null && filtered.length === 0 ? (
                <tr>
                  <td colSpan={7} className="readout">
                    没有匹配的账号。
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </section>

      {drawerAccount ? (
        <UserDrawer
          account={drawerAccount}
          currentUserId={currentUserId}
          busy={busy}
          teams={teamRows}
          onRefresh={load}
          onClose={() => setDrawerId(null)}
          onResetPassword={resetPassword}
          onTogglePlatformRole={togglePlatformRole}
          onToggleDisabled={toggleDisabled}
          onTeamChanged={(account, nextTeamId) => changeTeam(account, nextTeamId)}
        />
      ) : null}
    </div>
  );
}

interface UserDrawerProps {
  account: AccountRow;
  currentUserId: string;
  busy: boolean;
  teams: TeamRow[];
  onRefresh: () => Promise<void>;
  onClose: () => void;
  onResetPassword: (account: AccountRow, isSelf: boolean) => Promise<void>;
  onTogglePlatformRole: (account: AccountRow) => Promise<void>;
  onToggleDisabled: (account: AccountRow) => Promise<void>;
  onTeamChanged: (account: AccountRow, nextTeamId: string) => Promise<void>;
}

function UserDrawer({
  account,
  currentUserId,
  busy,
  teams,
  onRefresh,
  onClose,
  onResetPassword,
  onTogglePlatformRole,
  onToggleDisabled,
  onTeamChanged,
}: UserDrawerProps) {
  const isSelf = account.id === currentUserId;
  const [usage, setUsage] = useState<UsageAggregateRow[] | null>(null);
  const [devices, setDevices] = useState<DeviceRow[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [localBusy, setLocalBusy] = useState(false);
  const [budget, setBudget] = useState(
    account.team?.monthlyBudgetCredits ?? "",
  );

  useEffect(() => {
    setBudget(account.team?.monthlyBudgetCredits ?? "");
  }, [account.id, account.team?.monthlyBudgetCredits]);

  useEffect(() => {
    const onKey = (event: KeyboardEvent): void => {
      if (event.key === "Escape" && !event.defaultPrevented && !isDialogOpen()) {
        onClose();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const teamId = account.team?.teamId;

  useEffect(() => {
    let cancelled = false;
    setUsage(null);
    setDevices(null);
    setLoadError(null);
    // 「近 30 日」按 UTC naive 串传递（服务端 from/to 无时区按 UTC 解析）。
    const from = new Date(Date.now() - 30 * 86_400_000)
      .toISOString()
      .slice(0, 19);
    const jobs: Promise<void>[] = [
      api<{ devices: DeviceRow[] }>(`/devices?userId=${account.id}`).then(
        (data) => {
          if (!cancelled) setDevices(data.devices);
        },
      ),
    ];
    if (teamId) {
      // 显式带 teamId：避免 api 层自动注入侧栏当前团队，导致误查其他团队的用量。
      jobs.push(
        api<{ aggregates: UsageAggregateRow[] }>(
          `/usage?userId=${account.id}&teamId=${teamId}&from=${from}`,
        ).then((data) => {
          if (!cancelled) setUsage(data.aggregates);
        }),
      );
    }
    Promise.all(jobs).catch((loadFailure) => {
      if (!cancelled) setLoadError(errorMessage(loadFailure));
    });
    return () => {
      cancelled = true;
    };
  }, [account.id, teamId]);

  const reloadDevices = useCallback(async (): Promise<void> => {
    try {
      const data = await api<{ devices: DeviceRow[] }>(
        `/devices?userId=${account.id}`,
      );
      setDevices(data.devices);
    } catch (loadFailure) {
      setLoadError(errorMessage(loadFailure));
    }
  }, [account.id]);

  const runLocal = async (
    action: () => Promise<unknown>,
    success: string,
  ): Promise<void> => {
    setLocalBusy(true);
    try {
      await action();
      await onRefresh();
      await reloadDevices();
      toastOk(success);
    } catch (actionError) {
      toastErr(errorMessage(actionError));
    } finally {
      setLocalBusy(false);
    }
  };

  const changeMemberRole = async (role: string): Promise<void> => {
    if (!account.team || role === account.team.memberRole) return;
    const label =
      MEMBER_ROLE_OPTIONS.find((option) => option.value === role)?.label ?? role;
    await runLocal(
      () =>
        api(`/teams/${account.team!.teamId}/members/${account.id}`, {
          method: "PATCH",
          body: JSON.stringify({ role }),
        }),
      `成员角色已更新为「${label}」`,
    );
  };

  const saveBudget = async (): Promise<void> => {
    if (!account.team) return;
    await runLocal(
      () =>
        api(`/teams/${account.team!.teamId}/members/${account.id}`, {
          method: "PATCH",
          body: JSON.stringify({ monthlyBudgetCredits: budget }),
        }),
      budget.trim() === "" ? "成员月预算已清除（不限）" : "成员月预算已保存",
    );
  };

  const removeMember = async (): Promise<void> => {
    if (!account.team) return;
    const answer = await confirmDialog({
      k: "移出团队",
      title: `将 ${account.email} 移出「${account.team.teamName}」？`,
      message:
        "移出后该账号不再归属任何团队：网关请求与团队成员立即失效，历史用量仍计入原团队。",
      confirmText: "移出",
      danger: true,
    });
    if (!answer.confirmed) return;
    await runLocal(
      () =>
        api(`/teams/${account.team!.teamId}/members/${account.id}`, {
          method: "DELETE",
        }),
      `已将 ${account.email} 移出团队`,
    );
  };

  const revokeDevice = async (device: DeviceRow): Promise<void> => {
    const answer = await confirmDialog({
      k: "吊销设备",
      title: `吊销「${device.name || device.hint}」？`,
      message: "吊销后该设备需要重新走登录授权流程。",
      confirmText: "吊销",
      danger: true,
    });
    if (!answer.confirmed) return;
    await runLocal(
      () => api(`/devices/${device.id}/revoke`, { method: "POST" }),
      "设备已吊销",
    );
  };

  const totalRequests = (usage ?? []).reduce(
    (sum, row) => sum + row.requests,
    0,
  );
  const totalCredits =
    (usage ?? []).reduce((sum, row) => sum + row.creditsMicro, 0) /
    MICRO_CREDITS_PER_CREDIT;
  const topModels = [...(usage ?? [])]
    .sort((a, b) => b.creditsMicro - a.creditsMicro)
    .slice(0, 8);
  const frozen = busy || localBusy;

  return (
    <>
      <div className="drawer-backdrop" onClick={onClose} aria-hidden="true" />
      <aside
        className="drawer"
        role="dialog"
        aria-modal="true"
        aria-label={`账号详情 ${account.email}`}
      >
        <header className="drawer-h">
          <div className="drawer-title">
            <p className="eyebrow">账号详情</p>
            <h3 title={account.email}>
              {account.displayName || account.email}
            </h3>
            <p className="readout">{account.email}</p>
          </div>
          <div className="drawer-actions">
            {account.disabledAt ? (
              <Chip tone="mute">已禁用</Chip>
            ) : (
              <Chip tone="ok">正常</Chip>
            )}
            <button className="btn btn-ghost btn-sm" type="button" onClick={onClose}>
              关闭
            </button>
          </div>
        </header>
        <div className="drawer-b">
          <section className="drawer-sec">
            <h4 className="drawer-sec-title">账号与安全</h4>
            <p className="readout drawer-note">
              平台角色：{ROLE_LABEL[account.role]} · 创建于{" "}
              {formatShanghaiDate(account.createdAt)}
            </p>
            <div className="form-row">
              {isSelf ? (
                <button
                  className="btn btn-ghost btn-sm"
                  disabled={frozen}
                  onClick={() => void onResetPassword(account, true)}
                >
                  修改密码
                </button>
              ) : account.role === "root" ? (
                <span className="readout">超级管理员账号受保护，不可禁用或降级。</span>
              ) : (
                <>
                  <button
                    className="btn btn-ghost btn-sm"
                    disabled={frozen}
                    onClick={() => void onResetPassword(account, false)}
                  >
                    重置密码
                  </button>
                  <button
                    className="btn btn-ghost btn-sm"
                    disabled={frozen}
                    onClick={() => void onTogglePlatformRole(account)}
                  >
                    {account.role === "admin" ? "降为普通用户" : "设为管理员"}
                  </button>
                  {account.disabledAt ? (
                    <button
                      className="btn btn-ghost btn-sm"
                      disabled={frozen}
                      onClick={() => void onToggleDisabled(account)}
                    >
                      启用账号
                    </button>
                  ) : (
                    <button
                      className="btn btn-danger btn-sm"
                      disabled={frozen}
                      onClick={() => void onToggleDisabled(account)}
                    >
                      禁用账号
                    </button>
                  )}
                </>
              )}
            </div>
          </section>

          {account.role !== "root" ? (
            <section className="drawer-sec">
              <h4 className="drawer-sec-title">团队与预算</h4>
              {account.team ? (
                <>
                  <p className="readout drawer-note">
                    {account.team.teamName} · 加入于{" "}
                    {formatShanghaiDate(account.team.joinedAt)}
                  </p>
                  <div className="form-row">
                    <Select
                      ariaLabel="成员角色"
                      style={{ width: 130 }}
                      disabled={frozen}
                      value={account.team.memberRole}
                      onChange={(value) => void changeMemberRole(value)}
                      options={MEMBER_ROLE_OPTIONS}
                    />
                    <input
                      className="input num"
                      style={{ width: 120 }}
                      type="number"
                      min="0"
                      step="0.000001"
                      placeholder="不限"
                      aria-label="成员月预算"
                      value={budget}
                      onChange={(event) => setBudget(event.target.value)}
                    />
                    <button
                      className="btn btn-ghost btn-sm"
                      disabled={frozen}
                      onClick={() => void saveBudget()}
                    >
                      保存预算
                    </button>
                    <Select
                      ariaLabel="更换团队"
                      style={{ width: 170 }}
                      disabled={frozen}
                      value={account.team.teamId}
                      onChange={(value) => void onTeamChanged(account, value)}
                      options={[
                        { value: "", label: "移出团队" },
                        ...teams.map((team) => ({
                          value: team.id,
                          label: team.name,
                        })),
                      ]}
                    />
                  </div>
                  <p className="readout drawer-note">
                    月预算留空为不限，填 0 表示停止消费；超出预算后网关按 429 拒绝。
                  </p>
                </>
              ) : (
                <p className="readout drawer-note">
                  尚未加入团队，无网关用量与预算归属。
                </p>
              )}
              {account.team ? (
                <div className="form-row" style={{ marginTop: 10 }}>
                  <button
                    className="btn btn-danger btn-sm"
                    disabled={frozen}
                    onClick={() => void removeMember()}
                  >
                    移出团队
                  </button>
                </div>
              ) : null}
            </section>
          ) : null}

          <section className="drawer-sec">
            <h4 className="drawer-sec-title">近 30 日用量</h4>
            {!account.team ? (
              <p className="readout drawer-note">未入团队的账号不产生网关用量。</p>
            ) : usage === null ? (
              <p className="readout drawer-note">{loadError ?? "统计加载中…"}</p>
            ) : (
              <>
                <p className="drawer-readouts">
                  <b>{totalRequests.toLocaleString("en-US")}</b> 次请求 ·{" "}
                  <b>{formatCreditsDisplay(totalCredits)}</b> credit ·{" "}
                  {usage.length} 个模型
                </p>
                {topModels.length === 0 ? (
                  <p className="readout drawer-note">近 30 日暂无请求。</p>
                ) : (
                  <table className="table drawer-table">
                    <thead>
                      <tr>
                        <th scope="col">模型</th>
                        <th scope="col" className="num">
                          请求
                        </th>
                        <th scope="col" className="num">
                          积分（credit）
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {topModels.map((row) => (
                        <tr key={row.model}>
                          <td>{row.model}</td>
                          <td className="num">{row.requests.toLocaleString("en-US")}</td>
                          <td className="num">
                            {formatCreditsDisplay(
                              row.creditsMicro / MICRO_CREDITS_PER_CREDIT,
                            )}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}
              </>
            )}
          </section>

          <section className="drawer-sec">
            <h4 className="drawer-sec-title">
              设备{devices ? `（${devices.length}）` : ""}
            </h4>
            {devices === null ? (
              <p className="readout drawer-note">{loadError ?? "加载中…"}</p>
            ) : devices.length === 0 ? (
              <p className="readout drawer-note">该账号还没有设备凭据。</p>
            ) : (
              <table className="table drawer-table">
                <thead>
                  <tr>
                    <th scope="col">设备</th>
                    <th scope="col">最近使用</th>
                    <th scope="col" aria-label="操作" />
                  </tr>
                </thead>
                <tbody>
                  {devices.map((device) => (
                    <tr key={device.id}>
                      <td>
                        {device.name || "未命名设备"}
                        <span className="readout drawer-hint">
                          {device.platform ?? "未知平台"} · {device.hint}
                        </span>
                      </td>
                      <td className="readout">
                        {device.lastUsedAt
                          ? formatShanghaiTime(device.lastUsedAt)
                          : "从未使用"}
                      </td>
                      <td>
                        {device.revokedAt ? (
                          <Chip tone="mute">已吊销</Chip>
                        ) : (
                          <button
                            className="btn btn-ghost btn-sm"
                            disabled={frozen}
                            onClick={() => void revokeDevice(device)}
                          >
                            吊销
                          </button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </section>
        </div>
      </aside>
    </>
  );
}
