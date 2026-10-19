import { Fragment, useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "../api.js";
import { confirmDialog, toastErr, toastOk } from "../components/Feedback.js";
import { Select } from "../components/Select.js";
import { Chip, Notice } from "../components/StatusChip.js";
import { formatShanghaiTime } from "../time.js";

interface ProviderRow {
  id: string;
  name: string;
  baseUrl: string;
  protocol: "openai-chat-completions" | "openai-responses" | "anthropic-messages";
  enabled: boolean;
  disabledMode: "manual" | "auto" | null;
  autoDisabledAt: string | null;
  autoDisabledReason: string | null;
  consecutiveFailures: number;
  lastTestAt: string | null;
  lastTestOk: boolean | null;
  lastTestKind: string | null;
  lastTestStatus: number | null;
  lastTestLatencyMs: number | null;
  lastTestModel: string | null;
  lastTestDetail: string | null;
  hasKey: boolean;
  apiKeyHint: string | null;
  anthropicVersion: string | null;
  timeoutMs: number | null;
  createdAt: string;
}

/** /console/api/providers/:id/test 的返回：kind 为服务端归类的失败原因。 */
interface ProviderTestResult {
  ok: boolean;
  kind: "ok" | "auth" | "not_found" | "rate_limited" | "timeout" | "network" | "upstream_error";
  status?: number;
  latencyMs: number;
  detail?: string;
}

function testKindLabel(kind: string | null): string {
  switch (kind) {
    case "auth":
      return "密钥无效或未授权";
    case "not_found":
      return "模型不存在或路径不正确";
    case "rate_limited":
      return "上游限流";
    case "timeout":
      return "连接或响应超时";
    case "network":
      return "无法连接上游";
    case "upstream_error":
      return "上游错误";
    case "ok":
      return "通过";
    default:
      return "测试未通过";
  }
}

function protocolLabel(protocol: ProviderRow["protocol"]): string {
  if (protocol === "anthropic-messages") return "anthropic · /v1/messages";
  if (protocol === "openai-responses") return "openai responses · /v1/responses（专用）";
  // OpenAI 兼容家族：chat 与 responses 两个端点都透传给该供应商。
  return "openai · /chat/completions · /responses";
}

export function ProvidersPage() {
  const [providers, setProviders] = useState<ProviderRow[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);  const [form, setForm] = useState({
    name: "",
    baseUrl: "",
    protocol: "openai-chat-completions" as ProviderRow["protocol"],
    apiKey: "",
    anthropicVersion: "",
    timeoutMs: "",
  });
  const [expandedModels, setExpandedModels] = useState<string | null>(null);
  const [fetchedModels, setFetchedModels] = useState<Record<string, string[]>>({});

  const load = useCallback(async () => {
    try {
      const data = await api<{ providers: ProviderRow[] }>("/providers");
      setProviders(data.providers);
      setError(null);
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载失败");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const resetForm = (): void => {
    setEditingId(null);
    setForm({ name: "", baseUrl: "", protocol: "openai-chat-completions", apiKey: "", anthropicVersion: "", timeoutMs: "" });
  };

  const submit = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    setBusy(true);
    try {
      const body = {
        name: form.name.trim(),
        baseUrl: form.baseUrl.trim(),
        protocol: form.protocol,
        // 空密钥 = 不修改已存密钥（编辑时）；新供应商留空则不配密钥。
        ...(form.apiKey ? { apiKey: form.apiKey } : {}),
        anthropicVersion: form.anthropicVersion.trim() || null,
        timeoutMs: form.timeoutMs ? Number.parseInt(form.timeoutMs, 10) : null,
      };
      if (editingId) {
        await api(`/providers/${editingId}`, { method: "PATCH", body: JSON.stringify(body) });
        toastOk(`已更新供应商「${form.name.trim()}」`, "已保存");
      } else {
        await api("/providers", { method: "POST", body: JSON.stringify(body) });
        toastOk(`已创建供应商「${form.name.trim()}」，把模型绑定到它即可生效`, "已创建");
      }
      resetForm();
      await load();
    } catch (actionError) {
      toastErr(actionError instanceof Error ? actionError.message : "操作失败");
    } finally {
      setBusy(false);
    }
  };

  const startEdit = (provider: ProviderRow): void => {
    setEditingId(provider.id);
    setForm({
      name: provider.name,
      baseUrl: provider.baseUrl,
      protocol: provider.protocol,
      apiKey: "",
      anthropicVersion: provider.anthropicVersion ?? "",
      timeoutMs: provider.timeoutMs === null ? "" : String(provider.timeoutMs),
    });
  };

  const toggleEnabled = async (provider: ProviderRow): Promise<void> => {
    setBusy(true);
    try {
      await api(`/providers/${provider.id}`, {
        method: "PATCH",
        body: JSON.stringify({ enabled: !provider.enabled }),
      });
      await load();
      toastOk(provider.enabled ? `已停用「${provider.name}」` : `已启用「${provider.name}」`);
    } catch (actionError) {
      toastErr(actionError instanceof Error ? actionError.message : "操作失败");
    } finally {
      setBusy(false);
    }
  };

  const recoverProvider = async (provider: ProviderRow): Promise<void> => {
    setBusy(true);
    try {
      await api(`/providers/${provider.id}/recover`, { method: "POST" });
      await load();
      toastOk(`已恢复「${provider.name}」的调度`, "已恢复");
    } catch (actionError) {
      toastErr(actionError instanceof Error ? actionError.message : "恢复失败");
    } finally {
      setBusy(false);
    }
  };

  const remove = async (provider: ProviderRow): Promise<void> => {
    const answer = await confirmDialog({
      k: "删除供应商",
      title: `删除「${provider.name}」？`,
      message: "绑定该供应商的模型将自动解绑并回落网关默认上游（env 单上游）。",
      confirmText: "删除",
      danger: true,
    });
    if (!answer.confirmed) return;
    setBusy(true);
    try {
      await api(`/providers/${provider.id}`, { method: "DELETE" });
      if (editingId === provider.id) resetForm();
      await load();
      toastOk(`已删除供应商「${provider.name}」`, "已删除");
    } catch (actionError) {
      toastErr(actionError instanceof Error ? actionError.message : "删除失败");
    } finally {
      setBusy(false);
    }
  };

  const fetchModels = async (provider: ProviderRow): Promise<void> => {
    setBusy(true);
    try {
      const data = await api<{ models: string[] }>(`/providers/${provider.id}/models`);
      setFetchedModels((current) => ({ ...current, [provider.id]: data.models }));
      setExpandedModels(provider.id);
      toastOk(`上游返回 ${data.models.length} 个模型，对照「上游模型」列填写映射`, "获取成功");
    } catch (actionError) {
      toastErr(actionError instanceof Error ? actionError.message : "获取模型列表失败");
    } finally {
      setBusy(false);
    }
  };

  // 运行台动作：留空只验证可达性与鉴权；填模型名则真实发一次最小推理请求，
  // 验证「模型真的可调用」——获取列表成功不等于推理可用。
  const testProvider = async (provider: ProviderRow): Promise<void> => {
    const answer = await confirmDialog({
      k: "供应商测试",
      title: `测试「${provider.name}」`,
      message:
        "留空只验证连通与鉴权；填写上游模型名会真实发送一次最小推理请求（可能产生极少量上游费用）。",
      confirmText: "开始测试",
      input: { label: "探测模型（可选）", placeholder: "上游模型名，留空只测连通" },
    });
    if (!answer.confirmed) return;
    const model = answer.value.trim();
    setBusy(true);
    try {
      const data = await api<ProviderTestResult>(`/providers/${provider.id}/test`, {
        method: "POST",
        body: JSON.stringify(model ? { model } : {}),
      });
      // 结果已由后端落库（最近测试列），重载列表即见；失败同时给出行内 Toast。
      await load();
      if (data.ok) {
        toastOk(`「${provider.name}」测试通过 · ${data.latencyMs} ms`, "测试成功");
      } else {
        const label = testKindLabel(data.kind);
        const suffix = data.status ? `（HTTP ${data.status}）` : "";
        toastErr(`「${provider.name}」${label}${suffix}`, "测试未通过");
      }
    } catch (actionError) {
      toastErr(actionError instanceof Error ? actionError.message : "测试失败");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">接入 / 上游</p>
          <h1>上游供应商</h1>
        </div>
        <span className="readout">{providers.filter((p) => p.enabled).length} 生效 · {providers.length} 家</span>
      </header>
      <p className="readout" style={{ margin: 0, marginBottom: 18 }}>
        模型转发目标：控制台按需接入多家模型商，模型在「模型目录」绑定供应商；未绑定的模型走网关默认上游。
        API Key 加密存储、只显示后四位；base_url 填协议根（自动去掉尾部 /v1，路径由平台拼接）。
      </p>

      {error ? (
        <Notice tone="stop" k="加载失败">
          {error}
        </Notice>
      ) : null}

      <section className="pane">
        <div className="pane-h">
          <h2>{editingId ? "编辑供应商" : "接入新供应商"}</h2>
          <span className="readout">默认 OpenAI 兼容（/v1/chat/completions）</span>
        </div>
        <div className="pane-b">
          <form className="form-row" onSubmit={(event) => void submit(event)}>
            <input
              className="input"
              style={{ width: 150 }}
              placeholder="名称"
              aria-label="供应商名称"
              value={form.name}
              onChange={(event) => setForm({ ...form, name: event.target.value })}
              required
            />
            <input
              className="input grow"
              placeholder="Base URL（如 https://api.deepseek.com）"
              aria-label="Base URL"
              value={form.baseUrl}
              onChange={(event) => setForm({ ...form, baseUrl: event.target.value })}
              required
            />
            <Select
              ariaLabel="API 格式"
              style={{ width: 260 }}
              value={form.protocol}
              onChange={(value) => setForm({ ...form, protocol: value as ProviderRow["protocol"] })}
              options={[
                { value: "openai-chat-completions", label: "OpenAI 兼容（/chat/completions · /responses）" },
                { value: "openai-responses", label: "OpenAI Responses（/v1/responses 专用）" },
                { value: "anthropic-messages", label: "Anthropic（/v1/messages）" },
              ]}
            />
            <input
              className="input masked-input"
              style={{ width: 170 }}
              type="password"
              autoComplete="new-password"
              spellCheck={false}
              placeholder={editingId ? "API Key（留空不修改）" : "API Key（可空）"}
              aria-label="API Key"
              value={form.apiKey}
              onChange={(event) => setForm({ ...form, apiKey: event.target.value })}
            />
            <input
              className="input num"
              style={{ width: 120 }}
              placeholder="超时 ms"
              aria-label="超时毫秒"
              value={form.timeoutMs}
              onChange={(event) => setForm({ ...form, timeoutMs: event.target.value })}
            />
            <button className="btn" type="submit" disabled={busy || !form.name.trim() || !form.baseUrl.trim()}>
              {editingId ? "保存修改" : "创建供应商"}
            </button>
            {editingId ? (
              <button className="btn btn-ghost" type="button" disabled={busy} onClick={resetForm}>
                取消编辑
              </button>
            ) : null}
          </form>
        </div>
      </section>

      <section className="pane d1">
        <div className="pane-h">
          <h2>供应商清单</h2>
          <span className="readout">密钥 AES-256-GCM 加密落库</span>
        </div>
        <div className="pane-b flush table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">名称</th>
                <th scope="col">Base URL</th>
                <th scope="col">API 格式</th>
                <th scope="col">Key</th>
                <th scope="col">状态</th>
                <th scope="col">最近测试</th>
                <th scope="col" aria-label="操作" />
              </tr>
            </thead>
            <tbody>
              {providers.map((provider) => (
                <Fragment key={provider.id}>
                  <tr>
                    <td className="name">{provider.name}</td>
                    <td className="num">{provider.baseUrl}</td>
                    <td>{protocolLabel(provider.protocol)}</td>
                    <td>{provider.hasKey ? <code>…{provider.apiKeyHint}</code> : <span className="readout">未配置</span>}</td>
                    <td>
                      {provider.enabled ? (
                        <Chip tone="ok">生效</Chip>
                      ) : provider.disabledMode === "auto" ? (
                        <span className="form-row" style={{ alignItems: "baseline" }}>
                          <Chip tone="stop">自动停用</Chip>
                          <span className="readout">
                            {provider.autoDisabledReason ?? "连续失败"} · {provider.consecutiveFailures} 连败
                          </span>
                        </span>
                      ) : (
                        <Chip tone="mute">手动停用</Chip>
                      )}
                    </td>
                    <td>
                      {provider.lastTestAt === null ? (
                        <span className="readout">未测</span>
                      ) : (
                        <span
                          className="form-row"
                          style={{ alignItems: "baseline" }}
                          title={provider.lastTestDetail ?? undefined}
                        >
                          <Chip tone={provider.lastTestOk ? "ok" : "stop"}>
                            {provider.lastTestOk ? "通过" : testKindLabel(provider.lastTestKind)}
                          </Chip>
                          <span className="readout">
                            {formatShanghaiTime(provider.lastTestAt).slice(5, 16)} ·{" "}
                            {provider.lastTestLatencyMs ?? "?"} ms
                            {provider.lastTestModel ? ` · ${provider.lastTestModel}` : " · 仅连通"}
                          </span>
                        </span>
                      )}
                    </td>
                    <td>
                      <div className="form-row" style={{ justifyContent: "flex-end" }}>
                        <button
                          className="btn btn-ghost btn-sm"
                          disabled={busy}
                          onClick={() => void testProvider(provider)}
                        >
                          测试
                        </button>
                        <button
                          className="btn btn-ghost btn-sm"
                          disabled={busy}
                          onClick={() => void fetchModels(provider)}
                        >
                          获取模型列表
                        </button>
                        {provider.disabledMode === "auto" && !provider.enabled ? (
                          <button
                            className="btn btn-ghost btn-sm"
                            disabled={busy}
                            onClick={() => void recoverProvider(provider)}
                          >
                            恢复调度
                          </button>
                        ) : null}
                        <button
                          className="btn btn-ghost btn-sm"
                          disabled={busy}
                          onClick={() => void toggleEnabled(provider)}
                        >
                          {provider.enabled ? "停用" : "启用"}
                        </button>
                        <button className="btn btn-ghost btn-sm" disabled={busy} onClick={() => startEdit(provider)}>
                          编辑
                        </button>
                        <button
                          className="btn btn-danger btn-sm"
                          disabled={busy}
                          onClick={() => void remove(provider)}
                        >
                          删除
                        </button>
                      </div>
                    </td>
                  </tr>
                  {expandedModels === provider.id ? (
                    <tr>
                      <td colSpan={7}>
                        <div className="form-row">
                          {(fetchedModels[provider.id] ?? []).map((modelId) => (
                            <Chip key={modelId} tone="mute">
                              {modelId}
                            </Chip>
                          ))}
                          {(fetchedModels[provider.id] ?? []).length === 0 ? (
                            <span className="readout">上游未返回模型。</span>
                          ) : null}
                        </div>
                      </td>
                    </tr>
                  ) : null}
                </Fragment>
              ))}
              {providers.length === 0 ? (
                <tr>
                  <td colSpan={7} className="readout">
                    还没有供应商，接入一家试试（如 DeepSeek：https://api.deepseek.com）。
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
