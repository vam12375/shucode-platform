import { useEffect, useRef, useState } from "react";
import { api } from "../api.js";
import { confirmDialog, toastOk } from "../components/Feedback.js";
import { Chip, Notice } from "../components/StatusChip.js";

interface SourceParserState {
  configured: boolean;
  provider: "snapany";
  maxBytes: number;
  maxSeconds: number;
  source: "console" | "environment" | "none";
  environmentConfigured: boolean;
  canConfigure: boolean;
}

export function SourceParserStatus() {
  const [state, setState] = useState<SourceParserState | null>(null);
  const [error, setError] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [busy, setBusy] = useState<"save" | "clear" | null>(null);
  const actionPending = useRef(false);
  useEffect(() => {
    let active = true;
    api<SourceParserState>("/media/source-parser").then(
      (value) => {
        if (active) setState(value);
      },
      (reason: unknown) => {
        if (active) {
          setError(reason instanceof Error ? reason.message : "状态读取失败");
        }
      },
    );
    return () => {
      active = false;
    };
  }, []);

  const save = async (): Promise<void> => {
    const value = apiKey.trim();
    if (!state?.canConfigure || !value || actionPending.current) return;
    actionPending.current = true;
    setBusy("save");
    setError("");
    try {
      const next = await api<SourceParserState>("/media/source-parser", {
        method: "PUT",
        body: JSON.stringify({ apiKey: value }),
      });
      setState(next);
      setApiKey("");
      toastOk("SnapAny API Key 已保存，新的链接解析将使用此配置", "已保存");
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "保存失败");
    } finally {
      actionPending.current = false;
      setBusy(null);
    }
  };

  const clear = async (): Promise<void> => {
    if (state?.source !== "console" || actionPending.current) return;
    actionPending.current = true;
    setBusy("clear");
    try {
      const answer = await confirmDialog({
        k: "SnapAny 配置",
        title: "清除后台保存的 API Key？",
        message: state.environmentConfigured
          ? "清除后，新的链接解析将使用服务器环境中的 SNAPANY_API_KEY。"
          : "清除后，视频分享链接解析将停用；用户仍可上传原片。",
        confirmText: "清除密钥",
        danger: true,
      });
      if (!answer.confirmed) return;
      setError("");
      const next = await api<SourceParserState>("/media/source-parser", {
        method: "DELETE",
      });
      setState(next);
      setApiKey("");
      toastOk("后台保存的 SnapAny API Key 已清除", "已清除");
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "清除失败");
    } finally {
      actionPending.current = false;
      setBusy(null);
    }
  };

  if (!state) {
    return error ? (
      <Notice tone="stop" k="原片解析">{error}</Notice>
    ) : (
      <Notice k="原片解析">正在读取解析服务状态…</Notice>
    );
  }

  return (
    <section className="pane" aria-label="SnapAny 原片解析配置">
      <div className="pane-h">
        <h2>原片解析 · SnapAny</h2>
        <Chip tone={state.configured ? "ok" : "mute"}>
          {state.configured ? "已配置" : "未配置"}
        </Chip>
      </div>
      <div className="pane-b">
        <p className="readout">
          {state.source === "console"
            ? "当前使用后台保存的 API Key。"
            : state.source === "environment"
              ? "当前使用服务器环境中的 SNAPANY_API_KEY。"
              : "尚未配置 API Key，用户可上传原片。"}
          {` 原片限制 ${state.maxBytes / (1024 * 1024)} MiB、${state.maxSeconds} 秒。`}
        </p>
        {error ? <Notice tone="stop" k="配置操作失败">{error}</Notice> : null}
        {!state.canConfigure ? (
          <Notice k="暂不可保存">
            请先在 API 服务配置 PLATFORM_SECRET_KEY，以便加密保存解析密钥。
          </Notice>
        ) : null}
        <form
          onSubmit={(event) => {
            event.preventDefault();
            void save();
          }}
        >
          <label className="field">
            <span className="field-label">SnapAny API Key</span>
            <input
              className="input num"
              type="password"
              autoComplete="new-password"
              spellCheck={false}
              maxLength={4096}
              placeholder={state.source === "console" ? "输入新密钥以替换已保存配置" : "输入 SnapAny API Key"}
              value={apiKey}
              disabled={!state.canConfigure || busy !== null}
              onChange={(event) => setApiKey(event.target.value)}
            />
            <span className="readout">密钥加密保存，保存后不再显示；后台配置优先于服务器环境配置。</span>
          </label>
          <div className="form-row" style={{ marginTop: 12 }}>
            <button
              className="btn"
              type="submit"
              disabled={!state.canConfigure || !apiKey.trim() || busy !== null}
            >
              {busy === "save" ? "保存中…" : state.source === "console" ? "更新密钥" : "保存密钥"}
            </button>
            {state.source === "console" ? (
              <button
                className="btn btn-danger"
                type="button"
                disabled={busy !== null}
                onClick={() => void clear()}
              >
                {busy === "clear" ? "清除中…" : "清除后台密钥"}
              </button>
            ) : null}
          </div>
        </form>
      </div>
    </section>
  );
}
