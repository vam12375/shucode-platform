import {
  computeUsageMicroCredits,
  estimateMaxMicroCredits,
  type ModelPriceRates,
  type UsageTokenCounts,
} from "@shucode-platform/contracts/credits";
import {
  createSecretBox,
  hashToken,
  randomId,
  type DeviceTokenRecord,
  type PlatformModelRecord,
  type PlatformStore,
  type SecretBox,
  type UsageRecord,
  type UserRecord,
} from "@shucode-platform/db";
import { Hono, type Context } from "hono";
import { normalizeIp, resolveClientIp } from "./client-ip.js";
import type { GatewayEnv } from "./env.js";
import {
  createAnthropicSseUsageScanner,
  createOpenAiSseUsageScanner,
  createResponsesSseUsageScanner,
  parseUsageFromAnthropicJson,
  parseUsageFromOpenAiJson,
  parseUsageFromResponsesJson,
} from "./usage.js";

export interface GatewayLogger {
  info: (message: string, detail?: unknown) => void;
  warn: (message: string, detail?: unknown) => void;
  error: (message: string, detail?: unknown) => void;
}

export interface GatewayDeps {
  store: PlatformStore;
  env: GatewayEnv;
  now: () => number;
  fetchUpstream: (input: string | URL, init: RequestInit) => Promise<Response>;
  logger: GatewayLogger;
}

const MAX_MAX_TOKENS = 256_000;
const DEFAULT_MAX_TOKENS = 4_096;
const MAX_UPSTREAM_ERROR_BYTES = 1_000_000;

function anthropicError(status: number, type: string, message: string): Response {
  return new Response(JSON.stringify({ type: "error", error: { type, message } }), {
    status,
    headers: { "content-type": "application/json; charset=utf-8" },
  });
}

function openAiError(status: number, type: string, message: string): Response {
  return new Response(JSON.stringify({ error: { message, type, code: null } }), {
    status,
    headers: { "content-type": "application/json; charset=utf-8" },
  });
}

function readGatewayToken(c: Context): string | null {
  const authorization = c.req.header("authorization")?.trim();
  if (authorization?.toLowerCase().startsWith("bearer ")) {
    const token = authorization.slice(7).trim();
    if (token) return token;
  }
  return c.req.header("x-api-key")?.trim() || null;
}

function priceRates(model: PlatformModelRecord): ModelPriceRates {
  return {
    inputPerMtok: Number.parseFloat(model.inputPricePerMtok) || 0,
    outputPerMtok: Number.parseFloat(model.outputPricePerMtok) || 0,
    cacheReadPerMtok: Number.parseFloat(model.cacheReadPricePerMtok) || 0,
    cacheWritePerMtok: Number.parseFloat(model.cacheWritePricePerMtok) || 0,
  };
}

function readMaxTokensFrom(value: unknown): number {
  const raw = Number(value);
  return Number.isFinite(raw) && raw > 0 ? Math.min(Math.floor(raw), MAX_MAX_TOKENS) : DEFAULT_MAX_TOKENS;
}

/** 浏览器环境 setTimeout 返回 number，Node 返回可 unref 的对象；能 unref 就 unref，不延长进程寿命。 */
function unrefTimer(timer: ReturnType<typeof setTimeout>): void {
  if (typeof timer === "object" && timer !== null && "unref" in timer) {
    (timer as { unref(): void }).unref();
  }
}

/**
 * 两种模型协议共享同一条管道：鉴权 → 模型校验 → 额度准入（预留）→
 * 透传转发上游 → 按 usage 结算。协议差异只体现在
 * 请求体字段读取、上游鉴权头、usage 解析与流式采集器上。
 */
interface ProtocolSpec {
  readonly routePath: string;
  readonly upstreamPath: string;
  /** 协议家族：openai 家族（chat 与 responses 两个端点）或 anthropic。 */
  readonly family: "openai" | "anthropic";
  readonly errorResponse: (status: number, type: string, message: string) => Response;
  readonly readModelId: (body: Record<string, unknown>) => string | null;
  readonly readMaxTokens: (body: Record<string, unknown>) => number;
  readonly isStream: (body: Record<string, unknown>) => boolean;
  readonly buildUpstreamRequest: (
    c: Context,
    body: Record<string, unknown>,
    upstreamModel: string,
    stream: boolean,
    upstream: { apiKey: string; anthropicVersion: string },
  ) => { headers: Record<string, string>; bodyText: string };
  readonly parseUsageFromJson: (text: string) => UsageTokenCounts | null;
  readonly createStreamScanner: () => {
    push(chunk: string): void;
    usage(): UsageTokenCounts;
    dropped(): number;
  };
}

