import { describe, expect, it } from "vitest";
import { randomId } from "@shucode-platform/db";
import { hashToken, type PlatformStore } from "@shucode-platform/db";
import { createMemoryStore } from "@shucode-platform/db/memory";
import { createGatewayApp } from "./app.js";
import type { GatewayEnv } from "./env.js";

const DEVICE_TOKEN = `mdp_${"b".repeat(32)}`;

const ENV: GatewayEnv = {
  port: 8788,
  host: "127.0.0.1",
  databaseUrl: null,
  memoryDb: true,
  upstreamBaseUrl: "https://upstream.test",
  upstreamApiKey: "sk-upstream",
  anthropicVersion: "2023-06-01",
};

async function setup(options: {
  upstream: (input: string | URL, init: RequestInit) => Promise<Response>;
  quotaCredits?: string;
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
    env: ENV,
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
