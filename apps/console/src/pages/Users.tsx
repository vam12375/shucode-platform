import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "../api.js";
import { confirmDialog, toastErr, toastOk } from "../components/Feedback.js";
import { Select } from "../components/Select.js";
import { Chip, Notice } from "../components/StatusChip.js";
import { formatShanghaiDate } from "../time.js";

interface UserRow {
  id: string;
  email: string;
  displayName: string;
  role: "admin" | "member";
  disabledAt: string | null;
  createdAt: string;
}

export function UsersPage({ currentUserId }: { currentUserId: string }) {
  const [users, setUsers] = useState<UserRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<"member" | "admin">("member");
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
        body: JSON.stringify({ email, displayName, password, role }),
      });
      setEmail("");
      setDisplayName("");
      setPassword("");
      setRole("member");
    }, `已创建用户 ${createdEmail}`);
  };

  const setPasswordFor = async (user: UserRow): Promise<void> => {
    const answer = await confirmDialog({
      k: "重置密码",
      title: `为 ${user.email} 设置新密码`,
      message: "重置后该用户全部后台会话与设备凭据立即吊销，需重新登录。",
      confirmText: "重置",
      danger: true,
      input: { label: "新密码", type: "password", placeholder: "至少 8 位", minLength: 8 },
    });
    if (!answer.confirmed) return;
    await run(
      () =>
        api(`/users/${user.id}`, { method: "PATCH", body: JSON.stringify({ password: answer.value }) }),
      `已重置 ${user.email} 的密码`,
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
        平台账号供成员登录客户端与控制台；禁用后即时生效（登录、user-config、网关请求全部 401）。
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
            <Select
              ariaLabel="角色"
              style={{ width: 110 }}
              value={role}
              onChange={(value) => setRole(value as "member" | "admin")}
              options={[
                { value: "member", label: "成员" },
                { value: "admin", label: "管理员" },
              ]}
            />
            <button className="btn" type="submit" disabled={busy}>
              创建用户
            </button>
          </form>
          <p className="readout" style={{ margin: 0, marginTop: 10 }}>
            创建后还需在「团队」页把用户加入团队才会有额度归属。
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
              {(users ?? []).map((user) => (
                <tr key={user.id}>
                  <td>{user.email}</td>
                  <td>{user.displayName}</td>
                  <td>{user.role === "admin" ? "管理员" : "成员"}</td>
                  <td>{user.disabledAt ? <Chip tone="mute">已禁用</Chip> : <Chip tone="ok">正常</Chip>}</td>
                  <td className="num">{formatShanghaiDate(user.createdAt)}</td>
                  <td>
                    <div className="form-row" style={{ justifyContent: "flex-end" }}>
                      {user.id === currentUserId ? (
                        <span className="readout">当前登录</span>
                      ) : (
                        <>
                          <button
                            className="btn btn-ghost btn-sm"
                            disabled={busy}
                            onClick={() => void setPasswordFor(user)}
                          >
                            重置密码
                          </button>
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
                      )}
                    </div>
                  </td>
                </tr>
              ))}
              {users && users.length === 0 ? (
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
