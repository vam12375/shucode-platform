import { useCallback, useEffect, useState } from "react";
import { api } from "../api.js";
import { toastErr, toastOk } from "../components/Feedback.js";
import { Chip, Notice } from "../components/StatusChip.js";
import { formatShanghaiTime } from "../time.js";

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
    try {
      const result = await api<{ revision: number }>("/releases", {
        method: "POST",
        body: JSON.stringify({ content }),
      });
      setContent("");
      await load();
      toastOk(`已发布 revision ${result.revision} 并设为生效版本`, "发布完成");
    } catch (publishError) {
      toastErr(publishError instanceof Error ? publishError.message : "发布失败", "发布失败");
    } finally {
      setBusy(false);
    }
  };

  const activate = async (revision: number): Promise<void> => {
    setBusy(true);
    try {
      await api(`/releases/${revision}/activate`, { method: "POST" });
      await load();
      toastOk(`已切换到 R${revision}，客户端约 1 小时内拉取生效`, "已生效");
    } catch (activateError) {
      toastErr(activateError instanceof Error ? activateError.message : "操作失败");
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
      <header className="page-head">
        <div>
          <p className="eyebrow">下发 / 版本</p>
          <h1>内置配置发布</h1>
        </div>
        <span className="readout">生效版本 · {releases.find((r) => r.isActive)?.revision ?? "—"}</span>
      </header>
      <p className="readout" style={{ margin: 0, marginBottom: 18 }}>
        发布客户端内置 Provider/模型规则（Built-in Release）；客户端约 1 小时内拉取生效。内容必须是客户端可解析的完整配置，推荐以
        config/provider/shucode-builtin.json 为模板修改；错误内容会被客户端整份拒绝并回落旧缓存。
      </p>

      {error ? (
        <Notice tone="stop" k="加载失败">
          {error}
        </Notice>
      ) : null}

      <section className="pane">
        <div className="pane-h">
          <h2>发布新版本</h2>
        </div>
        <div className="pane-b">
          <div className="form-row">
            <input className="input grow" type="file" accept="application/json" onChange={(event) => void onPickFile(event.target.files?.[0])} />
          </div>
          <textarea
            placeholder={'粘贴内置配置 JSON（{"schemaVersion":1,"revision":N,"config":{...}}）'}
            value={content}
            onChange={(event) => setContent(event.target.value)}
          />
          <div className="form-row" style={{ marginTop: 10 }}>
            <button className="btn" disabled={busy || !content.trim()} onClick={() => void publish()}>
              {busy ? "发布中…" : "发布并生效"}
            </button>
          </div>
        </div>
      </section>

      <section className="pane d1">
        <div className="pane-h">
          <h2>版本历史</h2>
          <span className="readout">{releases.length} 个版本</span>
        </div>
        <div className="pane-b flush table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col" className="num">
                  Revision
                </th>
                <th scope="col" className="num">
                  发布时间
                </th>
                <th scope="col">状态</th>
                <th scope="col" aria-label="操作" />
              </tr>
            </thead>
            <tbody>
              {releases.map((release) => (
                <tr key={release.revision}>
                  <td className="num">R{release.revision}</td>
                  <td className="num">{formatShanghaiTime(release.publishedAt)}</td>
                  <td>{release.isActive ? <Chip tone="ok">生效中</Chip> : <Chip tone="mute">历史</Chip>}</td>
                  <td>
                    <div className="form-row" style={{ justifyContent: "flex-end" }}>
                      {release.isActive ? null : (
                        <button
                          className="btn btn-ghost btn-sm"
                          disabled={busy}
                          onClick={() => void activate(release.revision)}
                        >
                          切换到此版本
                        </button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
              {releases.length === 0 ? (
                <tr>
                  <td colSpan={4} className="readout">
                    暂无发布记录。
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
