import { formatCreditsDisplay } from "@shucode-platform/contracts/credits";
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, navigate } from "../api.js";
import { useAuth } from "../auth.js";
import { confirmDialog, toastErr, toastOk } from "../components/Feedback.js";
import { MfaSettings } from "../components/MfaSettings.js";
import { Gauge } from "../components/Gauge.js";
import { Chip, Notice } from "../components/StatusChip.js";
import type { ConsoleTeam, ConsoleUser } from "../permissions.js";
import { formatShanghaiTime } from "../time.js";

interface Member {
  user: ConsoleUser;
  role: "owner" | "admin" | "member";
  joinedAt: string;
  memberDisabledAt: string | null;
  monthlyBudgetCredits: string;
}
interface Invitation {
  id: string;
  email: string;
  createdAt: string;
  expiresAt: string;
  acceptedAt: string | null;
  revokedAt: string | null;
}
interface Model {
  modelId: string;
  displayName: string;
  inputPricePerMtok: string;
  outputPricePerMtok: string;
  cacheReadPricePerMtok: string;
  cacheWritePricePerMtok: string;
  offPeakMultiplier?: string;
}
interface Policy {
  availableModels?: Array<{ modelId: string; displayName: string }>;
  dailyBudgetCredits: string;
  monthlyBudgetCredits: string;
  allowedModelIds: string[];
  budgetAlertPercent: number;
  rpmLimit: number;
  tpmLimit: number;
  concurrencyLimit: number;
}
const EMPTY_POLICY: Policy = {
  dailyBudgetCredits: "",
  monthlyBudgetCredits: "",
  allowedModelIds: [],
  budgetAlertPercent: 80,
  rpmLimit: 0,
  tpmLimit: 0,
  concurrencyLimit: 0,
};
function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "操作失败";
}
function money(value?: string): string {
  return formatCreditsDisplay(Number(value) || 0);
}

