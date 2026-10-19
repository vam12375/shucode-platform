import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "../api.js";
import { useAuth } from "../auth.js";
import { confirmDialog, toastErr, toastOk } from "./Feedback.js";
import { Chip, Notice } from "./StatusChip.js";

interface MfaStatus {
  enabled: boolean;
  recoveryCodesRemaining: number;
}
export function MfaSettings() {
  const { refreshSession } = useAuth();
  const [status, setStatus] = useState<MfaStatus | null>(null);
  const [currentPassword, setCurrentPassword] = useState("");
  const [code, setCode] = useState("");
  const [recoveryCode, setRecoveryCode] = useState("");
  const [setup, setSetup] = useState<{
    secret: string;
    otpauthUrl: string;
  } | null>(null);
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    try {
      setStatus(await api<MfaStatus>("/security/mfa"));
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : "无法读取双重验证设置");
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);
  const run = async (action: () => Promise<void>) => {
    setBusy(true);
    setError("");
    try {
      await action();
      setCode("");
      setRecoveryCode("");
      setCurrentPassword("");
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "操作失败");
    } finally {
      setBusy(false);
    }
  };
  const beginSetup = async (event: FormEvent) => {
    event.preventDefault();
    await run(async () => {
      setRecoveryCodes([]);
      setSetup(
        await api<{ secret: string; otpauthUrl: string }>(
          "/security/mfa/setup",
          { method: "POST", body: JSON.stringify({ currentPassword }) },
        ),
      );
    });
  };
  const enable = async (event: FormEvent) => {
    event.preventDefault();
    await run(async () => {
      const data = await api<{ recoveryCodes: string[] }>(
        "/security/mfa/enable",
        { method: "POST", body: JSON.stringify({ code }) },
      );
      setSetup(null);
      setRecoveryCodes(data.recoveryCodes);
      await refreshSession();
      toastOk("双重验证已启用，请保存恢复码");
    });
  };
  const change = async (action: "disable" | "recovery-codes") => {
    const result = await confirmDialog({
      title: action === "disable" ? "关闭双重验证？" : "重新生成恢复码？",
      message:
        action === "disable"
          ? "关闭后，下次登录只需密码。"
          : "此前的恢复码将全部失效，请保存新生成的恢复码。",
      danger: true,
      confirmText: action === "disable" ? "关闭" : "重新生成",
    });
    if (!result.confirmed) return;
    await run(async () => {
      const data = await api<{ recoveryCodes?: string[] }>(
        `/security/mfa/${action}`,
        {
          method: "POST",
          body: JSON.stringify({
            currentPassword,
            ...(code ? { code } : {}),
            ...(recoveryCode ? { recoveryCode } : {}),
          }),
        },
      );
      setRecoveryCodes(data.recoveryCodes ?? []);
      await refreshSession();
      toastOk(action === "disable" ? "双重验证已关闭" : "恢复码已重新生成");
    });
  };
  const copy = (value: string) =>
    void navigator.clipboard
      .writeText(value)
      .then(() => toastOk("已复制，请妥善保存"))
      .catch(() => toastErr("复制失败，请手动选择并复制"));
  return (
    <section className="pane">
      <div className="pane-h">
        <h2>超级管理员双重验证</h2>
        {status ? (
          <Chip tone={status.enabled ? "ok" : "hot"}>
            {status.enabled ? "已启用" : "未启用"}
          </Chip>
        ) : null}
      </div>
      <div className="pane-b tenant-form">
        {error ? (
          <Notice tone="stop" k="操作未完成">
            {error}
          </Notice>
        ) : null}
        <p className="readout">
          使用身份验证器生成动态验证码。恢复码用于无法访问验证器时登录，每个恢复码只能使用一次。
        </p>
        {recoveryCodes.length > 0 ? (
          <div className="mfa-sensitive">
            <Notice k="恢复码仅展示一次">
              请保存到安全位置，刷新或离开页面后将无法再次查看。请勿分享密钥、验证码或恢复码。
            </Notice>
            <pre>{recoveryCodes.join("\n")}</pre>
            <div className="form-row">
              <button
                className="btn btn-ghost"
                type="button"
                onClick={() => copy(recoveryCodes.join("\n"))}
              >
                复制恢复码
              </button>
              <button
                className="btn btn-ghost"
                type="button"
                onClick={() => setRecoveryCodes([])}
              >
                已安全保存，隐藏
              </button>
            </div>
          </div>
        ) : null}
        {status && !status.enabled && !setup ? (
          <form
            className="tenant-form"
            onSubmit={(event) => void beginSetup(event)}
          >
            <label>
              当前密码
              <input
                className="input"
                type="password"
                autoComplete="current-password"
                required
                value={currentPassword}
                onChange={(e) => setCurrentPassword(e.target.value)}
              />
            </label>
            <button className="btn" disabled={busy}>
              开始设置双重验证
            </button>
          </form>
        ) : null}
        {setup ? (
          <form
            className="tenant-form"
            onSubmit={(event) => void enable(event)}
          >
            <Notice k="配置验证器">
              将下方密钥手动录入验证器，或复制 otpauth
              链接导入；随后填写验证器显示的 6 位验证码以确认启用。
            </Notice>
            <div className="mfa-sensitive">
              <p className="readout">验证器密钥</p>
              <code>{setup.secret}</code>
              <div className="form-row">
                <button
                  className="btn btn-ghost btn-sm"
                  type="button"
                  onClick={() => copy(setup.secret)}
                >
                  复制密钥
                </button>
                <button
                  className="btn btn-ghost btn-sm"
                  type="button"
                  onClick={() => copy(setup.otpauthUrl)}
                >
                  复制 otpauth 链接
                </button>
              </div>
            </div>
            <label>
              验证器验证码
              <input
                className="input"
                type="text"
                inputMode="numeric"
                pattern="[0-9]{6}"
                autoComplete="one-time-code"
                maxLength={6}
                required
                value={code}
                onChange={(e) => setCode(e.target.value)}
              />
            </label>
            <div className="form-row">
              <button className="btn" disabled={busy}>
                确认启用
              </button>
              <button
                className="btn btn-ghost"
                type="button"
                disabled={busy}
                onClick={() => {
                  setSetup(null);
                  setCode("");
                }}
              >
                取消
              </button>
            </div>
          </form>
        ) : null}
        {status?.enabled ? (
          <form
            className="tenant-form"
            onSubmit={(event) => event.preventDefault()}
          >
            <p className="readout">
              剩余恢复码：{status.recoveryCodesRemaining} 个。
            </p>
            <label>
              当前密码
              <input
                className="input"
                type="password"
                autoComplete="current-password"
                required
                value={currentPassword}
                onChange={(e) => setCurrentPassword(e.target.value)}
              />
            </label>
            <label>
              动态验证码
              <input
                className="input"
                type="text"
                inputMode="numeric"
                autoComplete="one-time-code"
                maxLength={6}
                value={code}
                onChange={(e) => setCode(e.target.value)}
                placeholder="验证码与恢复码任选一项"
              />
            </label>
            <label>
              恢复码
              <input
                className="input"
                type="text"
                autoComplete="off"
                value={recoveryCode}
                onChange={(e) => setRecoveryCode(e.target.value)}
              />
            </label>
            <div className="form-row">
              <button
                className="btn btn-ghost"
                type="button"
                disabled={busy || !currentPassword || (!code && !recoveryCode)}
                onClick={() => void change("recovery-codes")}
              >
                重新生成恢复码
              </button>
              <button
                className="btn btn-danger"
                type="button"
                disabled={busy || !currentPassword || (!code && !recoveryCode)}
                onClick={() => void change("disable")}
              >
                关闭双重验证
              </button>
            </div>
          </form>
        ) : null}
      </div>
    </section>
  );
}