function buildAnthropicSpec(): ProtocolSpec {
  return {
    routePath: "/v1/messages",
    upstreamPath: "/v1/messages",
    family: "anthropic",
    errorResponse: anthropicError,
    readModelId: (body) => (typeof body.model === "string" ? body.model.trim() : null),
    readMaxTokens: (body) => readMaxTokensFrom(body.max_tokens),
    isStream: (body) => body.stream === true,
    buildUpstreamRequest: (c, body, upstreamModel, _stream, upstream) => {
      const headers: Record<string, string> = {
        "content-type": "application/json",
        "anthropic-version": upstream.anthropicVersion,
      };
      const clientVersion = c.req.header("anthropic-version")?.trim();
      if (clientVersion) headers["anthropic-version"] = clientVersion;
      const beta = c.req.header("anthropic-beta")?.trim();
      if (beta) headers["anthropic-beta"] = beta;
      // 上游鉴权由平台注入，客户端的平台凭据不外发。
      if (upstream.apiKey) headers["x-api-key"] = upstream.apiKey;
      return { headers, bodyText: JSON.stringify({ ...body, model: upstreamModel }) };
    },
    parseUsageFromJson: (text) => {
      try {
        return parseUsageFromAnthropicJson(JSON.parse(text));
      } catch {
        return null;
      }
    },
    createStreamScanner: createAnthropicSseUsageScanner,
  };
}

function buildOpenAiSpec(): ProtocolSpec {
  return {
    routePath: "/v1/chat/completions",
    upstreamPath: "/v1/chat/completions",
    family: "openai",
    errorResponse: openAiError,
    readModelId: (body) => (typeof body.model === "string" ? body.model.trim() : null),
    readMaxTokens: (body) =>
      readMaxTokensFrom(body.max_completion_tokens ?? body.max_tokens),
    isStream: (body) => body.stream === true,
    buildUpstreamRequest: (_c, body, upstreamModel, stream, upstream) => {
      const headers: Record<string, string> = { "content-type": "application/json" };
      if (upstream.apiKey) headers["authorization"] = `Bearer ${upstream.apiKey}`;
      const payload: Record<string, unknown> = { ...body, model: upstreamModel };
      // 流式计量依赖最终 usage chunk：客户端未声明时注入 include_usage。
      if (stream && payload.stream_options === undefined) {
        payload.stream_options = { include_usage: true };
      }
      return { headers, bodyText: JSON.stringify(payload) };
    },
    parseUsageFromJson: (text) => {
      try {
        return parseUsageFromOpenAiJson(JSON.parse(text));
      } catch {
        return null;
      }
    },
    createStreamScanner: createOpenAiSseUsageScanner,
  };
}

/**
 * 平台凭据（设备 token）鉴权：token → 设备 → 用户。
 * 错误响应形状跟随路由协议（spec.errorResponse 由调用方传入）。
 */
async function authenticateGatewayRequest(
  deps: GatewayDeps,
  c: Context,
  errorResponse: (status: number, type: string, message: string) => Response,
): Promise<{ device: DeviceTokenRecord; user: UserRecord } | Response> {
  const token = readGatewayToken(c);
  if (!token) {
    return errorResponse(401, "authentication_error", "缺少平台凭据（x-api-key 或 Bearer）");
  }
  const device: DeviceTokenRecord | null = await deps.store.findDeviceTokenByHash(hashToken(token));
  if (!device || device.revokedAt) {
    return errorResponse(401, "authentication_error", "平台凭据无效或已吊销");
  }
  const user: UserRecord | null = await deps.store.findUserById(device.userId);
  if (!user || user.disabledAt) {
    return errorResponse(403, "permission_error", "账号不可用");
  }
  return { device, user };
}

/**
 * GET /v1/models：OpenAI list 兼容的已启用模型清单。
 * 客户端 Provider「获取模型列表」/ 模型选择界面调用；模型目录平台全局共享，
 * 不按团队隔离（是否可请求由额度准入与下发配置决定），因此仅校验设备凭据有效。
 */
