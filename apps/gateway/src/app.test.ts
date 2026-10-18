import { describe, expect, it } from "vitest";
import { createSecretBox, randomId } from "@shucode-platform/db";
import { hashToken, type PlatformStore } from "@shucode-platform/db";
import { createMemoryStore } from "@shucode-platform/db/memory";
import { createGatewayApp } from "./app.js";
import type { GatewayEnv } from "./env.js";
import { createOpenAiSseUsageScanner } from "./usage.js";

const DEVICE_TOKEN = `mdp_${"b".repeat(32)}`;
const PROVIDER_SECRET_KEY = Buffer.alloc(32, 9).toString("base64");

const ENV: GatewayEnv = {
  port: 8788,
  host: "127.0.0.1",
  databaseUrl: null,
  memoryDb: true,
  upstreamBaseUrl: "https://upstream.test",
  upstreamApiKey: "sk-upstream",
  anthropicVersion: "2023-06-01",
  upstreamTimeoutMs: 120_000,
  upstreamIdleTimeoutMs: 60_000,
  platformSecretKey: null,
};

async function setup(options: {
  upstream: (input: string | URL, init: RequestInit) => Promise<Response>;
  quotaCredits?: string;
  envOverride?: Partial<GatewayEnv>;
}): Promise<{ app: ReturnType<typeof createGatewayApp>; store: PlatformStore; teamId: string }> {
  const store = await createMemoryStore();
  const user = await store.createUser({
    email: "member@test.dev",
    passwordHash: "x",
    displayName: "Member",
    role: "member",
  });
  const team = await store.createTeam({
    name: "团队",
    quotaCredits: options.quotaCredits ?? "100.000000",
    unlimited: false,
  });
  await store.addMember(team.id, user.id, "member");
  await store.createDeviceToken({
    id: randomId(),
    userId: user.id,
    name: "测试设备",
    platform: null,
    tokenHash: hashToken(DEVICE_TOKEN),
    tokenCiphertext: "unused",
    tokenHint: "abcd",
    createdAt: new Date().toISOString(),
    lastUsedAt: null,
    revokedAt: null,
  });
  await store.upsertModel({
    modelId: "test-model",
    displayName: "Test Model",
    inputPricePerMtok: "3",
    outputPricePerMtok: "15",
    cacheReadPricePerMtok: "0.3",
    cacheWritePricePerMtok: "3.75",
  });
  const app = createGatewayApp({
    store,
    env: { ...ENV, ...options.envOverride },
    now: Date.now,
    fetchUpstream: options.upstream,
    logger: { info: () => {}, warn: () => {}, error: () => {} },
  });
  return { app, store, teamId: team.id };
}

function messageRequest(body: Record<string, unknown>): RequestInit {
  return {
    method: "POST",
    headers: { "content-type": "application/json", "x-api-key": DEVICE_TOKEN },
    body: JSON.stringify(body),
  };
}

