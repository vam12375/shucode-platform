import { useCallback, useEffect, useState } from "react";
import { api } from "../api.js";

interface ReleaseRow {
  revision: number;
  publishedAt: string;
  isActive: boolean;
}

export function ReleasesPage() {
  const [releases, setReleases] = useState<ReleaseRow[]>([]);
  const [content, setContent] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const data = await api<{ releases: ReleaseRow[] }>("/releases");
      setReleases(data.releases);
      setError(null);
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载失败");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const publish = async (): Promise<void> => {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const result = await api<{ revision: number }>("/releases", {
        method: "POST",
        body: JSON.stringify({ content }),
      });
      setNotice(`已发布 revision ${result.revision} 并设为生效版本`);
      setContent("");
      await load();
    } catch (publishError) {
      setError(publishError instanceof Error ? publishError.message : "发布失败");
    } finally {
      setBusy(false);
    }
  };

  const onPickFile = async (file: File | undefined): Promise<void> => {
    if (!file) return;
    setContent(await file.text());
  };

  return (
    <div>
      <h1>内置配置发布</h1>
      <p className="muted">
        发布客户端内置 Provider/模型规则（Built-in Release）。客户端约 1 小时内拉取生效；
        内容必须是客户端可解析的完整配置，发布前请用客户端的配置目录文件校验（推荐直接以当前
        <span className="mono"> config/provider/shucode-builtin.json </span>
        为模板修改）。错误内容会被客户端整份拒绝并回落旧缓存。
      </p>
      {error ? <p className="error">{error}</p> : null}
      {notice ? <p className="muted">{notice}</p> : null}

      <div className="card">
        <h2 style={{ marginTop: 0 }}>发布新版本</h2>
        <div className="row" style={{ marginBottom: 8 }}>
          <input
            type="file"
            accept="application/json"
            onChange={(event) => void onPickFile(event.target.files?.[0])}
          />
        </div>
        <textarea
          placeholder='粘贴内置配置 JSON（{"schemaVersion":1,"revision":N,"config":{...}}）'
          value={content}
          onChange={(event) => setContent(event.target.value)}
        />
        <div className="row" style={{ marginTop: 10 }}>
          <button disabled={busy || !content.trim()} onClick={() => void publish()}>
            {busy ? "发布中…" : "发布并生效"}
          </button>
        </div>
      </div>

      <div className="card">
        <h2 style={{ marginTop: 0 }}>版本历史</h2>
        <table>
          <thead>
            <tr>
              <th>Revision</th>
              <th>发布时间</th>
              <th>状态</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {releases.map((release) => (
              <tr key={release.revision}>
                <td className="mono">{release.revision}</td>
                <td className="mono">{release.publishedAt.replace("T", " ").slice(0, 19)}</td>
                <td>{release.isActive ? "生效中" : "历史"}</td>
                <td style={{ textAlign: "right" }}>
                  {release.isActive ? null : (
                    <button
                      className="secondary"
                      disabled={busy}
                      onClick={() =>
                        void (async () => {
                          await api(`/releases/${release.revision}/activate`, { method: "POST" })
                            .then(load)
                            .catch((activateError: unknown) =>
                              setError(
                                activateError instanceof Error ? activateError.message : "操作失败",
                              ),
                            );
                        })()
                      }
                    >
                      切换到此版本
                    </button>
                  )}
                </td>
              </tr>
            ))}
            {releases.length === 0 ? (
              <tr>
                <td colSpan={4} className="muted">
                  暂无发布记录。
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>
    </div>
  );
}