async function handleModelsRequest(c: Context, deps: GatewayDeps): Promise<Response> {
  const auth = await authenticateGatewayRequest(deps, c, openAiError);
  if (auth instanceof Response) return auth;
  const models = await deps.store.listModels();
  const epochSeconds = (iso: string): number => {
    const parsed = Date.parse(iso);
    return Number.isFinite(parsed) ? Math.floor(parsed / 1000) : 0;
  };
  return c.json({
    object: "list",
    data: models.map((model) => ({
      id: model.modelId,
      object: "model",
      created: epochSeconds(model.createdAt),
      owned_by: "mendao-platform",
    })),
  });
}

/**
 * OpenAI Responses API（/v1/responses）：与 chat completions 同属 OpenAI 兼容家族
 * （provider protocol 仍登记为 openai-chat-completions，家族一致即可转发）。
 * usage 自带在响应/terminal 事件里，无需注入 stream_options。
 */
function buildOpenAiResponsesSpec(): ProtocolSpec {
  return {
    routePath: "/v1/responses",
    upstreamPath: "/v1/responses",
    family: "openai",
    errorResponse: openAiError,
    readModelId: (body) => (typeof body.model === "string" ? body.model.trim() : null),
    readMaxTokens: (body) => readMaxTokensFrom(body.max_output_tokens ?? body.max_tokens),
    isStream: (body) => body.stream === true,
    buildUpstreamRequest: (_c, body, upstreamModel, _stream, upstream) => {
      const headers: Record<string, string> = { "content-type": "application/json" };
      if (upstream.apiKey) headers["authorization"] = `Bearer ${upstream.apiKey}`;
      return { headers, bodyText: JSON.stringify({ ...body, model: upstreamModel }) };
    },
    parseUsageFromJson: (text) => {
      try {
        return parseUsageFromResponsesJson(JSON.parse(text));
      } catch {
        return null;
      }
    },
    createStreamScanner: createResponsesSseUsageScanner,
  };
}

export function createGatewayApp(deps: GatewayDeps): Hono {
  const app = new Hono();
  const trustedProxyIps = new Set((deps.env.trustedProxyIps ?? []).map((value) => {
    const ip = normalizeIp(value);
    if (!ip) throw new Error("可信代理必须使用有效的 IPv4/IPv6 地址");
    return ip;
  }));
  const secretBox: SecretBox | null = deps.env.platformSecretKey
    ? createSecretBox(deps.env.platformSecretKey)
    : null;

  app.onError((error, c) => {
    deps.logger.error("网关请求处理失败", { path: c.req.path, error });
    return anthropicError(500, "api_error", "网关内部错误");
  });

  app.get("/healthz", (c) => c.json({ ok: true }));
  app.get("/v1/models", (c) => handleModelsRequest(c, deps));

  for (const spec of [buildAnthropicSpec(), buildOpenAiSpec(), buildOpenAiResponsesSpec()]) {
    app.post(spec.routePath, (c) => {
      // app.request() 不提供 Node env，无法验证来源时不采信转发头。
      const incoming = (c.env as { incoming?: { socket?: { remoteAddress?: string } } } | undefined)?.incoming;
      const clientIp = resolveClientIp(incoming?.socket?.remoteAddress, c.req.raw.headers, trustedProxyIps);
      return handleProtocolRequest(c, deps, spec, secretBox, clientIp);
    });
  }

  return app;
}