describe("模型网关", () => {
  it("非流式：转发上游并按 usage 结算到团队额度", async () => {
    const upstreamJson = JSON.stringify({
      id: "msg_1",
      type: "message",
      content: [{ type: "text", text: "hi" }],
      usage: { input_tokens: 1000, output_tokens: 500 },
    });
    const { app, store, teamId } = await setup({
      upstream: async () => new Response(upstreamJson, { status: 200 }),
    });

    const res = await app.request(
      "/v1/messages",
      messageRequest({ model: "test-model", max_tokens: 1024, messages: [] }),
    );
    expect(res.status).toBe(200);
    expect(await res.text()).toBe(upstreamJson);

    const team = await store.getTeam(teamId);
    // 1000*3 + 500*15 = 10500 micro = 0.0105 credit
    expect(team?.usedCredits).toBe("0.010500");
    expect(team?.reservedCredits).toBe("0.000000");

    const usage = await store.listUsage({ teamId });
    expect(usage).toHaveLength(1);
    expect(usage[0]?.status).toBe("ok");
    expect(usage[0]?.creditsMicro).toBe(10500);
  });

  it.each([
    ["198.51.100.20", "198.51.100.20"],
    ["::ffff:198.51.100.20", "198.51.100.20"],
    ["2001:db8::20", "2001:db8::20"],
    ["::1234", "::1234"],
    ["::ffff:0:203.0.113.7", "::ffff:0:cb00:7107"],
    ["10.0.0.2", "10.0.0.2"],
    ["127.0.0.1", "127.0.0.1"],
    ["::1", "::1"],
    ["fd00::2", "fd00::2"],
  ])("用量记录：未配置可信代理时对端 %s 忽略伪造头", async (remoteAddress, expectedIp) => {
    const { app, store, teamId } = await setup({
      upstream: async () => new Response("{}", { status: 200 }),
    });
    const request = messageRequest({ model: "test-model", max_tokens: 100, messages: [] });
    const res = await app.request(
      "/v1/messages",
      {
        ...request,
        headers: {
          ...request.headers,
          "x-forwarded-for": "203.0.113.7, 10.0.0.3",
          "x-real-ip": "203.0.113.8",
        },
      },
      { incoming: { socket: { remoteAddress } } },
    );
    expect(res.status).toBe(200);
    const usage = await store.listUsage({ teamId });
    expect(usage).toHaveLength(1);
    expect(usage[0]?.clientIp).toBe(expectedIp);
  });

  it("用量记录：没有 TCP 对端时不采信转发头，也不返回 500", async () => {
    const { app, store, teamId } = await setup({
      upstream: async () => new Response("{}", { status: 200 }),
    });
    const request = messageRequest({ model: "test-model", max_tokens: 100, messages: [] });
    const res = await app.request("/v1/messages", {
      ...request,
      headers: { ...request.headers, "x-forwarded-for": "203.0.113.7" },
    });
    expect(res.status).toBe(200);
    const usage = await store.listUsage({ teamId });
    expect(usage).toHaveLength(1);
    expect(usage[0]?.clientIp).toBeNull();
  });

  it.each([
    ["/v1/messages", false, false],
    ["/v1/messages", true, false],
    ["/v1/messages", false, true],
    ["/v1/chat/completions", false, false],
    ["/v1/chat/completions", true, false],
    ["/v1/chat/completions", false, true],
    ["/v1/responses", false, false],
    ["/v1/responses", true, false],
    ["/v1/responses", false, true],
  ])("用量记录：可信代理 %s stream=%s failed=%s 保留校验后的 IP", async (path, stream, failed) => {
    const { app, store, teamId } = await setup({
      envOverride: { trustedProxyIps: ["10.0.0.2", "10.0.0.3"] },
      upstream: async () => new Response(stream ? "data: {}\n\n" : "{}", { status: failed ? 502 : 200 }),
    });
    const request = messageRequest({ model: "test-model", max_tokens: 100, stream, messages: [], input: "hi" });
    const res = await app.request(
      path,
      {
        ...request,
        headers: { ...request.headers, "x-forwarded-for": "192.0.2.99, 203.0.113.7, 10.0.0.3" },
      },
      { incoming: { socket: { remoteAddress: "::ffff:10.0.0.2" } } },
    );
    expect(res.status).toBe(failed ? 502 : 200);
    await res.text();
    const usage = await store.listUsage({ teamId });
    expect(usage).toHaveLength(1);
    expect(usage[0]?.clientIp).toBe("203.0.113.7");
    expect(usage[0]?.status).toBe(failed ? "error" : "ok");
  });

  it("用量记录：非 mapped 的 ::ffff: IPv6 可信代理可正常启动并解析来源", async () => {
    const { app, store, teamId } = await setup({
      envOverride: { trustedProxyIps: ["::ffff:0:cb00:7107"] },
      upstream: async () => new Response("{}", { status: 200 }),
    });
    const request = messageRequest({ model: "test-model", max_tokens: 100, messages: [] });
    const res = await app.request(
      "/v1/messages",
      { ...request, headers: { ...request.headers, "x-forwarded-for": "198.51.100.20" } },
      { incoming: { socket: { remoteAddress: "::ffff:0:203.0.113.7" } } },
    );
    expect(res.status).toBe(200);
    const usage = await store.listUsage({ teamId });
    expect(usage).toHaveLength(1);
    expect(usage[0]?.clientIp).toBe("198.51.100.20");
  });

  it("流式：透传 SSE 并取 message_delta 的累积输出结算", async () => {
    const sse = [
      'event: message_start\ndata: {"type":"message_start","message":{"usage":{"input_tokens":1000,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}',
      'event: content_block_delta\ndata: {"type":"content_block_delta","delta":{"type":"text_delta","text":"he"}}',
      'event: message_delta\ndata: {"type":"message_delta","usage":{"output_tokens":200}}',
      'event: message_stop\ndata: {"type":"message_stop"}',
      "",
    ].join("\n\n");
    const { app, store, teamId } = await setup({
      upstream: async () =>
        new Response(sse, {
          status: 200,
          headers: { "content-type": "text/event-stream" },
        }),
    });

    const res = await app.request(
      "/v1/messages",
      messageRequest({ model: "test-model", max_tokens: 1024, stream: true, messages: [] }),
    );
    expect(res.status).toBe(200);
    const text = await res.text();
    expect(text).toContain("message_delta");

    const team = await store.getTeam(teamId);
    // 1000*3 + 200*15 = 6000 micro = 0.006 credit
    expect(team?.usedCredits).toBe("0.006000");
    expect(team?.reservedCredits).toBe("0.000000");
  });

  it("额度不足：准入拒绝且不触达上游", async () => {
    let upstreamCalled = false;
    const { app } = await setup({
      quotaCredits: "0.001000",
      upstream: async () => {
        upstreamCalled = true;
        return new Response("{}", { status: 200 });
      },
    });
    const res = await app.request(
      "/v1/messages",
      messageRequest({ model: "test-model", max_tokens: 4096, messages: [] }),
    );
    expect(res.status).toBe(429);
    const body = (await res.json()) as { error: { type: string } };
    expect(body.error.type).toBe("rate_limit_error");
    expect(upstreamCalled).toBe(false);
  });

  it("未知或未启用模型返回 404；上游失败释放预留", async () => {
    const { app, store, teamId } = await setup({
      upstream: async () => {
        throw new Error("upstream down");
      },
    });
    const unknown = await app.request(
      "/v1/messages",
      messageRequest({ model: "nope", max_tokens: 100, messages: [] }),
    );
    expect(unknown.status).toBe(404);

    const failed = await app.request(
      "/v1/messages",
      messageRequest({ model: "test-model", max_tokens: 1024, messages: [] }),
    );
    expect(failed.status).toBe(502);
    const team = await store.getTeam(teamId);
    expect(team?.reservedCredits).toBe("0.000000");
    expect(team?.usedCredits).toBe("0.000000");
    const usage = await store.listUsage({ teamId });
    expect(usage[0]?.status).toBe("error");
  });

  it("GET /v1/models：设备凭据返回已启用模型清单；无凭据/吊销 401", async () => {
    const { app, store } = await setup({ upstream: async () => new Response("{}") });

    const res = await app.request("/v1/models", { headers: { "x-api-key": DEVICE_TOKEN } });
    expect(res.status).toBe(200);
    const body = (await res.json()) as {
      object: string;
      data: Array<{ id: string; object: string; created: number; owned_by: string }>;
    };
    expect(body.object).toBe("list");
    expect(body.data.map((model) => model.id)).toContain("test-model");
    const first = body.data[0];
    expect(first?.object).toBe("model");
    expect(first?.created).toBeGreaterThan(0);
    expect(first?.owned_by).toBe("mendao-platform");

    const noAuth = await app.request("/v1/models");
    expect(noAuth.status).toBe(401);
    const noAuthBody = (await noAuth.json()) as { error: { type: string } };
    expect(noAuthBody.error.type).toBe("authentication_error");

    const records = await store.listDeviceTokens();
    const token = records[0];
    if (!token) throw new Error("missing device token");
    await store.revokeDeviceToken(token.id, new Date().toISOString());
    const revoked = await app.request("/v1/models", { headers: { "x-api-key": DEVICE_TOKEN } });
    expect(revoked.status).toBe(401);
  });

  it("无凭据 / 已吊销凭据返回 401", async () => {
    const { app, store } = await setup({ upstream: async () => new Response("{}") });
    const noAuth = await app.request("/v1/messages", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ model: "test-model", max_tokens: 10, messages: [] }),
    });
    expect(noAuth.status).toBe(401);

    const records = await store.listDeviceTokens();
    const token = records[0];
    if (!token) throw new Error("missing device token");
    await store.revokeDeviceToken(token.id, new Date().toISOString());
    const revoked = await app.request(
      "/v1/messages",
      messageRequest({ model: "test-model", max_tokens: 10, messages: [] }),
    );
    expect(revoked.status).toBe(401);
  });
});

