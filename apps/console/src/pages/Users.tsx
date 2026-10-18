import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "../api.js";
import { confirmDialog, toastErr, toastOk } from "../components/Feedback.js";
import { Select } from "../components/Select.js";
import { Chip, Notice } from "../components/StatusChip.js";
import { formatShanghaiDate } from "../time.js";

type UserRole = "root" | "admin" | "user";

interface UserRow {
  id: string;
  email: string;
  displayName: string;
  role: UserRole;
  disabledAt: string | null;
  createdAt: string;
}

const ROLE_LABEL: Record<UserRole, string> = {
  root: "超级管理员",
  admin: "管理员",
  user: "用户",
};

export function UsersPage({
  currentUserId,
  currentUserRole,
}: {
  currentUserId: string;
  currentUserRole: UserRole;
}) {
  const isRoot = currentUserRole === "root";
  const [users, setUsers] = useState<UserRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<"admin" | "user">("user");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const data = await api<{ users: UserRow[] }>("/users");
      setUsers(data.users);
      setError(null);
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载失败");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // 操作失败走全局 Toast；内联 error 仅保留给页面加载失败。
  const run = async (action: () => Promise<unknown>, success?: string): Promise<void> => {
    setBusy(true);
    try {
      await action();
      await load();
      if (success) toastOk(success);
    } catch (actionError) {
      toastErr(actionError instanceof Error ? actionError.message : "操作失败");
    } finally {
      setBusy(false);
    }
  };

  const createUser = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    const createdEmail = email.trim();
    await run(async () => {
      await api("/users", {
        method: "POST",
        body: JSON.stringify({ email, displayName, password, role: isRoot ? role : "user" }),
      });
      setEmail("");
      setDisplayName("");
      setPassword("");
      setRole("user");
    }, `已创建用户 ${createdEmail}`);
  };

  const setPasswordFor = async (user: UserRow, isSelf: boolean): Promise<void> => {
    const minLength = user.role === "root" ? 12 : 8;
    const answer = await confirmDialog({
      k: isSelf ? "修改密码" : "重置密码",
      title: `为 ${user.email} 设置新密码`,
      message: isSelf
        ? "修改后当前登录会话也会失效，需要使用新密码重新登录。"
        : "重置后该用户全部后台会话与设备凭据立即吊销，需重新登录。",
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
        api(`/users/${user.id}`, { method: "PATCH", body: JSON.stringify({ password: answer.value }) }),
      isSelf ? "密码已更新，请重新登录" : `已重置 ${user.email} 的密码`,
    );
    if (isSelf) {
      window.location.reload();
    }
  };

  const changeRole = async (user: UserRow, nextRole: "admin" | "user"): Promise<void> => {
    const promoting = nextRole === "admin";
    const answer = await confirmDialog({
      k: promoting ? "设为管理员" : "降为普通用户",
      title: `将 ${user.email} ${promoting ? "设为管理员" : "降为普通用户"}？`,
      message: promoting
        ? "该用户将获得控制台管理权限，但创建/升降管理员仍仅限超级管理员。"
        : "降级后该用户全部后台会话与设备凭据立即吊销，且不能再登录控制台。",
      confirmText: promoting ? "设为管理员" : "降级",
      danger: !promoting,
    });
    if (!answer.confirmed) return;
    await run(
      () =>
        api(`/users/${user.id}`, {
          method: "PATCH",
          body: JSON.stringify({ role: nextRole }),
        }),
      `已将 ${user.email} ${promoting ? "设为管理员" : "降为普通用户"}`,
    );
  };

  const disableUser = async (user: UserRow): Promise<void> => {
    const answer = await confirmDialog({
      k: "禁用用户",
      title: `禁用 ${user.email}？`,
      message: "禁用即时生效：平台登录、控制台会话、网关请求全部 401，其后台会话与设备凭据一并吊销。",
      confirmText: "禁用",
      danger: true,
    });
    if (!answer.confirmed) return;
    await run(
      () =>
        api(`/users/${user.id}`, { method: "PATCH", body: JSON.stringify({ disabled: true }) }),
      `已禁用 ${user.email}`,
    );
  };

  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">组织 / 账号</p>
          <h1>用户</h1>
        </div>
      </header>
      <p className="readout" style={{ margin: 0, marginBottom: 18 }}>
        平台账号供成员登录客户端与控制台；超级管理员唯一且不可禁用/降级，普通管理员只能管理普通用户。
      </p>

      {error ? (
        <Notice tone="stop" k="操作未完成">
          {error}
        </Notice>
      ) : null}

      <section className="pane">
        <div className="pane-h">
          <h2>创建用户</h2>
        </div>
        <div className="pane-b">
          <form className="form-row" onSubmit={(event) => void createUser(event)}>
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
            {isRoot ? (
              <Select
                ariaLabel="角色"
                style={{ width: 110 }}
                value={role}
                onChange={(value) => setRole(value as "admin" | "user")}
                options={[
                  { value: "user", label: "用户" },
                  { value: "admin", label: "管理员" },
                ]}
              />
            ) : null}
            <button className="btn" type="submit" disabled={busy}>
              创建用户
            </button>
          </form>
          <p className="readout" style={{ margin: 0, marginTop: 10 }}>
            创建后还需在「团队」页把用户加入团队才会有额度归属。超级管理员账号只能首次初始化创建。
          </p>
        </div>
      </section>

      <section className="pane d1">
        <div className="pane-h">
          <h2>用户清单</h2>
          <span className="readout">{(users ?? []).length} 个账号</span>
        </div>
        <div className="pane-b flush table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">邮箱</th>
                <th scope="col">显示名</th>
                <th scope="col">角色</th>
                <th scope="col">状态</th>
                <th scope="col" className="num">
                  创建时间
                </th>
                <th scope="col" aria-label="操作" />
              </tr>
            </thead>
            <tbody>
              {(users ?? []).map((user) => {
                const isSelf = user.id === currentUserId;
                const canManage = isRoot
                  ? user.role !== "root"
                  : user.role === "user" && !isSelf;
                return (
                  <tr key={user.id}>
                    <td>{user.email}</td>
                    <td>{user.displayName}</td>
                    <td>{ROLE_LABEL[user.role]}</td>
                    <td>
                      {user.disabledAt ? <Chip tone="mute">已禁用</Chip> : <Chip tone="ok">正常</Chip>}
                    </td>
                    <td className="num">{formatShanghaiDate(user.createdAt)}</td>
                    <td>
                      <div className="form-row" style={{ justifyContent: "flex-end" }}>
                        {isSelf ? (
                          <>
                            <span className="readout">当前登录</span>
                            <button
                              className="btn btn-ghost btn-sm"
                              disabled={busy}
                              onClick={() => void setPasswordFor(user, true)}
                            >
                              修改密码
                            </button>
                          </>
                        ) : user.role === "root" ? (
                          <span className="readout">受保护</span>
                        ) : canManage ? (
                          <>
                            <button
                              className="btn btn-ghost btn-sm"
                              disabled={busy}
                              onClick={() => void setPasswordFor(user, false)}
                            >
                              重置密码
                            </button>
                            {isRoot ? (
                              <button
                                className="btn btn-ghost btn-sm"
                                disabled={busy}
                                onClick={() =>
                                  void changeRole(user, user.role === "admin" ? "user" : "admin")
                                }
                              >
                                {user.role === "admin" ? "降为普通用户" : "设为管理员"}
                              </button>
                            ) : null}
                            {user.disabledAt ? (
                              <button
                                className="btn btn-ghost btn-sm"
                                disabled={busy}
                                onClick={() =>
                                  void run(
                                    () =>
                                      api(`/users/${user.id}`, {
                                        method: "PATCH",
                                        body: JSON.stringify({ disabled: false }),
                                      }),
                                    `已启用 ${user.email}`,
                                  )
                                }
                              >
                                启用
                              </button>
                            ) : (
                              <button
                                className="btn btn-danger btn-sm"
                                disabled={busy}
                                onClick={() => void disableUser(user)}
                              >
                                禁用
                              </button>
                            )}
                          </>
                        ) : (
                          <span className="readout">无权操作</span>
                        )}
                      </div>
                    </td>
                  </tr>
                );
              })}
              {users !== null && users.length === 0 ? (
                <tr>
                  <td colSpan={6} className="readout">
                    还没有用户。
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}