async function handleProtocolRequest(
  c: Context,
  deps: GatewayDeps,
  spec: ProtocolSpec,
  secretBox: SecretBox | null,
  clientIp: string | null,
): Promise<Response> {
  // ---- 鉴权（与 /v1/models 共用，错误形状跟随本路由协议） ----
  const auth = await authenticateGatewayRequest(deps, c, spec.errorResponse);
  if (auth instanceof Response) return auth;
  const { device, user } = auth;
  const teams = await deps.store.listTeamsForUser(user.id);
  const primary = teams.find((entry) => !entry.team.disabledAt);
  if (!primary) {
    return spec.errorResponse(
      403,
      "permission_error",
      "账号未加入任何团队，请联系团队管理员",
    );
  }
  await deps.store.touchDeviceToken(device.id, new Date(deps.now()).toISOString());

  // ---- 请求体校验 ----
  let body: Record<string, unknown>;
  try {
    const parsed = await c.req.json();
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      return spec.errorResponse(400, "invalid_request_error", "请求体必须是 JSON 对象");
    }
    body = parsed as Record<string, unknown>;
  } catch {
    return spec.errorResponse(400, "invalid_request_error", "请求体不是合法 JSON");
  }
  const modelId = spec.readModelId(body);
  if (!modelId) {
    return spec.errorResponse(400, "invalid_request_error", "缺少 model 字段");
  }
  const model = await deps.store.getModel(modelId);
  if (!model || !model.enabled) {
    return spec.errorResponse(404, "not_found_error", `模型未在平台启用：${modelId}`);
  }
  const stream = spec.isStream(body);
  const maxTokens = spec.readMaxTokens(body);
  const rates = priceRates(model);
  const upstreamModel = model.upstreamModel?.trim() || model.modelId;

  // ---- 解析上游目标：模型绑定的供应商优先；未绑定回落 env 单上游（兼容既有部署） ----
  let upstreamBaseUrl = deps.env.upstreamBaseUrl;
  let upstreamApiKey = deps.env.upstreamApiKey;
  let upstreamAnthropicVersion = deps.env.anthropicVersion;
  let upstreamConnectTimeoutMs = deps.env.upstreamTimeoutMs;
  let upstreamIdleTimeoutMs = deps.env.upstreamIdleTimeoutMs;
  let upstreamProviderId: string | null = null;
  if (model.upstreamProviderId) {
    const provider = await deps.store.getUpstreamProvider(model.upstreamProviderId);
    if (!provider || !provider.enabled) {
      return spec.errorResponse(
        404,
        "not_found_error",
        `模型绑定的上游供应商已停用或不存在：${model.modelId}`,
      );
    }
    // 阶段一同构透传：按「家族」校验（openai 家族包含 chat 与 responses 两个端点）。
    const providerFamily = provider.protocol === "anthropic-messages" ? "anthropic" : "openai";
    if (providerFamily !== spec.family) {
      return spec.errorResponse(
        404,
        "not_found_error",
        `模型 ${model.modelId} 的上游协议（${provider.protocol}）与请求协议（${spec.family}）不一致`,
      );
    }
    // Responses 专用供应商（上游只提供 /v1/responses）不接受 chat 请求。
    if (
      provider.protocol === "openai-responses" &&
      spec.upstreamPath === "/v1/chat/completions"
    ) {
      return spec.errorResponse(
        404,
        "not_found_error",
        `模型 ${model.modelId} 绑定的供应商按 Responses 协议接入，不支持 chat completions 请求`,
      );
    }
    if (provider.apiKeyCiphertext) {
      if (!secretBox) {
        return spec.errorResponse(500, "api_error", "平台密钥未配置，无法解析上游凭据");
      }
      try {
        upstreamApiKey = secretBox.decrypt(provider.apiKeyCiphertext);
      } catch {
        return spec.errorResponse(500, "api_error", "上游凭据解密失败（PLATFORM_SECRET_KEY 是否更换过？）");
      }
    } else {
      // 供应商未配密钥：不带鉴权头（如本地 ollama / 内网网关）。
      upstreamApiKey = "";
    }
    upstreamBaseUrl = provider.baseUrl;
    if (provider.anthropicVersion) upstreamAnthropicVersion = provider.anthropicVersion;
    if (provider.timeoutMs) {
      upstreamConnectTimeoutMs = provider.timeoutMs;
      upstreamIdleTimeoutMs = provider.timeoutMs;
    }
    upstreamProviderId = provider.id;
  }

  // ---- 额度准入：预留（原子检查 + 预留，超限即 429） ----
  const estimateMicro = estimateMaxMicroCredits(maxTokens, rates.outputPerMtok);
  const reserved = await deps.store.tryReserveQuota(primary.team.id, estimateMicro);
  if (!reserved) {
    return spec.errorResponse(
      429,
      "rate_limit_error",
      "团队额度已用尽，请联系团队管理员充值 / Team quota exhausted",
    );
  }

  const requestId = c.req.header("x-request-id")?.trim() || randomId();
  const startedAt = deps.now();
  // 结算/释放互斥标记：失败路径与流式收尾（flush/cancel/abort）只允许生效一次，
  // 防止重复扣费或重复释放预留。
  let settled = false;

  const buildUsageRecord = (
    usage: UsageTokenCounts,
    status: UsageRecord["status"],
    errorType: string | null,
  ): UsageRecord => ({
    id: randomId(),
    requestId,
    userId: user.id,
    teamId: primary.team.id,
    deviceTokenId: device.id,
    model: model.modelId,
    upstreamModel,
    inputTokens: usage.inputTokens,
    outputTokens: usage.outputTokens,
    cacheReadTokens: usage.cacheReadTokens,
    cacheCreationTokens: usage.cacheCreationTokens,
    creditsMicro: computeUsageMicroCredits(usage, rates),
    status,
    errorType,
    clientIp,
    latencyMs: deps.now() - startedAt,
    providerId: upstreamProviderId,
    createdAt: new Date(deps.now()).toISOString(),
  });

  const failWithUsage = async (
    status: UsageRecord["status"],
    errorType: string,
  ): Promise<void> => {
    if (settled) return;
    settled = true;
    try {
      await deps.store.insertUsage(
        buildUsageRecord(
          { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheCreationTokens: 0 },
          status,
          errorType,
        ),
      );
      await deps.store.releaseReservation(primary.team.id, estimateMicro);
    } catch (error) {
      deps.logger.error("额度释放/失败记录写入失败", { requestId, error });
    }
  };

  const upstreamRequest = spec.buildUpstreamRequest(c, body, upstreamModel, stream, {
    apiKey: upstreamApiKey,
    anthropicVersion: upstreamAnthropicVersion,
  });

  // ---- 转发上游（客户端的平台凭据不外发，见 buildUpstreamRequest） ----
  // 限时：非流式覆盖连接到响应体读完；流式覆盖到响应头返回，响应体由
  // TransformStream 的空闲看门狗接管。没有超时时上游挂死会一直占用已预留额度。
  // 供应商记录可覆盖超时（timeout_ms），否则用 env 全局值。
  const abortUpstream = new AbortController();
  let upstreamTimedOut = false;
  let upstreamIdleFired = false;
  const headerTimeout = setTimeout(() => {
    upstreamTimedOut = true;
    abortUpstream.abort();
  }, upstreamConnectTimeoutMs);
  unrefTimer(headerTimeout);

  let upstream: Response;
  try {
    upstream = await deps.fetchUpstream(`${upstreamBaseUrl}${spec.upstreamPath}`, {
      method: "POST",
      headers: upstreamRequest.headers,
      body: upstreamRequest.bodyText,
      signal: abortUpstream.signal,
    });
  } catch (error) {
    clearTimeout(headerTimeout);
    deps.logger.warn("上游请求失败", { requestId, timedOut: upstreamTimedOut, error });
    await failWithUsage("error", upstreamTimedOut ? "upstream_timeout" : "upstream_unavailable");
    if (upstreamTimedOut) {
      return spec.errorResponse(504, "api_error", "上游响应超时，请稍后重试");
    }
    return spec.errorResponse(502, "api_error", "上游供应商暂时不可用，请稍后重试");
  }

  if (!upstream.ok) {
    clearTimeout(headerTimeout);
    const text = (await upstream.text().catch(() => "")).slice(0, MAX_UPSTREAM_ERROR_BYTES);
    deps.logger.warn("上游返回错误", { requestId, status: upstream.status });
    await failWithUsage("error", `upstream_${upstream.status}`);
    if (!text) {
      return spec.errorResponse(upstream.status, "api_error", "upstream error");
    }
    return new Response(text, {
      status: upstream.status,
      headers: {
        "content-type": upstream.headers.get("content-type") ?? "application/json",
      },
    });
  }

  // ---- 非流式：一次结算 ----
  if (!stream) {
    // header 限时继续覆盖整个响应体读取（非流式的总时限），读完成后才解除。
    let text: string;
    try {
      text = await upstream.text();
    } catch {
      // 此前该路径只会绕进 onError 返回 500 且不释放预留；现在按上游失败结算。
      clearTimeout(headerTimeout);
      const errorType = upstreamTimedOut ? "upstream_timeout" : "upstream_body_failure";
      deps.logger.warn("上游响应体读取失败", { requestId, errorType });
      await failWithUsage("error", errorType);
      return spec.errorResponse(
        upstreamTimedOut ? 504 : 502,
        "api_error",
        "上游响应异常，请稍后重试",
      );
    }
    clearTimeout(headerTimeout);
    let usage: UsageTokenCounts = {
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
    };
    try {
      usage = spec.parseUsageFromJson(text) ?? usage;
    } catch {
      // 上游 200 但响应不可解析：按 0 计量并记录，避免错扣。
      deps.logger.warn("上游响应无法解析 usage", { requestId });
    }
    try {
      await deps.store.settleUsage(buildUsageRecord(usage, "ok", null), estimateMicro);
    } catch (error) {
      deps.logger.error("结算写入失败", { requestId, error });
    }
    return new Response(text, {
      status: 200,
      headers: { "content-type": upstream.headers.get("content-type") ?? "application/json" },
    });
  }

  // ---- 流式：边透传边采集 usage，stream 结束时结算；客户端中断按 aborted 记录 ----
  if (!upstream.body) {
    await failWithUsage("error", "upstream_empty_body");
    return spec.errorResponse(502, "api_error", "上游返回空响应体");
  }
  // 响应头已就绪，从连接限时切换为空闲看门狗：连续 idleTimeout 毫秒无新字节
  // 视为上游挂死，abort 上游连接（这将触发下方 transform 的 abort 收尾）。
  clearTimeout(headerTimeout);
  const scanner = spec.createStreamScanner();
  const decoder = new TextDecoder();
  let idleTimer: ReturnType<typeof setTimeout> | null = null;
  const armIdleWatchdog = (): void => {
    if (idleTimer) clearTimeout(idleTimer);
    idleTimer = setTimeout(() => {
      upstreamIdleFired = true;
      abortUpstream.abort();
    }, upstreamIdleTimeoutMs);
    unrefTimer(idleTimer);
  };
  const clearIdleWatchdog = (): void => {
    if (idleTimer) {
      clearTimeout(idleTimer);
      idleTimer = null;
    }
  };
  const finalize = async (status: UsageRecord["status"]): Promise<void> => {
    if (settled) return;
    settled = true;
    clearIdleWatchdog();
    const dropped = scanner.dropped();
    if (dropped > 0) {
      deps.logger.warn("流式 usage 存在无法解析/被截断的事件帧", { requestId, dropped });
    }
    try {
      await deps.store.settleUsage(
        buildUsageRecord(scanner.usage(), status, status === "aborted" ? "client_aborted" : null),
        estimateMicro,
      );
    } catch (error) {
      deps.logger.error("流式结算写入失败", { requestId, error });
    }
  };
  // 字段已进 streams 规范且 Node 24 运行时支持；TS lib 的 Transformer 定义尚未收录，
  // 这里按运行时能力扩展声明（参数类型从 TransformStream 构造签名推导）。
  type TransformerWithAbort =
    ConstructorParameters<typeof TransformStream<Uint8Array, Uint8Array>>[0] & {
      abort?: (reason?: unknown) => void | Promise<void>;
    };
  const transformer: TransformerWithAbort = {
    transform(chunk: Uint8Array, controller: TransformStreamDefaultController<Uint8Array>) {
      armIdleWatchdog();
      try {
        scanner.push(decoder.decode(chunk, { stream: true }));
      } catch (error) {
        deps.logger.warn("usage 采集失败，不影响转发", { requestId, error });
      }
      controller.enqueue(chunk);
    },
    async flush() {
      try {
        scanner.push(decoder.decode());
      } catch {
        // 忽略尾部解码失败。
      }
      await finalize("ok");
    },
    async cancel() {
      // 空闲超时切断上游后，错误传到消费端常引发附带 cancel，且可能先于
      // transform.writable 的 abort 收尾：此时必须按上游失败记录，而非误判为客户端中断。
      if (upstreamIdleFired && !settled) {
        await failWithUsage("error", "upstream_idle_timeout");
        return;
      }
      // 客户端断开：按已采集 usage 结算，剩余预留由 settleUsage 释放。
      await finalize("aborted");
    },
    async abort() {
      // 上游流中断（含空闲超时）：此前该路径不经过任何收尾，会漏释放预留。
      clearIdleWatchdog();
      await failWithUsage(
        "error",
        upstreamIdleFired ? "upstream_idle_timeout" : "upstream_stream_failure",
      );
    },
  };
  const transform = new TransformStream<Uint8Array, Uint8Array>(transformer);
  return new Response(upstream.body.pipeThrough(transform), {
    status: 200,
    headers: {
      "content-type": upstream.headers.get("content-type") ?? "text/event-stream",
      "cache-control": "no-store",
    },
  });
}