describe("模型网关：OpenAI chat 协议", () => {
  it("非流式：透传 /v1/chat/completions 并按 OpenAI usage（缓存扣减）结算", async () => {
    let captured: RequestInit | undefined;
    const upstreamJson = JSON.stringify({
      id: "chatcmpl_1",
      choices: [{ message: { role: "assistant", content: "hi" } }],
      usage: {
        prompt_tokens: 1000,
        completion_tokens: 500,
        prompt_tokens_details: { cached_tokens: 400 },
      },
    });
    const { app, store, teamId } = await setup({
      upstream: async (_input, init) => {
        captured = init;
        return new Response(upstreamJson, { status: 200 });
      },
    });

    const res = await app.request(
      "/v1/chat/completions",
      messageRequest({ model: "test-model", max_tokens: 1024, messages: [] }),
    );
    expect(res.status).toBe(200);

    // 上游收到 Bearer 鉴权（不是转发客户端的平台凭据）。
    expect((captured?.headers as Record<string, string>)?.authorization).toBe(
      "Bearer sk-upstream",
    );

    const team = await store.getTeam(teamId);
    // fresh input 600×3 + output 500×15 + cacheRead 400×0.3 = 9420 micro
    expect(team?.usedCredits).toBe("0.009420");

    const usage = await store.listUsage({ teamId });
    expect(usage[0]?.status).toBe("ok");
    expect(usage[0]?.inputTokens).toBe(600);
    expect(usage[0]?.cacheReadTokens).toBe(400);
    expect(usage[0]?.outputTokens).toBe(500);
  });

  it("流式：注入 stream_options.include_usage 并按最终 usage chunk 结算", async () => {
    let capturedBody: string | undefined;
    const sse = [
      'data: {"id":"c1","choices":[{"delta":{"content":"he"}}]}',
      'data: {"id":"c1","choices":[{"delta":{"content":"llo"}}]}',
      'data: {"id":"c1","choices":[],"usage":{"prompt_tokens":2000,"completion_tokens":300,"prompt_cache_hit_tokens":1500}}',
      "data: [DONE]",
      "",
    ].join("\n\n");
    const { app, store, teamId } = await setup({
      upstream: async (_input, init) => {
        capturedBody = String(init?.body ?? "");
        return new Response(sse, {
          status: 200,
          headers: { "content-type": "text/event-stream" },
        });
      },
    });

    const res = await app.request(
      "/v1/chat/completions",
      messageRequest({ model: "test-model", max_tokens: 1024, stream: true, messages: [] }),
    );
    expect(res.status).toBe(200);
    expect(await res.text()).toContain("[DONE]");

    const payload = JSON.parse(capturedBody ?? "{}") as {
      model: string;
      stream_options?: { include_usage?: boolean };
    };
    expect(payload.stream_options?.include_usage).toBe(true);

    // fresh input 500×3 + cacheRead 1500×0.3 + output 300×15 = 6450 micro
    // （DeepSeek 风格缓存字段兼容；prompt_tokens 为含缓存的总量）。
    const team = await store.getTeam(teamId);
    expect(team?.usedCredits).toBe("0.006450");
    expect(team?.reservedCredits).toBe("0.000000");
  });

  it("openai 途径的鉴权错误返回 OpenAI 错误形状", async () => {
    const { app } = await setup({ upstream: async () => new Response("{}") });
    const res = await app.request("/v1/chat/completions", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ model: "test-model", max_tokens: 10, messages: [] }),
    });
    expect(res.status).toBe(401);
    const body = (await res.json()) as { error: { type: string } };
    expect(body.error.type).toBe("authentication_error");
  });
});

