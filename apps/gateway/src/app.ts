import {
  computeUsageMicroCredits,
  estimateMaxMicroCredits,
  type ModelPriceRates,
  type UsageTokenCounts,
} from "@shucode-platform/contracts/credits";
import {
  hashToken,
  randomId,
  type DeviceTokenRecord,
  type PlatformModelRecord,
  type PlatformStore,
  type UsageRecord,
  type UserRecord,
} from "@shucode-platform/db";
import { Hono, type Context } from "hono";
import type { GatewayEnv } from "./env.js";
import { createAnthropicSseUsageScanner, parseUsageFromAnthropicJson } from "./usage.js";

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
const DEFAULT_MAX_TOKENS = 4096;
const MAX_UPSTREAM_ERROR_BYTES = 1_000_000;

function anthropicError(status: number, type: string, message: string): Response {
  return new Response(JSON.stringify({ type: "error", error: { type, message } }), {
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

/**
 * Anthropic 兼容模型网关：鉴权 → 额度准入（预留）→ 转发上游 → 计量结算。
 *
 * 额度契约：准入时按 max_tokens × 输出单价预留，响应完成后用上游 usage 结算差额；
 * 额度检查与扣减必须都在网关完成——客户端配置可被用户修改，不能成为额度执行点。
 */
export function createGatewayApp(deps: GatewayDeps): Hono {
  const app = new Hono();

  app.onError((error, c) => {
    deps.logger.error("网关请求处理失败", { path: c.req.path, error });
    return anthropicError(500, "api_error", "网关内部错误");
  });

  app.get("/healthz", (c) => c.json({ ok: true }));

  app.post("/v1/messages", async (c) => {
    // ---- 鉴权 ----
    const token = readGatewayToken(c);
    if (!token) {
      return anthropicError(401, "authentication_error", "缺少平台凭据（x-api-key 或 Bearer）");
    }
    const device: DeviceTokenRecord | null = await deps.store.findDeviceTokenByHash(
      hashToken(token),
    );
    if (!device || device.revokedAt) {
      return anthropicError(401, "authentication_error", "平台凭据无效或已吊销");
    }
    const user: UserRecord | null = await deps.store.findUserById(device.userId);
    if (!user || user.disabledAt) {
      return anthropicError(403, "permission_error", "账号不可用");
    }
    const teams = await deps.store.listTeamsForUser(user.id);
    const primary = teams.find((entry) => !entry.team.disabledAt);
    if (!primary) {
      return anthropicError(403, "permission_error", "账号未加入任何团队，请联系团队管理员");
    }
    await deps.store.touchDeviceToken(device.id, new Date(deps.now()).toISOString());

    // ---- 请求体校验 ----
    let body: Record<string, unknown>;
    try {
      const parsed = await c.req.json();
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
        return anthropicError(400, "invalid_request_error", "请求体必须是 JSON 对象");
      }
      body = parsed as Record<string, unknown>;
    } catch {
      return anthropicError(400, "invalid_request_error", "请求体不是合法 JSON");
    }
    const modelId = typeof body.model === "string" ? body.model.trim() : "";
    if (!modelId) {
      return anthropicError(400, "invalid_request_error", "缺少 model 字段");
    }
    const model = await deps.store.getModel(modelId);
    if (!model || !model.enabled) {
      return anthropicError(404, "not_found_error", `模型未在平台启用：${modelId}`);
    }
    const stream = body.stream === true;
    const rawMaxTokens = Number(body.max_tokens);
    const maxTokens =
      Number.isFinite(rawMaxTokens) && rawMaxTokens > 0
        ? Math.min(Math.floor(rawMaxTokens), MAX_MAX_TOKENS)
        : DEFAULT_MAX_TOKENS;
    const rates = priceRates(model);
    const upstreamModel = model.upstreamModel?.trim() || model.modelId;

    // ---- 额度准入：预留（原子检查 + 预留，超限即 429） ----
    const estimateMicro = estimateMaxMicroCredits(maxTokens, rates.outputPerMtok);
    const reserved = await deps.store.tryReserveQuota(primary.team.id, estimateMicro);
    if (!reserved) {
      return anthropicError(
        429,
        "rate_limit_error",
        "团队额度已用尽，请联系团队管理员充值 / Team quota exhausted",
      );
    }

    const requestId = c.req.header("x-request-id")?.trim() || randomId();
    const startedAt = deps.now();

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
      latencyMs: deps.now() - startedAt,
      createdAt: new Date(deps.now()).toISOString(),
    });

    const failWithUsage = async (
      status: UsageRecord["status"],
      errorType: string,
    ): Promise<void> => {
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

    const upstreamHeaders: Record<string, string> = {
      "content-type": "application/json",
      "anthropic-version": deps.env.anthropicVersion,
    };
    const clientVersion = c.req.header("anthropic-version")?.trim();
    if (clientVersion) upstreamHeaders["anthropic-version"] = clientVersion;
    const beta = c.req.header("anthropic-beta")?.trim();
    if (beta) upstreamHeaders["anthropic-beta"] = beta;
    if (deps.env.upstreamApiKey) upstreamHeaders["x-api-key"] = deps.env.upstreamApiKey;

    // ---- 转发上游 ----
    let upstream: Response;
    try {
      upstream = await deps.fetchUpstream(`${deps.env.upstreamBaseUrl}/v1/messages`, {
        method: "POST",
        headers: upstreamHeaders,
        body: JSON.stringify({ ...body, model: upstreamModel }),
      });
    } catch (error) {
      deps.logger.warn("上游请求失败", { requestId, error });
      await failWithUsage("error", "upstream_unavailable");
      return anthropicError(502, "api_error", "上游供应商暂时不可用，请稍后重试");
    }

    if (!upstream.ok) {
      const text = (await upstream.text().catch(() => "")).slice(0, MAX_UPSTREAM_ERROR_BYTES);
      deps.logger.warn("上游返回错误", { requestId, status: upstream.status });
      await failWithUsage("error", `upstream_${upstream.status}`);
      return new Response(text || JSON.stringify({ type: "error", error: { type: "api_error", message: "upstream error" } }), {
        status: upstream.status,
        headers: { "content-type": upstream.headers.get("content-type") ?? "application/json" },
      });
    }

    // ---- 非流式：一次结算 ----
    if (!stream) {
      const text = await upstream.text();
      let usage: UsageTokenCounts = {
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
      };
      try {
        usage = parseUsageFromAnthropicJson(JSON.parse(text)) ?? usage;
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
      return anthropicError(502, "api_error", "上游返回空响应体");
    }
    const scanner = createAnthropicSseUsageScanner();
    const decoder = new TextDecoder();
    let settled = false;
    const finalize = async (status: UsageRecord["status"]): Promise<void> => {
      if (settled) return;
      settled = true;
      try {
        await deps.store.settleUsage(
          buildUsageRecord(scanner.usage(), status, status === "aborted" ? "client_aborted" : null),
          estimateMicro,
        );
      } catch (error) {
        deps.logger.error("流式结算写入失败", { requestId, error });
      }
    };
    const transform = new TransformStream<Uint8Array, Uint8Array>({
      transform(chunk, controller) {
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
        await finalize("aborted");
      },
    });
    return new Response(upstream.body.pipeThrough(transform), {
      status: 200,
      headers: {
        "content-type": upstream.headers.get("content-type") ?? "text/event-stream",
        "cache-control": "no-store",
      },
    });
  });

  return app;
}
