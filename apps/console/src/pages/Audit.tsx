import { useCallback, useEffect, useState } from "react";
import { api } from "../api.js";
import { Notice } from "../components/StatusChip.js";
import { formatShanghaiTime } from "../time.js";

interface AuditLogRow {
  id: string;
  action: string;
  targetType: string | null;
  targetId: string | null;
  detail: unknown;
  createdAt: string;
  actorEmail: string | null;
}

interface AuditPageData {
  logs: AuditLogRow[];
  nextCursor: string | null;
}

/** 动作标签：资源.动作 → 中文；未收录的动作原样展示（新埋点不必先改此处）。 */
const ACTION_LABELS: Record<string, string> = {
  "user.create": "创建用户",
  "user.update": "更新用户",
  "team.create": "创建团队",
  "team.update": "更新团队",
  "provider.create": "创建供应商",
  "provider.update": "更新供应商",
  "provider.delete": "删除供应商",
  "provider.test": "测试供应商",
  "model.upsert": "保存模型",
  "model.import": "导入模型",
  "model.delete": "删除模型",
  "pricing.update": "更新定价库",
  "release.publish": "发布配置",
  "release.activate": "切换配置版本",
  "device.revoke": "吊销设备",
};

export function AuditPage() {
  const [logs, setLogs] = useState<AuditLogRow[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [moreLoading, setMoreLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const fetchLogs = useCallback((cursor: string | null) => {
    const params = new URLSearchParams();
    if (cursor) params.set("cursor", cursor);
    return api<AuditPageData>(`/audit${params.size > 0 ? `?${params.toString()}` : ""}`);
  }, []);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const data = await fetchLogs(null);
      setLogs(data.logs);
      setNextCursor(data.nextCursor);
      setError(null);
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载失败");
    } finally {
      setLoading(false);
    }
  }, [fetchLogs]);

  useEffect(() => {
    void load();
  }, [load]);

  const loadMore = useCallback(async () => {
    if (!nextCursor || moreLoading) return;
    setMoreLoading(true);
    try {
      const data = await fetchLogs(nextCursor);
      setLogs((previous) => [...previous, ...data.logs]);
      setNextCursor(data.nextCursor);
      setError(null);
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载失败");
    } finally {
      setMoreLoading(false);
    }
  }, [fetchLogs, nextCursor, moreLoading]);

  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">治理 / 审计</p>
          <h1>审计日志</h1>
        </div>
        <span className="readout m0">管理动作留痕 · 不含凭据与请求正文</span>
      </header>

      {error ? (
        <Notice tone="stop" k="加载失败">
          {error}
        </Notice>
      ) : null}

      <section className="pane">
        <div className="pane-h">
          <h2>最近动作</h2>
          <span className="readout">每页 50 条 · 时间倒序</span>
        </div>
        <div className="pane-b flush table-scroll vscroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">时间</th>
                <th scope="col">操作者</th>
                <th scope="col">动作</th>
                <th scope="col">目标</th>
                <th scope="col">详情</th>
              </tr>
            </thead>
            <tbody>
              {logs.map((log) => (
                <tr key={log.id}>
                  <td className="num">{formatShanghaiTime(log.createdAt)}</td>
                  <td>{log.actorEmail ?? "—"}</td>
                  <td>{ACTION_LABELS[log.action] ?? log.action}</td>
                  <td className="num">
                    {log.targetType}
                    {log.targetType && log.targetId ? " · " : ""}
                    {log.targetId ? log.targetId.slice(0, 24) : ""}
                  </td>
                  <td>
                    <code style={{ wordBreak: "break-all", fontSize: 12 }}>
                      {log.detail === null || log.detail === undefined
                        ? "—"
                        : JSON.stringify(log.detail)}
                    </code>
                  </td>
                </tr>
              ))}
              {logs.length === 0 && !loading ? (
                <tr>
                  <td colSpan={5} className="readout">
                    暂无审计记录（上架运行后的管理动作会在此留痕）。
                  </td>
                </tr>
              ) : null}
              {loading ? (
                <tr>
                  <td colSpan={5} className="readout">
                    加载中…
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
        {nextCursor ? (
          <div className="pane-b">
            <button
              className="btn btn-ghost"
              type="button"
              disabled={moreLoading}
              onClick={() => void loadMore()}
            >
              {moreLoading ? "加载中…" : "加载更多"}
            </button>
          </div>
        ) : null}
      </section>
    </div>
  );
}