describe("模型网关：上游超时与流中断", () => {
  it("连接超时：返回 504、errorType=upstream_timeout 并释放预留", async () => {
    const { app, store, teamId } = await setup({
      envOverride: { upstreamTimeoutMs: 30 },
      upstream: (_input, init) =>
        new Promise<Response>((_resolve, reject) => {
          (init.signal as AbortSignal).addEventListener("abort", () =>
            reject(new Error("aborted")),
          );
        }),
    });
    const res = await app.request(
      "/v1/messages",
      messageRequest({ model: "test-model", max_tokens: 1024, messages: [] }),
    );
    expect(res.status).toBe(504);
    const team = await store.getTeam(teamId);
    expect(team?.reservedCredits).toBe("0.000000");
    const usage = await store.listUsage({ teamId });
    expect(usage[0]?.status).toBe("error");
    expect(usage[0]?.errorType).toBe("upstream_timeout");
  });

  it("响应体读取超时：释放预留并返回 504（此前该路径漏结算）", async () => {
    const { app, store, teamId } = await setup({
      envOverride: { upstreamTimeoutMs: 30 },
      upstream: (_input, init) => {
        // 模拟 fetch 语义：signal abort 会中断响应体流。
        const signal = init.signal as AbortSignal;
        return Promise.resolve(
          new Response(
            new ReadableStream<Uint8Array>({
              start(controller) {
                signal.addEventListener("abort", () => controller.error(new Error("timeout")));
              },
            }),
            { status: 200, headers: { "content-type": "application/json" } },
          ),
        );
      },
    });
    const res = await app.request(
      "/v1/messages",
      messageRequest({ model: "test-model", max_tokens: 1024, messages: [] }),
    );
    expect(res.status).toBe(504);
    const team = await store.getTeam(teamId);
    expect(team?.reservedCredits).toBe("0.000000");
    const usage = await store.listUsage({ teamId });
    expect(usage[0]?.status).toBe("error");
    expect(usage[0]?.errorType).toBe("upstream_timeout");
  });

  it("流式空闲超时：中断上游、释放预留并记录 error（此前漏释放预留）", async () => {
    const encoder = new TextEncoder();
    const { app, store, teamId } = await setup({
      envOverride: { upstreamIdleTimeoutMs: 30 },
      upstream: (_input, init) => {
        const signal = init.signal as AbortSignal;
        return Promise.resolve(
          new Response(
            new ReadableStream<Uint8Array>({
              start(controller) {
                controller.enqueue(encoder.encode('data: {"choices":[]}\n\n'));
                signal.addEventListener("abort", () => controller.error(new Error("idle")));
              },
            }),
            { status: 200, headers: { "content-type": "text/event-stream" } },
          ),
        );
      },
    });
    const res = await app.request(
      "/v1/chat/completions",
      messageRequest({ model: "test-model", max_tokens: 1024, stream: true, messages: [] }),
    );
    expect(res.status).toBe(200);
    // 空闲看门狗切断上游流，转发给客户端的响应体随之终止。
    await expect(res.text()).rejects.toThrow();
    // 等 abort 收尾（failWithUsage 异步写库）完成。
    let team = await store.getTeam(teamId);
    for (let i = 0; i < 50 && team?.reservedCredits !== "0.000000"; i += 1) {
      await new Promise((resolve) => setTimeout(resolve, 10));
      team = await store.getTeam(teamId);
    }
    expect(team?.reservedCredits).toBe("0.000000");
    const usage = await store.listUsage({ teamId });
    expect(usage[0]?.status).toBe("error");
    expect(usage[0]?.errorType).toBe("upstream_idle_timeout");
  });

  it("SSE 扫描：buffer 超限丢弃并计数，非 JSON 帧不再静默吞掉", async () => {
    const scanner = createOpenAiSseUsageScanner();
    scanner.push("data: not-json\n\n");
    // 单个未完结事件超过 1MB 上限：整体丢弃并计数。
    scanner.push(`data: ${"x".repeat(1_100_000)}\n\n`);
    scanner.push(
      'data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}\n\n',
    );
    expect(scanner.dropped()).toBe(2);
    expect(scanner.usage().outputTokens).toBe(5);
  });
});

