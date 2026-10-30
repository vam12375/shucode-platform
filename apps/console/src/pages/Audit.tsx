import { useCallback, useEffect, useRef, useState } from "react";
import { ChevronLeft, ChevronRight, RefreshCw, ScrollText } from "lucide-react";
import { useAuth } from "../auth.js";
import { Glyph, PageTitle } from "../components/Icon.js";
import { Select } from "../components/Select.js";
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
  "member.update": "更新成员权限",
  "member.remove": "移除成员",
  "member.add": "添加成员",
  "policy.update": "更新消费策略",
  "invitation.create": "创建邀请",
  "invitation.revoke": "撤回邀请",
  "invitation.accept": "接受邀请",
  "quota_request.create": "申请团队额度",
  "quota_request.approved": "批准额度申请",
  "quota_request.rejected": "拒绝额度申请",
  "team.member.update": "更新成员权限",
  "team.member.remove": "移除成员",
  "team.policy.update": "更新消费策略",
  "team.invitation.create": "创建邀请",
  "team.invitation.revoke": "撤回邀请",
  "team.invitation.accept": "接受邀请",
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
  const { teamId, user, isRoot, canManageTeam } = useAuth();
  const [rows, setLogs] = useState<AuditLogRow[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [pageSize, setPageSize] = useState(50);
  const [pageIndex, setPageIndex] = useState(0);
  const [pageCursors, setPageCursors] = useState<(string | null)[]>([null]);
  const [responseScope, setResponseScope] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const requestVersion = useRef(0);
  const pageScope = JSON.stringify([teamId, user.id, isRoot, canManageTeam, pageSize]);
  const currentPage = responseScope === pageScope;
  const logs = currentPage ? rows : [];

  const load = useCallback(async (
    cursor: string | null = null,
    targetPage = 0,
    cursors: (string | null)[] = [null],
  ) => {
    const version = ++requestVersion.current;
    const params = new URLSearchParams({ limit: String(pageSize) });
    if (cursor) params.set("cursor", cursor);
    setLoading(true);
    try {
      const data = await api<AuditPageData>(`/audit?${params.toString()}`);
      if (version !== requestVersion.current) return;
      setLogs(data.logs);
      setNextCursor(data.nextCursor);
      setPageIndex(targetPage);
      setPageCursors(cursors);
      setResponseScope(pageScope);
      setError(null);
    } catch (loadError) {
      if (version === requestVersion.current) setError(loadError instanceof Error ? loadError.message : "加载失败");
    } finally {
      if (version === requestVersion.current) setLoading(false);
    }
  }, [pageScope, pageSize]);

  useEffect(() => {
    setPageIndex(0);
    setPageCursors([null]);
    setNextCursor(null);
    setError(null);
    void load();
    // 团队或权限变化后，迟到的旧页不得覆盖当前范围，也不能复用其分页游标。
    return () => { requestVersion.current += 1; };
  }, [load]);

  const previousPage = (): void => {
    if (loading || !currentPage || pageIndex === 0) return;
    void load(pageCursors[pageIndex - 1], pageIndex - 1, pageCursors);
  };
  const nextPage = (): void => {
    if (loading || !currentPage || !nextCursor) return;
    void load(nextCursor, pageIndex + 1, [...pageCursors.slice(0, pageIndex + 1), nextCursor]);
  };

  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">治理 / 审计</p>
          <PageTitle icon={ScrollText}>审计日志</PageTitle>
        </div>
        <span className="readout m0">管理动作留痕 · 不含凭据与请求正文</span>
      </header>

      {error ? (
        <Notice tone="stop" k="加载失败">
          {error}{" "}<button className="btn btn-sm" type="button" disabled={loading} onClick={() => void load()}><Glyph icon={RefreshCw} size={13} strokeWidth={2} />重试</button>
        </Notice>
      ) : null}

      <section className="pane">
        <div className="pane-h">
          <h2>最近动作</h2>
          <span className="readout">每页 {pageSize} 条 · 时间倒序</span>
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
        <nav className="pane-b form-row" aria-label="审计日志分页" style={{ justifyContent: "space-between" }}>
          <div className="form-row">
            <Select
              ariaLabel="审计日志每页条数"
              value={String(pageSize)}
              onChange={(value) => setPageSize(Number(value))}
              options={[20, 50, 100].map((size) => ({ value: String(size), label: `每页 ${size} 条` }))}
              style={{ width: 130 }}
            />
            <span className="readout" aria-live="polite">第 {currentPage ? pageIndex + 1 : 1} 页 · 本页 {logs.length} 条{loading ? " · 加载中…" : ""}</span>
          </div>
          <div className="form-row">
            <button className="btn btn-ghost" type="button" aria-label="审计日志上一页" disabled={loading || !currentPage || pageIndex === 0} onClick={previousPage}><Glyph icon={ChevronLeft} size={14} strokeWidth={2} />上一页</button>
            <button className="btn btn-ghost" type="button" aria-label="审计日志下一页" disabled={loading || !currentPage || !nextCursor} onClick={nextPage}>下一页<Glyph icon={ChevronRight} size={14} strokeWidth={2} /></button>
          </div>
        </nav>
      </section>
    </div>
  );
}