export function TenantPage({ section }: { section: string }) {
  const { teamId, isRoot, refreshSession } = useAuth();
  const [team, setTeam] = useState<ConsoleTeam | null>(null);
  const [members, setMembers] = useState<Member[]>([]);
  const [invitations, setInvitations] = useState<Invitation[]>([]);
  const [models, setModels] = useState<Model[]>([]);
  const [policy, setPolicy] = useState<Policy>(EMPTY_POLICY);
  const [notifications, setNotifications] = useState<
    Array<{ kind: string; message: string; tone: string }>
  >([]);
  const [budgets, setBudgets] = useState<Record<string, string>>({});
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [inviteUrl, setInviteUrl] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const load = useCallback(async () => {
    if (!teamId) return;
    try {
      const [detail, invitationData, policyData, modelData, noticeData] =
        await Promise.all([
          api<{ team: ConsoleTeam; members: Member[] }>(`/teams/${teamId}`),
          api<{ invitations: Invitation[] }>(`/teams/${teamId}/invitations`),
          api<Policy>(`/teams/${teamId}/policy`),
          api<{ models: Model[] }>("/models"),
          api<{
            notifications: Array<{
              kind: string;
              message: string;
              tone: string;
            }>;
          }>(`/teams/${teamId}/notifications`),
        ]);
      setTeam(detail.team);
      setName(detail.team.name);
      setMembers(detail.members);
      setBudgets(
        Object.fromEntries(
          detail.members.map((item) => [
            item.user.id,
            item.monthlyBudgetCredits ?? "",
          ]),
        ),
      );
      setInvitations(invitationData.invitations);
      setPolicy({ ...EMPTY_POLICY, ...policyData });
      setModels(
        policyData.availableModels
          ? policyData.availableModels.map((model) => ({
              ...model,
              inputPricePerMtok: "0",
              outputPricePerMtok: "0",
              cacheReadPricePerMtok: "0",
              cacheWritePricePerMtok: "0",
            }))
          : modelData.models,
      );
      setNotifications(noticeData.notifications);
      setError("");
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [teamId]);
  useEffect(() => {
    void load();
  }, [load]);
  const run = async (action: () => Promise<unknown>, message: string) => {
    setBusy(true);
    try {
      await action();
      await load();
      await refreshSession();
      toastOk(message);
    } catch (e) {
      toastErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  if (!teamId)
    return (
      <Notice k="尚未加入团队">
        请联系团队管理员获取邀请链接。你仍可查看本人设备、修改个人资料。
      </Notice>
    );
  const title =
    section === "members"
      ? "成员管理"
      : section === "policy"
        ? "预算与限制"
        : section === "team-settings"
          ? "团队设置"
          : "团队概览";
  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">团队工作台 / {team?.name ?? "加载中"}</p>
          <h1>{title}</h1>
        </div>
        <span className="readout">仅当前团队 · {members.length} 位成员</span>
      </header>
      {error ? (
        <Notice tone="stop" k="加载失败">
          {error}
        </Notice>
      ) : null}
      {section === "team" && team ? (
        <>
          {notifications.map((notice, index) => (
            <Notice
              key={`${notice.kind}:${index}`}
              tone={notice.tone === "stop" ? "stop" : undefined}
              k="团队提醒"
            >
              {notice.message}
            </Notice>
          ))}
          <section className="pane">
            <div className="pane-h">
              <h2>团队额度</h2>
              <Chip tone={team.unlimited ? "mute" : "ok"}>
                {team.unlimited ? "不限额度" : "已获批额度"}
              </Chip>
            </div>
            <div className="pane-b">
              <Gauge
                used={Number(team.usedCredits)}
                quota={team.unlimited ? null : Number(team.quotaCredits)}
              />
              <p className="readout">
                已消费 {money(team.usedCredits)} credit · 预留{" "}
                {money(team.reservedCredits)} credit ·{" "}
                {team.unlimited
                  ? "额度不限"
                  : `剩余 ${money(String(Math.max(0, Number(team.quotaCredits) - Number(team.usedCredits) - Number(team.reservedCredits ?? 0))))} credit`}
              </p>
              <p className="readout">
                增加团队总额度可提交人工额度申请。团队预算用于限制消费，不能增加可用余额。
              </p>
            </div>
          </section>
          <section className="stat-grid">
            <div className="stat-card">
              <p className="stat-label">每日预算</p>
              <p className="stat-value">
                {policy.dailyBudgetCredits
                  ? `${money(policy.dailyBudgetCredits)} credit`
                  : "不限"}
              </p>
            </div>
            <div className="stat-card">
              <p className="stat-label">每月预算</p>
              <p className="stat-value">
                {policy.monthlyBudgetCredits
                  ? `${money(policy.monthlyBudgetCredits)} credit`
                  : "不限"}
              </p>
            </div>
            <div className="stat-card">
              <p className="stat-label">可用模型</p>
              <p className="stat-value">
                {policy.allowedModelIds.length || models.length}
                <span className="unit">个</span>
              </p>
            </div>
            <div className="stat-card">
              <p className="stat-label">平台速率上限</p>
              <p className="stat-sub">
                {team.rpmLimit || "不限"} RPM · {team.tpmLimit || "不限"} TPM
                <br />
                {team.concurrencyLimit || "不限"} 并发
              </p>
            </div>
          </section>
          <div className="form-row">
            <a className="btn btn-ghost" href="#/members">
              管理成员
            </a>
            <a className="btn btn-ghost" href="#/usage">
              查看消费明细
            </a>
            <a className="btn btn-ghost" href="#/quota-requests">
              申请额度
            </a>
            <a className="btn btn-ghost" href="#/policy">
              调整预算
            </a>
          </div>
        </>
      ) : null}
      {section === "members" ? (
        <>
          <Notice k="成员访问">
            暂停或移除只影响本团队访问。成员账号、密码及其他团队权益由成员本人和平台管理。
          </Notice>
          <section className="pane">
            <div className="pane-h">
              <h2>团队成员</h2>
              <span className="readout">月预算留空不限 · 0 停止消费</span>
            </div>
            <div className="table-scroll">
              <table className="table">
                <thead>
                  <tr>
                    <th>成员</th>
                    <th>身份</th>
                    <th>状态</th>
                    <th>每月预算（credit）</th>
                    <th>操作</th>
                  </tr>
                </thead>
                <tbody>
                  {members.map((member) => (
                    <tr key={member.user.id}>
                      <td>
                        <b>{member.user.displayName || member.user.email}</b>
                        <div className="readout">{member.user.email}</div>
                      </td>
                      <td>
                        {member.role === "owner"
                          ? "所有者"
                          : member.role === "admin"
                            ? "管理员"
                            : "成员"}
                      </td>
                      <td>
                        <Chip tone={member.memberDisabledAt ? "mute" : "ok"}>
                          {member.memberDisabledAt ? "已暂停" : "正常"}
                        </Chip>
                      </td>
                      <td>
                        <div className="form-row">
                          <input
                            className="input"
                            type="number"
                            min="0"
                            step="0.000001"
                            aria-label={`${member.user.email}的月预算`}
                            value={budgets[member.user.id] ?? ""}
                            placeholder="不限"
                            onChange={(e) =>
                              setBudgets({
                                ...budgets,
                                [member.user.id]: e.target.value,
                              })
                            }
                            style={{ width: 130 }}
                          />
                          <button
                            className="btn btn-ghost btn-sm"
                            disabled={busy}
                            onClick={() =>
                              void run(
                                () =>
                                  api(
                                    `/teams/${teamId}/members/${member.user.id}`,
                                    {
                                      method: "PATCH",
                                      body: JSON.stringify({
                                        monthlyBudgetCredits:
                                          budgets[member.user.id] ?? "",
                                      }),
                                    },
                                  ),
                                "成员预算已保存",
                              )
                            }
                          >
                            保存
                          </button>
                        </div>
                      </td>
                      <td>
                        <div className="form-row">
                          <button
                            className="btn btn-ghost btn-sm"
                            disabled={
                              busy || (!isRoot && member.role !== "member")
                            }
                            onClick={() =>
                              void run(
                                () =>
                                  api(
                                    `/teams/${teamId}/members/${member.user.id}`,
                                    {
                                      method: "PATCH",
                                      body: JSON.stringify({
                                        disabled: !member.memberDisabledAt,
                                      }),
                                    },
                                  ),
                                "成员访问状态已更新",
                              )
                            }
                          >
                            {member.memberDisabledAt ? "恢复" : "暂停"}
                          </button>
                          <button
                            className="btn btn-danger btn-sm"
                            disabled={
                              busy || (!isRoot && member.role !== "member")
                            }
                            onClick={async () => {
                              const result = await confirmDialog({
                                title: `移除 ${member.user.email}？`,
                                message: "该成员将失去本团队访问权限。",
                                danger: true,
                                confirmText: "移除",
                              });
                              if (result.confirmed)
                                void run(
                                  () =>
                                    api(
                                      `/teams/${teamId}/members/${member.user.id}`,
                                      { method: "DELETE" },
                                    ),
                                  "成员已移除",
                                );
                            }}
                          >
                            移除
                          </button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
          <section className="pane">
            <div className="pane-h">
              <h2>邀请成员</h2>
              <span className="readout">邀请加入当前团队</span>
            </div>
            <div className="pane-b">
              <form
                className="form-row"
                onSubmit={(event) => {
                  event.preventDefault();
                  void run(async () => {
                    const data = await api<{
                      invitation: Invitation;
                      inviteUrl: string;
                    }>(`/teams/${teamId}/invitations`, {
                      method: "POST",
                      body: JSON.stringify({ email }),
                    });
                    setInviteUrl(
                      new URL(data.inviteUrl, window.location.origin).href,
                    );
                    setEmail("");
                  }, "邀请链接已创建");
                }}
              >
                <input
                  className="input"
                  type="email"
                  required
                  placeholder="成员邮箱"
                  aria-label="受邀成员邮箱"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                />
                <button className="btn" disabled={busy}>
                  创建邀请链接
                </button>
              </form>
              <p className="readout">
                复制链接并自行分享给受邀成员。链接绑定上述邮箱并有有效期。
              </p>
              {inviteUrl ? (
                <div className="form-row">
                  <input
                    className="input invite-url"
                    aria-label="邀请链接"
                    value={inviteUrl}
                    readOnly
                  />
                  <button
                    className="btn btn-ghost"
                    onClick={() =>
                      void navigator.clipboard
                        .writeText(inviteUrl)
                        .then(() => toastOk("邀请链接已复制"))
                        .catch(() => toastErr("复制失败，请选中链接手动复制"))
                    }
                  >
                    复制链接
                  </button>
                </div>
              ) : null}
            </div>
            <div className="table-scroll">
              <table className="table">
                <thead>
                  <tr>
                    <th>邮箱</th>
                    <th>有效期</th>
                    <th>状态</th>
                    <th>操作</th>
                  </tr>
                </thead>
                <tbody>
                  {invitations.map((item) => {
                    const expired = Date.parse(item.expiresAt) <= Date.now();
                    return (
                      <tr key={item.id}>
                        <td>{item.email}</td>
                        <td className="num">
                          {formatShanghaiTime(item.expiresAt)}
                        </td>
                        <td>
                          <Chip
                            tone={
                              item.acceptedAt
                                ? "ok"
                                : item.revokedAt || expired
                                  ? "mute"
                                  : "info"
                            }
                          >
                            {item.acceptedAt
                              ? "已接受"
                              : item.revokedAt
                                ? "已撤回"
                                : expired
                                  ? "已过期"
                                  : "待接受"}
                          </Chip>
                        </td>
                        <td>
                          {!item.acceptedAt && !item.revokedAt && !expired ? (
                            <button
                              className="btn btn-ghost btn-sm"
                              disabled={busy}
                              onClick={() =>
                                void run(
                                  () =>
                                    api(
                                      `/teams/${teamId}/invitations/${item.id}/revoke`,
                                      { method: "POST" },
                                    ),
                                  "邀请已撤回",
                                )
                              }
                            >
                              撤回
                            </button>
                          ) : (
                            "—"
                          )}
                        </td>
                      </tr>
                    );
                  })}
                  {invitations.length === 0 ? (
                    <tr>
                      <td colSpan={4} className="readout">
                        暂无邀请。
                      </td>
                    </tr>
                  ) : null}
                </tbody>
              </table>
            </div>
          </section>
        </>
      ) : null}
      {section === "policy" ? (
        <section className="pane">
          <div className="pane-h">
            <h2>消费策略</h2>
            <span className="readout">预算按团队计费口径执行</span>
          </div>
          <form
            className="pane-b tenant-form"
            onSubmit={(e) => {
              e.preventDefault();
              void run(
                () =>
                  api(`/teams/${teamId}/policy`, {
                    method: "PUT",
                    body: JSON.stringify({
                      dailyBudgetCredits: policy.dailyBudgetCredits,
                      monthlyBudgetCredits: policy.monthlyBudgetCredits,
                      allowedModelIds: policy.allowedModelIds,
                      budgetAlertPercent: policy.budgetAlertPercent,
                      rpmLimit: policy.rpmLimit,
                      tpmLimit: policy.tpmLimit,
                      concurrencyLimit: policy.concurrencyLimit,
                    }),
                  }),
                "团队策略已保存",
              );
            }}
          >
            <label>
              每日预算（credit）
              <input
                className="input"
                type="number"
                min="0"
                step="0.000001"
                placeholder="留空不限"
                value={policy.dailyBudgetCredits ?? ""}
                onChange={(e) =>
                  setPolicy({ ...policy, dailyBudgetCredits: e.target.value })
                }
              />
            </label>
            <label>
              每月预算（credit）
              <input
                className="input"
                type="number"
                min="0"
                step="0.000001"
                placeholder="留空不限"
                value={policy.monthlyBudgetCredits ?? ""}
                onChange={(e) =>
                  setPolicy({ ...policy, monthlyBudgetCredits: e.target.value })
                }
              />
            </label>
            <label>
              预算提醒阈值（%）
              <input
                className="input"
                type="number"
                min="1"
                max="100"
                required
                value={policy.budgetAlertPercent}
                onChange={(e) =>
                  setPolicy({
                    ...policy,
                    budgetAlertPercent: Number(e.target.value),
                  })
                }
              />
            </label>
            <p className="readout">
              预算留空表示不额外限制，0 表示停止消费；团队总额度仍由平台控制。
            </p>
            <fieldset>
              <legend>团队速率限制</legend>
              <p className="readout">
                0
                继承平台上限。实际取团队限制与平台上限中较小的非零值，无法放大平台上限；平台上限为不限时，可设置团队限制。
              </p>
              <p className="readout">
                平台上限：{team?.rpmLimit || "不限"} RPM ·{" "}
                {team?.tpmLimit || "不限"} TPM ·{" "}
                {team?.concurrencyLimit || "不限"} 并发
              </p>
              <div className="tenant-rate-limits">
                {(
                  [
                    ["rpmLimit", "每分钟请求数（RPM）"],
                    ["tpmLimit", "每分钟 Token（TPM）"],
                    ["concurrencyLimit", "同时请求数"],
                  ] as const
                ).map(([field, label]) => (
                  <label key={field}>
                    {label}
                    <input
                      className="input"
                      type="number"
                      min="0"
                      max="1000000"
                      step="1"
                      required
                      value={policy[field]}
                      onChange={(event) =>
                        setPolicy({
                          ...policy,
                          [field]: Number(event.target.value),
                        })
                      }
                    />
                  </label>
                ))}
              </div>
            </fieldset>
            <fieldset>
              <legend>模型白名单</legend>
              <p className="readout">
                未勾选模型时允许所有平台开放模型；勾选后仅允许所选模型。
              </p>
              <div className="model-checkboxes">
                {models.map((model) => (
                  <label key={model.modelId}>
                    <input
                      type="checkbox"
                      checked={policy.allowedModelIds.includes(model.modelId)}
                      onChange={(e) =>
                        setPolicy({
                          ...policy,
                          allowedModelIds: e.target.checked
                            ? [...policy.allowedModelIds, model.modelId]
                            : policy.allowedModelIds.filter(
                                (id) => id !== model.modelId,
                              ),
                        })
                      }
                    />
                    {model.displayName || model.modelId}
                  </label>
                ))}
              </div>
            </fieldset>
            <button className="btn" disabled={busy}>
              {busy ? "保存中…" : "保存消费策略"}
            </button>
          </form>
        </section>
      ) : null}
      {section === "team-settings" ? (
        <section className="pane">
          <div className="pane-h">
            <h2>团队资料</h2>
          </div>
          <form
            className="pane-b tenant-form"
            onSubmit={(e) => {
              e.preventDefault();
              void run(async () => {
                await api(`/teams/${teamId}`, {
                  method: "PATCH",
                  body: JSON.stringify({ name }),
                });
                await refreshSession();
              }, "团队名称已更新");
            }}
          >
            <label>
              团队名称
              <input
                className="input"
                required
                maxLength={120}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
            <p className="readout">
              额度、管理员任免与平台速率上限由平台管理员管理。
            </p>
            <button className="btn" disabled={busy}>
              保存团队资料
            </button>
          </form>
        </section>
      ) : null}
    </div>
  );
}

export function CatalogPage() {
  const [models, setModels] = useState<Model[]>([]);
  const [error, setError] = useState("");
  useEffect(() => {
    api<{ models: Model[] }>("/models")
      .then((data) => setModels(data.models))
      .catch((e) => setError(errorMessage(e)));
  }, []);
  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">计量 / 模型与售价</p>
          <h1>可用模型</h1>
        </div>
        <span className="readout">售价只读 · credit / 百万 Token</span>
      </header>
      {error ? <Notice tone="stop">{error}</Notice> : null}
      <Notice k="计费说明">
        消费按请求发生时的适用售价扣减。缓存读写单独计费；空闲时段倍率作用于相应售价。
      </Notice>
      <section className="pane">
        <div className="table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th>模型</th>
                <th>输入售价</th>
                <th>输出售价</th>
                <th>缓存读</th>
                <th>缓存写</th>
                <th>空闲倍率</th>
              </tr>
            </thead>
            <tbody>
              {models.map((model) => (
                <tr key={model.modelId}>
                  <td>
                    <b>{model.displayName}</b>
                    <div className="readout">{model.modelId}</div>
                  </td>
                  <td className="num">{money(model.inputPricePerMtok)}</td>
                  <td className="num">{money(model.outputPricePerMtok)}</td>
                  <td className="num">{money(model.cacheReadPricePerMtok)}</td>
                  <td className="num">{money(model.cacheWritePricePerMtok)}</td>
                  <td className="num">{model.offPeakMultiplier || "1"}×</td>
                </tr>
              ))}
              {models.length === 0 ? (
                <tr>
                  <td colSpan={6} className="readout">
                    当前暂无可用模型。
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

export function ProfilePage() {
  const { user, isRoot, refreshSession } = useAuth();
  const [displayName, setDisplayName] = useState(user.displayName);
  const [currentPassword, setCurrentPassword] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const save = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    try {
      await api("/profile", {
        method: "PATCH",
        body: JSON.stringify({
          displayName,
          ...(password ? { currentPassword, password } : {}),
        }),
      });
      await refreshSession();
      setCurrentPassword("");
      setPassword("");
      toastOk(
        password ? "密码已更新，其他会话及设备凭据已撤销" : "个人资料已保存",
      );
    } catch (e) {
      toastErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">账户 / 安全</p>
          <h1>个人设置</h1>
        </div>
      </header>
      <section className="pane">
        <div className="pane-h">
          <h2>个人资料与密码</h2>
        </div>
        <form className="pane-b tenant-form" onSubmit={(e) => void save(e)}>
          <label>
            邮箱
            <input className="input" readOnly value={user.email} />
          </label>
          <label>
            显示名称
            <input
              className="input"
              required
              maxLength={120}
              autoComplete="name"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
            />
          </label>
          <label>
            当前密码
            <input
              className="input"
              type="password"
              autoComplete="current-password"
              required={Boolean(password)}
              value={currentPassword}
              onChange={(e) => setCurrentPassword(e.target.value)}
            />
          </label>
          <label>
            新密码
            <input
              className="input"
              type="password"
              minLength={isRoot ? 12 : 10}
              autoComplete="new-password"
              placeholder="留空则不修改密码"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </label>
          <p className="readout">
            修改密码需要当前密码，并会撤销其他登录会话和已有设备凭据；设备需重新登录。
          </p>
          <button className="btn" disabled={busy}>
            {busy ? "保存中…" : "保存"}
          </button>
        </form>
      </section>
      {isRoot ? <MfaSettings /> : null}
    </div>
  );
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
        <h1>接受团队邀请</h1>
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
