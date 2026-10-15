import { useCallback, useEffect, useState } from "react";
import { api } from "../api.js";

interface PlatformConfig {
  help: {
    community_urls?: { "zh-CN"?: string; "en-US"?: string };
    feedback_url?: string;
    feedback_use_external_form?: boolean;
  } | null;
  forceUpdate: { minimalVersion?: string } | null;
}

export function ConfigPage() {
  const [feedbackUrl, setFeedbackUrl] = useState("");
  const [communityZh, setCommunityZh] = useState("");
  const [communityEn, setCommunityEn] = useState("");
  const [feedbackExternalForm, setFeedbackExternalForm] = useState(false);
  const [minimalVersion, setMinimalVersion] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const data = await api<PlatformConfig>("/config");
      setFeedbackUrl(data.help?.feedback_url ?? "");
      setCommunityZh(data.help?.community_urls?.["zh-CN"] ?? "");
      setCommunityEn(data.help?.community_urls?.["en-US"] ?? "");
      setFeedbackExternalForm(data.help?.feedback_use_external_form === true);
      setMinimalVersion(data.forceUpdate?.minimalVersion ?? "");
      setError(null);
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载失败");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const save = async (): Promise<void> => {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      await api("/config", {
        method: "PUT",
        body: JSON.stringify({
          help: {
            feedback_url: feedbackUrl.trim() || undefined,
            community_urls: {
              "zh-CN": communityZh.trim() || undefined,
              "en-US": communityEn.trim() || undefined,
            },
            feedback_use_external_form: feedbackExternalForm,
          },
          forceUpdate: minimalVersion.trim() ? { minimalVersion: minimalVersion.trim() } : null,
        }),
      });
      setNotice("已保存，客户端约 1 小时内生效");
      await load();
    } catch (saveError) {
      setError(saveError instanceof Error ? saveError.message : "保存失败");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <h1>平台配置</h1>
      <p className="muted">
        这些字段随公开配置（/api/v1/client/configs）下发，全环境可见。强制更新版本号会被客户端启动
        gate 校验：版本低于该值的客户端将被要求更新，请谨慎填写。
      </p>
      {error ? <p className="error">{error}</p> : null}
      {notice ? <p className="muted">{notice}</p> : null}

      <div className="card">
        <h2 style={{ marginTop: 0 }}>帮助与社区</h2>
        <div className="row" style={{ marginBottom: 8 }}>
          <span style={{ width: 120 }} className="muted">
            反馈链接
          </span>
          <input
            className="grow"
            value={feedbackUrl}
            onChange={(event) => setFeedbackUrl(event.target.value)}
            placeholder="https://..."
          />
        </div>
        <div className="row" style={{ marginBottom: 8 }}>
          <span style={{ width: 120 }} className="muted">
            社区（中文）
          </span>
          <input
            className="grow"
            value={communityZh}
            onChange={(event) => setCommunityZh(event.target.value)}
            placeholder="https://..."
          />
        </div>
        <div className="row" style={{ marginBottom: 8 }}>
          <span style={{ width: 120 }} className="muted">
            社区（英文）
          </span>
          <input
            className="grow"
            value={communityEn}
            onChange={(event) => setCommunityEn(event.target.value)}
            placeholder="https://..."
          />
        </div>
        <label className="row" style={{ gap: 6 }}>
          <input
            type="checkbox"
            checked={feedbackExternalForm}
            onChange={(event) => setFeedbackExternalForm(event.target.checked)}
          />
          反馈使用外部表单
        </label>
      </div>

      <div className="card">
        <h2 style={{ marginTop: 0 }}>强制更新</h2>
        <div className="row">
          <input
            style={{ width: 180 }}
            value={minimalVersion}
            onChange={(event) => setMinimalVersion(event.target.value)}
            placeholder="最低版本，如 0.19.12"
          />
        </div>
        <p className="muted" style={{ marginBottom: 0 }}>
          留空表示不强制更新。注意：当前平台未托管安装包，自动更新清单返回“无更新”，
          填写该值前请确认发行流程已就绪（见 README「桌面更新」一节）。
        </p>
      </div>

      <button disabled={busy} onClick={() => void save()}>
        {busy ? "保存中…" : "保存"}
      </button>
    </div>
  );
}