describe("模型网关：多上游供应商", () => {
  it("绑定供应商的模型按供应商转发与计量；未绑定模型回落 env 单上游", async () => {
    const captured: Array<{ url: string; auth: string }> = [];
    const { app, store, teamId } = await setup({
      envOverride: { platformSecretKey: PROVIDER_SECRET_KEY },
      upstream: async (input, init) => {
        captured.push({
          url: String(input),
          auth: (init.headers as Record<string, string>)["authorization"] ?? "",
        });
        return new Response(JSON.stringify({ choices: [], usage: {} }), {
          status: 200,
          headers: { "content-type": "application/json" },
        });
      },
    });
    const box = createSecretBox(PROVIDER_SECRET_KEY);
    const provider = await store.createUpstreamProvider({
      name: "ProviderA",
      baseUrl: "https://a.test",
      protocol: "openai-chat-completions",
      apiKeyCiphertext: box.encrypt("sk-a"),
      apiKeyHint: "sk-a",
    });
    await store.upsertModel({
      modelId: "bound-model",
      displayName: "Bound",
      inputPricePerMtok: "3",
      outputPricePerMtok: "15",
      upstreamModel: "real-a",
      upstreamProviderId: provider.id,
    });

    const bound = await app.request(
      "/v1/chat/completions",
      messageRequest({ model: "bound-model", max_tokens: 100, messages: [] }),
    );
    expect(bound.status).toBe(200);
    // 解密后的供应商密钥 + 供应商 base_url（不带 /v1，路径按协议拼接）。
    expect(captured[0]?.url).toBe("https://a.test/v1/chat/completions");
    expect(captured[0]?.auth).toBe("Bearer sk-a");
    let usage = await store.listUsage({ teamId });
    expect(usage.find((record) => record.model === "bound-model")?.providerId).toBe(provider.id);

    // 未绑定供应商的模型回落 env 单上游与 env 密钥。
    const fallback = await app.request(
      "/v1/chat/completions",
      messageRequest({ model: "test-model", max_tokens: 100, messages: [] }),
    );
    expect(fallback.status).toBe(200);
    expect(captured[1]?.url).toBe("https://upstream.test/v1/chat/completions");
    expect(captured[1]?.auth).toBe("Bearer sk-upstream");
    usage = await store.listUsage({ teamId });
    expect(usage.find((record) => record.model === "test-model")?.providerId).toBeNull();
  });

  it("协议不一致与供应商停用时边界拒绝且不触达上游", async () => {    let upstreamCalled = false;
    const { app, store } = await setup({
      upstream: async () => {
        upstreamCalled = true;
        return new Response("{}", { status: 200 });
      },
    });
    const anthropicProvider = await store.createUpstreamProvider({
      name: "Anthropic",
      baseUrl: "https://api.anthropic.com",
      protocol: "anthropic-messages",
      apiKeyCiphertext: null,
      apiKeyHint: null,
    });
    await store.upsertModel({
      modelId: "mix-model",
      displayName: "Mix",
      inputPricePerMtok: "1",
      outputPricePerMtok: "1",
      upstreamProviderId: anthropicProvider.id,
    });

    const mismatch = await app.request(
      "/v1/chat/completions",
      messageRequest({ model: "mix-model", max_tokens: 10, messages: [] }),
    );
    expect(mismatch.status).toBe(404);
    expect(((await mismatch.json()) as { error: { message: string } }).error.message).toContain(
      "协议",
    );

    await store.updateUpstreamProvider(anthropicProvider.id, { enabled: false });
    const disabled = await app.request(
      "/v1/messages",
      messageRequest({ model: "mix-model", max_tokens: 10, messages: [] }),
    );
    expect(disabled.status).toBe(404);
    expect(upstreamCalled).toBe(false);
  });

  it("Responses 专用供应商：接受 /v1/responses、拒绝 chat 请求且不触达上游", async () => {
    let upstreamCalled = false;
    const { app, store } = await setup({
      upstream: async () => {
        upstreamCalled = true;
        return new Response(
          JSON.stringify({ id: "r1", usage: { input_tokens: 100, output_tokens: 10 } }),
          { status: 200, headers: { "content-type": "application/json" } },
        );
      },
    });
    const provider = await store.createUpstreamProvider({
      name: "ResponsesOnly",
      baseUrl: "https://resp.test",
      protocol: "openai-responses",
      apiKeyCiphertext: null,
      apiKeyHint: null,
    });
    await store.upsertModel({
      modelId: "resp-model",
      displayName: "Resp",
      inputPricePerMtok: "1",
      outputPricePerMtok: "1",
      upstreamProviderId: provider.id,
    });

    const chatRes = await app.request(
      "/v1/chat/completions",
      messageRequest({ model: "resp-model", max_tokens: 10, messages: [] }),
    );
    expect(chatRes.status).toBe(404);
    expect(((await chatRes.json()) as { error: { message: string } }).error.message).toContain(
      "Responses",
    );
    expect(upstreamCalled).toBe(false);

    const respRes = await app.request(
      "/v1/responses",
      messageRequest({ model: "resp-model", max_output_tokens: 10, input: "hi" }),
    );
    expect(respRes.status).toBe(200);
    expect(upstreamCalled).toBe(true);
  });
});

