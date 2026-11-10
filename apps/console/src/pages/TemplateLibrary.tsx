import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import {
  LayoutTemplate,
  ChevronLeft,
  ChevronRight,
  Download,
  FileUp,
  Plus,
  RefreshCw,
  Save,
  Search,
  Trash2,
} from "lucide-react";
import { api, apiUrl } from "../api.js";
import { useAuth } from "../auth.js";
import { confirmDialog, toastOk } from "../components/Feedback.js";
import { Glyph, PageTitle } from "../components/Icon.js";
import { Select } from "../components/Select.js";
import { Chip, Notice } from "../components/StatusChip.js";
import {
  emptyTemplate,
  entryFields,
  filterTemplates,
  kindLabels,
  isVideoRecipe,
  sceneLabels,
  type PackageInfo,
  type LibraryEntry,
  type TemplateFields,
  type TemplateKind,
} from "../template-library.js";
import "./template-library.css";

const PAGE_SIZE = 12;
const errorMessage = (error: unknown) =>
  error instanceof Error ? error.message : "操作失败，请重试";
const types = Object.entries(kindLabels) as [TemplateKind, string][];

export function TemplateLibraryPage() {
  const { isRoot, user } = useAuth();
  return isRoot ? (
    <LibraryContent key={user.id} />
  ) : (
    <Notice tone="stop">仅超级管理员可管理模板库。</Notice>
  );
}
function LibraryContent() {
  const [entries, setEntries] = useState<LibraryEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [filter, setFilter] = useState("all");
  const [status, setStatus] = useState("all");
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(0);
  const [selectedId, setSelectedId] = useState("");
  const [creating, setCreating] = useState(false);
  const [createKind, setCreateKind] = useState<TemplateKind>("prompt");
  const [draft, setDraft] = useState<TemplateFields | null>(null);
  const [newEnabled, setNewEnabled] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [importedPackage, setImportedPackage] = useState<PackageInfo | null>(
    null,
  );
  const active = useRef(false);
  const request = useRef(0);
  const lock = useRef(false);
  const editor = useRef<HTMLElement>(null);
  const load = useCallback(async () => {
    const current = ++request.current;
    setLoading(true);
    try {
      const data = await api<{ entries: LibraryEntry[] }>("/template-library");
      if (!active.current || current !== request.current) return;
      setEntries(data.entries);
      setError("");
      setConflict(false);
    } catch (reason) {
      if (active.current && current === request.current)
        setError(errorMessage(reason));
    } finally {
      if (active.current && current === request.current) setLoading(false);
    }
  }, []);
  useEffect(() => {
    active.current = true;
    void load();
    return () => {
      active.current = false;
      request.current++;
    };
  }, [load]);
  const selected = entries.find((entry) => entry.id === selectedId);
  const packageInfo = importedPackage ?? selected?.package;
  const kind = creating ? createKind : selected?.kind;
  const fields = draft ?? (selected ? entryFields(selected) : null);
  const dirty = Boolean(
    draft &&
      (creating ||
        (selected &&
          JSON.stringify(draft) !== JSON.stringify(entryFields(selected)))),
  );
  const filtered = filterTemplates(entries, filter, status, query);
  const lastPage = Math.max(0, Math.ceil(filtered.length / PAGE_SIZE) - 1);
  const currentPage = Math.min(page, lastPage);
  const visible = filtered.slice(
    currentPage * PAGE_SIZE,
    (currentPage + 1) * PAGE_SIZE,
  );
  const patch = (value: Partial<TemplateFields>) =>
    setDraft((previous) => ({
      ...(previous ?? fields ?? emptyTemplate()),
      ...value,
    }));
  const changeSelection = async (entry?: LibraryEntry) => {
    if (lock.current || loading) return;
    lock.current = true;
    try {
      if (dirty) {
        const result = await confirmDialog({
          title: "放弃未保存的修改？",
          message: "当前输入尚未保存，切换后需要重新填写。",
          confirmText: "放弃修改",
          danger: true,
        });
        if (!result.confirmed || !active.current) return;
      }
      setSelectedId(entry?.id ?? "");
      setCreating(!entry);
      setCreateKind("prompt");
      setImportedPackage(null);
      setDraft(entry ? null : emptyTemplate());
      setNewEnabled(false);
      setConflict(false);
      setError("");
      requestAnimationFrame(() =>
        editor.current?.scrollIntoView({ block: "nearest" }),
      );
    } finally {
      lock.current = false;
    }
  };
  const mutate = async (
    entry: LibraryEntry | undefined,
    method: "POST" | "PUT" | "DELETE",
    body: object,
    message: string,
  ) => {
    if (lock.current || loading) return;
    lock.current = true;
    setBusy(true);
    setError("");
    try {
      const result = await api<LibraryEntry>(
        `/template-library${entry ? `/${encodeURIComponent(entry.id)}` : ""}`,
        { method, body: JSON.stringify(body) },
      );
      if (!active.current) return;
      setEntries((previous) =>
        method === "DELETE"
          ? previous.filter((item) => item.id !== entry?.id)
          : method === "POST"
            ? [...previous, result]
            : previous.map((item) => (item.id === result.id ? result : item)),
      );
      if (method === "DELETE") {
        setSelectedId("");
        setDraft(null);
        setImportedPackage(null);
      } else if (method === "POST" || "fields" in body) {
        setCreating(false);
        setImportedPackage(null);
        setSelectedId(result.id);
        setDraft(null);
      }
      setConflict(false);
      toastOk(message);
    } catch (reason) {
      if (active.current) {
        const stale = (reason as { code?: number })?.code === 409;
        setConflict(stale);
        setError(
          stale
            ? "模板已被其他管理员修改。刷新目录会保留你的输入，请核对后重新保存。"
            : errorMessage(reason),
        );
      }
    } finally {
      lock.current = false;
      if (active.current) setBusy(false);
    }
  };
  const uploadPreview = async (file: File, slot: "cover" | "video") => {
    if (!selected || busy || file.size > 64 * 1024 * 1024) {
      if (file.size > 64 * 1024 * 1024) setError("素材不得超过 64 MiB");
      return;
    }
    setBusy(true);
    try {
      const uploaded = await api<{ name: string }>("/template-library/previews", {
        method: "POST", body: file, headers: { "content-type": file.type || "application/octet-stream" },
      });
      await mutate(selected, "PUT", { expectedVersion: selected.expectedVersion,
        preview: { cover: selected.preview?.cover ?? "", video: selected.preview?.video ?? "", [slot]: uploaded.name },
      }, "预览素材已更新");
    } catch (reason) { setError(errorMessage(reason)); }
    finally { if (active.current) setBusy(false); }
  };
  const save = async (event: FormEvent) => {
    event.preventDefault();
    if (!fields || !kind || conflict) return;
    const cleanFields = {
      ...fields,
      tags: fields.tags.filter((tag) => tag.trim()),
    };
    const body = creating
      ? { kind, fields: cleanFields, enabled: newEnabled }
      : { expectedVersion: selected?.expectedVersion, fields: cleanFields };
    await mutate(
      selected,
      creating ? "POST" : "PUT",
      body,
      creating ? "模板已收录" : "模板已保存",
    );
  };
  const remove = async () => {
    if (!selected || lock.current || loading || conflict) return;
    lock.current = true;
    const result = await confirmDialog({
      title: "删除模板",
      message: `删除「${selected.name}」后，将从模板库及客户端可用目录移除，刷新后不会恢复。已有任务和历史记录保留。${dirty ? "当前未保存的修改也会丢弃。" : ""}`,
      confirmText: "删除模板",
      danger: true,
    });
    lock.current = false;
    if (result.confirmed && active.current)
      await mutate(
        selected,
        "DELETE",
        { expectedVersion: selected.expectedVersion },
        "模板已删除",
      );
  };
  const importSkill = async (file: File | undefined) => {
    if (!file || !fields || lock.current) return;
    if (
      !file.name.toLowerCase().endsWith(".zip") ||
      file.size > 4 * 1024 * 1024
    ) {
      setError("请选择不超过 4 MiB 的完整 Skill ZIP 包。");
      return;
    }
    lock.current = true;
    setBusy(true);
    try {
      const response = await fetch(apiUrl("/template-library/packages"), {
        method: "POST",
        credentials: "include",
        headers: { "content-type": "application/zip" },
        body: file,
      });
      const result = (await response.json()) as {
        code: number;
        msg: string;
        data: { package: PackageInfo; content: string };
      };
      if (!response.ok || result.code !== 0)
        throw new Error(result.msg || "整包导入失败");
      if (!active.current) return;
      const metadata =
        result.data.content.match(/^---\r?\n([\s\S]*?)\r?\n---/u)?.[1] ?? "";
      const name =
        metadata.match(/^name:\s*(.+)$/mu)?.[1]?.replace(/^['"]|['"]$/gu, "") ??
        file.name.replace(/\.zip$/iu, "");
      const description =
        metadata
          .match(/^description:\s*(.+)$/mu)?.[1]
          ?.replace(/^['"]|['"]$/gu, "") ?? "";
      setImportedPackage(result.data.package);
      patch({
        content: result.data.content,
        packageDigest: result.data.package.sha256,
        name: fields.name || name,
        description: fields.description || description,
      });
      setError("");
    } catch (reason) {
      if (active.current) setError(errorMessage(reason));
    } finally {
      lock.current = false;
      if (active.current) setBusy(false);
    }
  };
  const download = async () => {
    if (!fields) return;
    let blob = new Blob([fields.content], { type: "text/plain;charset=utf-8" });
    if (kind === "skill" && selected?.package) {
      try {
        const response = await fetch(
          apiUrl(
            `/template-library/${encodeURIComponent(selected.id)}/package?sha256=${selected.package.sha256}`,
          ),
          { credentials: "include" },
        );
        if (!response.ok)
          throw new Error("技能包已更新或下载失败，请刷新目录后重试。");
        blob = await response.blob();
      } catch (reason) {
        if (active.current) setError(errorMessage(reason));
        return;
      }
    }
    let isJSON = isVideoRecipe(selected);
    if (kind === "template" || kind === "video" && !isJSON) {
      try {
        JSON.parse(fields.content);
        isJSON = true;
      } catch {
        /* 通用文本模板沿用文本下载。 */
      }
    }
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download =
      kind === "skill"
        ? "skill-package.zip"
        : isJSON
          ? "template.json"
          : "template.txt";
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };

  return (
    <div className="template-library">
      <header className="page-head">
        <div>
          <p className="eyebrow">参考 / 内容资产</p>
          <PageTitle icon={LayoutTemplate}>模板库</PageTitle>
        </div>
        <div className="form-row">
          <button
            className="btn btn-ghost btn-sm"
            disabled={loading || busy}
            onClick={() => void load()}
          >
            <Glyph icon={RefreshCw} size={14} />
            刷新目录
          </button>
          <button
            className="btn btn-sm"
            disabled={loading || busy}
            onClick={() => void changeSelection()}
          >
            <Glyph icon={Plus} size={14} />
            收录模板
          </button>
        </div>
      </header>
      <p className="readout">
        管理视频配方、自动化、提示词与
        Skill。视频模板保存后，在线客户端自动更新；离线客户端恢复连接后更新。
      </p>
      <div className="library-register">
        <span>
          <strong>{entries.length}</strong> 条收录
        </span>
        <span>
          <i className="library-signal" />
          {entries.filter((entry) => entry.enabled).length} 条启用
        </span>
        <span>{entries.filter((entry) => !entry.enabled).length} 条停用</span>
        <span className="library-register-note">
          模板启停与视频效果审核独立
        </span>
      </div>
      {error ? (
        <Notice tone="stop" k={conflict ? "版本冲突" : "操作未完成"}>
          {error}
          {conflict ? (
            <button
              className="btn btn-ghost btn-sm"
              disabled={loading || busy}
              onClick={() => void load()}
            >
              刷新并保留输入
            </button>
          ) : null}
        </Notice>
      ) : null}
      <div className="library-workspace">
        <section className="pane library-catalog" aria-label="模板目录">
          <div className="library-filters" aria-label="模板类型">
            {[["all", "全部"], ...types].map(([value, label]) => (
              <button
                key={value}
                className={
                  filter === value ? "library-filter active" : "library-filter"
                }
                aria-pressed={filter === value}
                onClick={() => {
                  setFilter(value!);
                  setPage(0);
                }}
              >
                {label}
                <span>
                  {value === "all"
                    ? entries.length
                    : entries.filter((entry) => entry.kind === value).length}
                </span>
              </button>
            ))}
          </div>
          <div className="library-search">
            <label className="library-search-input">
              <Glyph icon={Search} />
              <input
                className="input"
                aria-label="搜索模板"
                placeholder="搜索名称、标签或 ID"
                value={query}
                onChange={(event) => {
                  setQuery(event.target.value);
                  setPage(0);
                }}
              />
            </label>
            <Select
              ariaLabel="启停状态"
              value={status}
              onChange={(value) => {
                setStatus(value);
                setPage(0);
              }}
              options={[
                { value: "all", label: "全部状态" },
                { value: "enabled", label: "已启用" },
                { value: "disabled", label: "已停用" },
              ]}
            />
          </div>
          {loading ? (
            <p className="pane-b" role="status">
              正在读取模板目录…
            </p>
          ) : null}
          {!loading && !visible.length ? (
            <div className="library-empty">
              <Glyph icon={LayoutTemplate} size={28} />
              <p>{entries.length ? "没有符合筛选的模板" : "暂无模板"}</p>
              <span className="readout">
                调整筛选，或点击「收录模板」添加内容。
              </span>
            </div>
          ) : null}
          <div className="library-rows">
            {visible.map((entry) => (
              <article
                key={entry.id}
                className={`library-row${selectedId === entry.id ? " selected" : ""}`}
              >
                <button
                  className="library-row-main"
                  disabled={busy || loading}
                  aria-pressed={selectedId === entry.id}
                  onClick={() => void changeSelection(entry)}
                >
                  <span className="library-row-meta">
                    {kindLabels[entry.kind]}
                    <span>
                      {sceneLabels[entry.scene] ??
                        (entry.source === "builtin-video"
                          ? "内置配方"
                          : entry.source === "curated"
                            ? "门道创作配方"
                          : entry.source === "dineops"
                            ? "旧门道"
                            : "自建")}
                    </span>
                  </span>
                  <strong>{entry.name}</strong>
                  <span className="library-excerpt">
                    {entry.description || entry.content}
                  </span>
                </button>
                <div className="library-row-state">
                  <Chip tone={entry.enabled ? "ok" : "mute"}>
                    {entry.enabled ? "已启用" : "已停用"}
                  </Chip>
                  <label className="switch">
                    <input
                      type="checkbox"
                      aria-label={`${entry.enabled ? "停用" : "启用"} ${entry.name}`}
                      checked={entry.enabled}
                      disabled={busy || loading || conflict}
                      onChange={() =>
                        void mutate(
                          entry,
                          "PUT",
                          {
                            expectedVersion: entry.expectedVersion,
                            enabled: !entry.enabled,
                          },
                          entry.enabled ? "模板已停用" : "模板已启用",
                        )
                      }
                    />
                    <i />
                  </label>
                </div>
              </article>
            ))}
          </div>
          <div className="library-pagination">
            <span className="readout">
              共 {filtered.length} 条 · {currentPage + 1} / {lastPage + 1} 页
            </span>
            <div className="form-row">
              <button
                className="btn btn-ghost btn-sm"
                aria-label="上一页"
                disabled={!currentPage}
                onClick={() => setPage(currentPage - 1)}
              >
                <Glyph icon={ChevronLeft} />
              </button>
              <button
                className="btn btn-ghost btn-sm"
                aria-label="下一页"
                disabled={currentPage >= lastPage}
                onClick={() => setPage(currentPage + 1)}
              >
                <Glyph icon={ChevronRight} />
              </button>
            </div>
          </div>
        </section>
        <section
          className="pane library-editor"
          ref={editor}
          aria-label="模板详情"
        >
          {!fields ? (
            <div className="library-empty">
              <Glyph icon={LayoutTemplate} size={32} />
              <h2>选一条模板，查看完整内容</h2>
              <p className="readout">
                保留来源、双语正文与使用场景。
                <br />
                新内容可先停用保存，准备好后再启用。
              </p>
            </div>
          ) : (
            <>
              <div className="pane-h">
                <h2>{creating ? "收录模板" : kindLabels[kind!]}</h2>
                {!creating ? (
                  <button
                    className="btn btn-ghost btn-sm"
                    onClick={() => void download()}
                  >
                    <Glyph icon={Download} size={14} />
                    下载
                  </button>
                ) : null}
              </div>
              <form
                className="pane-b form-stack"
                onSubmit={(event) => void save(event)}
              >
                {creating ? (
                  <label className="field">
                    <span className="field-label">内容类型</span>
                    <Select
                      ariaLabel="内容类型"
                      value={createKind}
                      disabled={busy}
                      options={types
                        .filter(([value]) => value !== "video")
                        .map(([value, label]) => ({ value, label }))}
                      onChange={(value) => {
                        const next = value as TemplateKind;
                        setCreateKind(next);
                        patch({
                          scene:
                            next === "skill" || next === "template"
                              ? ""
                              : next === "automation"
                                ? "scheduled-task"
                                : "draft-suggestion",
                          cronExpr: next === "automation" ? "0 9 * * 1-5" : "",
                          packageDigest: undefined,
                        });
                      }}
                    />
                  </label>
                ) : (
                  <p className="library-source readout">
                    {selected?.source === "dineops"
                      ? "旧门道 · DineOps"
                      : selected?.source === "custom"
                        ? "自建内容"
                        : selected?.source === "builtin-video"
                          ? "平台内置视频配方"
                          : "客户端场景目录"}
                    <br />
                    <span>{selected?.id}</span>
                  </p>
                )}
                {isVideoRecipe(selected) ? (
                  <>
                    <h3>{fields.name}</h3>
                    <p>{fields.description}</p>
                    <Notice>
                      视频配方按版本维护。启用后可供选择，自动采用仍需通过对应模型的成片审核。
                    </Notice>
                    <a href="#/media-models">前往媒体模型查看模板审核</a>
                    <section className="library-preview-editor">
                      <h3>封面与示例视频</h3>
                      <p className="muted">公开展示素材，单个文件最多 64 MiB。发布后使用内容指纹地址缓存一年；更新素材自动换址。</p>
                      {selected?.preview?.cover && <img style={{ maxWidth: "100%", maxHeight: 240 }} src={"/api/v1/media/template-assets/" + selected.preview.cover} alt="模板封面" />}
                      {selected?.preview?.video && <video style={{ maxWidth: "100%", maxHeight: 240 }} src={"/api/v1/media/template-assets/" + selected.preview.video} controls preload="none" />}
                      <label className="field"><span className="field-label">上传封面</span><input aria-label="上传模板封面" type="file" accept="image/png,image/jpeg,image/webp" disabled={busy} onChange={(event) => { const file = event.target.files?.[0]; if (file) void uploadPreview(file,"cover"); event.target.value = ""; }} /></label>
                      <label className="field"><span className="field-label">上传示例视频</span><input aria-label="上传模板示例视频" type="file" accept="video/mp4,video/webm" disabled={busy} onChange={(event) => { const file = event.target.files?.[0]; if (file) void uploadPreview(file,"video"); event.target.value = ""; }} /></label>
                      {selected?.preview && <button type="button" className="btn" disabled={busy} onClick={() => void mutate(selected,"PUT",{ expectedVersion:selected.expectedVersion, preview:{cover:"",video:""}},"预览关联已清除")}>清除预览关联</button>}
                    </section>
                    <pre className="library-content">{fields.content}</pre>
                  </>
                ) : (
                  <>
                    <label className="field">
                      <span className="field-label">名称</span>
                      <input
                        className="input"
                        aria-label="模板名称"
                        required
                        maxLength={300}
                        value={fields.name}
                        disabled={busy}
                        onChange={(event) =>
                          patch({ name: event.target.value })
                        }
                      />
                    </label>
                    {kind === "automation" ? (
                      <label className="field">
                        <span className="field-label">运行场景</span>
                        <Select
                          ariaLabel="运行场景"
                          value={fields.scene}
                          disabled={busy || !creating}
                          options={[
                            { value: "scheduled-task", label: "定时任务" },
                            { value: "off-peak-task", label: "闲时任务" },
                          ]}
                          onChange={(scene) =>
                            patch({
                              scene,
                              cronExpr:
                                scene === "scheduled-task" ? "0 9 * * 1-5" : "",
                            })
                          }
                        />
                      </label>
                    ) : null}
                    {fields.scene === "scheduled-task" ? (
                      <label className="field">
                        <span className="field-label">定时规则（Cron）</span>
                        <input
                          className="input num"
                          aria-label="定时规则"
                          value={fields.cronExpr}
                          required
                          disabled={busy}
                          placeholder="0 9 * * 1-5"
                          onChange={(event) =>
                            patch({ cronExpr: event.target.value })
                          }
                        />
                        <small className="readout">
                          支持每日或每周规则，例如工作日 09:00：0 9 * * 1-5。
                        </small>
                      </label>
                    ) : null}
                    <label className="field">
                      <span className="field-label">简介</span>
                      <textarea
                        aria-label="模板简介"
                        rows={2}
                        maxLength={2000}
                        value={fields.description}
                        disabled={busy}
                        onChange={(event) =>
                          patch({ description: event.target.value })
                        }
                      />
                    </label>
                    {kind === "skill" ? (
                      <>
                        <label className="btn btn-ghost btn-sm library-file">
                          <Glyph icon={FileUp} size={14} />
                          导入完整 Skill 包
                          <input
                            type="file"
                            accept=".zip,application/zip"
                            aria-label="导入 Skill 文件"
                            disabled={busy}
                            onChange={(event) => {
                              void importSkill(event.target.files?.[0]);
                              event.target.value = "";
                            }}
                          />
                        </label>
                        <p className="readout">
                          包含
                          SKILL.md、脚本、参考资料与素材；下载保留完整目录。
                        </p>
                        {packageInfo ? (
                          <details className="library-package">
                            <summary>
                              完整包 · {packageInfo.files.length} 个文件
                            </summary>
                            <ul>
                              {packageInfo.files.map((file) => (
                                <li key={file.path}>
                                  <code>{file.path}</code>
                                  <span>{file.size.toLocaleString()} B</span>
                                </li>
                              ))}
                            </ul>
                            <p className="readout">
                              SHA-256：{packageInfo.sha256}
                            </p>
                          </details>
                        ) : null}
                      </>
                    ) : null}
                    <label className="field">
                      <span className="field-label">
                        {kind === "skill"
                          ? "Skill 正文"
                          : kind === "template"
                            ? "模板正文"
                            : "提示词正文"}
                      </span>
                      <textarea
                        className="library-textarea"
                        aria-label="模板正文"
                        rows={12}
                        value={fields.content}
                        maxLength={60000}
                        required
                        disabled={busy}
                        onChange={(event) =>
                          patch({ content: event.target.value })
                        }
                      />
                    </label>
                    <label className="field">
                      <span className="field-label">标签（用逗号分隔）</span>
                      <input
                        className="input"
                        aria-label="模板标签"
                        value={fields.tags.join(", ")}
                        disabled={busy}
                        onChange={(event) =>
                          patch({
                            tags: event.target.value
                              .split(/[,，]/u)
                              .map((tag) => tag.trim()),
                          })
                        }
                      />
                    </label>
                    <details>
                      <summary>英文内容</summary>
                      <div className="form-stack library-english">
                        <label className="field">
                          <span className="field-label">英文名称</span>
                          <input
                            className="input"
                            aria-label="英文名称"
                            value={fields.nameEn}
                            maxLength={500}
                            disabled={busy}
                            onChange={(event) =>
                              patch({ nameEn: event.target.value })
                            }
                          />
                        </label>
                        <label className="field">
                          <span className="field-label">英文简介</span>
                          <textarea
                            value={fields.descriptionEn}
                            maxLength={4000}
                            aria-label="英文简介"
                            disabled={busy}
                            onChange={(event) =>
                              patch({ descriptionEn: event.target.value })
                            }
                          />
                        </label>
                        <label className="field">
                          <span className="field-label">英文正文</span>
                          <textarea
                            className="library-textarea"
                            rows={8}
                            value={fields.contentEn}
                            maxLength={60000}
                            aria-label="英文正文"
                            disabled={busy}
                            onChange={(event) =>
                              patch({ contentEn: event.target.value })
                            }
                          />
                        </label>
                      </div>
                    </details>
                    {creating ? (
                      <label className="form-row">
                        <input
                          type="checkbox"
                          checked={newEnabled}
                          disabled={busy}
                          onChange={(event) =>
                            setNewEnabled(event.target.checked)
                          }
                        />
                        保存后启用
                      </label>
                    ) : null}
                  </>
                )}
                <div className="library-save">
                  {!isVideoRecipe(selected) ? (
                    <button
                      className="btn btn-sm"
                      type="submit"
                      disabled={
                        busy || loading || conflict || (!creating && !dirty)
                      }
                    >
                      <Glyph icon={Save} size={14} />
                      {busy ? "保存中…" : creating ? "收录模板" : "保存修改"}
                    </button>
                  ) : null}
                  {selected && !creating ? (
                    <button
                      className="btn btn-danger btn-ghost btn-sm"
                      type="button"
                      disabled={busy || loading || conflict}
                      onClick={() => void remove()}
                    >
                      <Glyph icon={Trash2} size={14} />
                      删除
                    </button>
                  ) : null}
                </div>
              </form>
            </>
          )}
        </section>
      </div>
    </div>
  );
}
