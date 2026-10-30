import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Save, SlidersHorizontal } from "lucide-react";
import { api } from "../api.js";
import { toastErr, toastOk } from "../components/Feedback.js";
import { Glyph, PageTitle } from "../components/Icon.js";
import { Notice } from "../components/StatusChip.js";

interface PlatformConfig {
  help: {
    community_urls?: { "zh-CN"?: string; "en-US"?: string };
    feedback_url?: string;
    feedback_use_external_form?: boolean;
  } | null;
  forceUpdate: { minimalVersion?: string } | null;
  releaseManifestEnabled: boolean;
}

function FieldRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="form-row" style={{ marginBottom: 8 }}>
      <span style={{ width: 120 }} className="readout">
        {label}
      </span>
      {children}
    </div>
  );
}

export function ConfigPage() {
  const [feedbackUrl, setFeedbackUrl] = useState("");
  const [communityZh, setCommunityZh] = useState("");
  const [communityEn, setCommunityEn] = useState("");
  const [feedbackExternalForm, setFeedbackExternalForm] = useState(false);
  const [minimalVersion, setMinimalVersion] = useState("");
  const [releaseManifestEnabled, setReleaseManifestEnabled] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const data = await api<PlatformConfig>("/config");
      setFeedbackUrl(data.help?.feedback_url ?? "");
      setCommunityZh(data.help?.community_urls?.["zh-CN"] ?? "");
      setCommunityEn(data.help?.community_urls?.["en-US"] ?? "");
      setFeedbackExternalForm(data.help?.feedback_use_external_form === true);
      setReleaseManifestEnabled(data.releaseManifestEnabled === true);
      setMinimalVersion(
        data.releaseManifestEnabled ? (data.forceUpdate?.minimalVersion ?? "") : "",
      );
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
          forceUpdate:
            releaseManifestEnabled && minimalVersion.trim()
              ? { minimalVersion: minimalVersion.trim() }
              : null,
        }),
      });
      await load();
      toastOk("已保存，客户端约 1 小时内生效", "平台配置");
    } catch (saveError) {
      toastErr(saveError instanceof Error ? saveError.message : "保存失败", "保存失败");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">下发 / 平台参数</p>
          <PageTitle icon={SlidersHorizontal}>平台配置</PageTitle>
        </div>
      </header>
      <p className="readout" style={{ margin: 0, marginBottom: 18 }}>
        这些字段随公开配置下发，全环境可见。强制更新版本号会被客户端启动 gate 校验：低于该值的客户端将被要求更新，请谨慎填写。
      </p>

      {error ? (
        <Notice tone="stop" k="加载失败">
          {error}
        </Notice>
      ) : null}

      <section className="pane">
        <div className="pane-h">
          <h2>帮助与社区</h2>
          <span className="readout">随公开配置下发</span>
        </div>
        <div className="pane-b">
          <FieldRow label="反馈链接">
            <input
              className="input grow"
              value={feedbackUrl}
              onChange={(event) => setFeedbackUrl(event.target.value)}
              placeholder="https://..."
            />
          </FieldRow>
          <FieldRow label="社区（中文）">
            <input
              className="input grow"
              value={communityZh}
              onChange={(event) => setCommunityZh(event.target.value)}
              placeholder="https://..."
            />
          </FieldRow>
          <FieldRow label="社区（英文）">
            <input
              className="input grow"
              value={communityEn}
              onChange={(event) => setCommunityEn(event.target.value)}
              placeholder="https://..."
            />
          </FieldRow>
          <div className="form-row">
            <span style={{ width: 120 }} className="readout" />
            <label className="form-row" style={{ gap: 8 }}>
              <span className="switch">
                <input
                  type="checkbox"
                  aria-label="反馈使用外部表单"
                  checked={feedbackExternalForm}
                  onChange={(event) => setFeedbackExternalForm(event.target.checked)}
                />
                <i />
              </span>
              <span style={{ fontSize: 13 }}>反馈使用外部表单</span>
            </label>
          </div>
        </div>
      </section>

      <section className="pane d1">
        <div className="pane-h">
          <h2>强制更新</h2>
        </div>
        <div className="pane-b">
          <FieldRow label="最低版本">
            <input
              className="input num"
              style={{ width: 180, textAlign: "left" }}
              value={minimalVersion}
              onChange={(event) => setMinimalVersion(event.target.value)}
              placeholder="最低版本，如 0.19.12"
              disabled={!releaseManifestEnabled}
            />
          </FieldRow>
          <p className="readout" style={{ margin: 0 }}>
            {releaseManifestEnabled
              ? "留空表示不强制更新。强制更新会让低于该版本的客户端在启动时被要求升级。"
              : "当前未接入安装包分发，自动更新清单仍返回“无更新”，因此强制更新已禁用；保存其他配置时也会清空已填写的版本号。"}
          </p>
        </div>
      </section>

      <div className="form-row">
        <button className="btn" disabled={busy} onClick={() => void save()}>
          <Glyph icon={Save} size={14} strokeWidth={2} />
          {busy ? "保存中…" : "保存"}
        </button>
      </div>
    </div>
  );
}