describe("模型网关：OpenAI Responses API", () => {
  it("非流式：透传 /v1/responses 并按 input/output_tokens（缓存扣减）结算", async () => {
    let capturedUrl = "";
    let capturedAuth = "";
    const upstreamJson = JSON.stringify({
      id: "resp_1",
      object: "response",
      status: "completed",
      output: [],
      usage: {
        input_tokens: 1000,
        output_tokens: 500,
        input_tokens_details: { cached_tokens: 400 },
      },
    });
    const { app, store, teamId } = await setup({
      upstream: async (input, init) => {
        capturedUrl = String(input);
        capturedAuth = (init.headers as Record<string, string>)["authorization"] ?? "";
        return new Response(upstreamJson, {
          status: 200,
          headers: { "content-type": "application/json" },
        });
      },
    });

    const res = await app.request(
      "/v1/responses",
      messageRequest({ model: "test-model", max_output_tokens: 1024, input: "hi" }),
    );
    expect(res.status).toBe(200);
    expect(await res.text()).toBe(upstreamJson);
    expect(capturedUrl).toBe("https://upstream.test/v1/responses");
    expect(capturedAuth).toBe("Bearer sk-upstream");

    // fresh input 600×3 + output 500×15 + cacheRead 400×0.3 = 9420 micro
    const team = await store.getTeam(teamId);
    expect(team?.usedCredits).toBe("0.009420");
    const usage = await store.listUsage({ teamId });
    expect(usage[0]?.inputTokens).toBe(600);
    expect(usage[0]?.cacheReadTokens).toBe(400);
    expect(usage[0]?.outputTokens).toBe(500);
    expect(usage[0]?.status).toBe("ok");
  });

  it("流式：按 response.completed 事件的 response.usage 结算", async () => {
    const sse = [
      'event: response.created\ndata: {"type":"response.created","response":{"id":"r1"}}',
      'event: response.output_text.delta\ndata: {"type":"response.output_text.delta","delta":"he"}',
      'event: response.completed\ndata: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":2000,"output_tokens":300,"input_tokens_details":{"cached_tokens":1500}}}}',
      "",
    ].join("\n\n");
    const { app, store, teamId } = await setup({
      upstream: async () => new Response(sse, { status: 200, headers: { "content-type": "text/event-stream" } }),
    });

    const res = await app.request(
      "/v1/responses",
      messageRequest({ model: "test-model", max_output_tokens: 1024, stream: true, input: ["hi"] }),
    );
    expect(res.status).toBe(200);
    expect(await res.text()).toContain("response.completed");

    // fresh input 500×3 + output 300×15 + cacheRead 1500×0.3 = 6450 micro
    const team = await store.getTeam(teamId);
    expect(team?.usedCredits).toBe("0.006450");
    expect(team?.reservedCredits).toBe("0.000000");
  });
});
