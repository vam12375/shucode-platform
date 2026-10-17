import { describe, expect, it, vi } from "vitest";
import {
  createSecretBox,
  hashToken,
  randomId,
  type DeviceTokenRecord,
} from "@shucode-platform/db";
import { createMemoryStore } from "@shucode-platform/db/memory";
import { createApp } from "./app.js";
import type { AppContext } from "./context.js";
import { loadPlatformEnv } from "./env.js";
import { createConfigEventBus, type ConfigEventDomain } from "./services/config-events.js";

const SECRET_KEY = Buffer.alloc(32, 7).toString("base64");
const POLL_TOKEN = "a".repeat(64);

async function createTestContext(): Promise<AppContext> {
  const store = await createMemoryStore({ demoSeed: true });
  const env = loadPlatformEnv({
    PORT: "8787",
    MEMORY_DB: "1",
    PLATFORM_SECRET_KEY: SECRET_KEY,
    PUBLIC_API_ORIGIN: "http://localhost:8787",
    GATEWAY_BASE_URL: "https://gw.mendao.chat",
  });
  return {
    store,
    env,
    now: Date.now,
    secretBox: createSecretBox(SECRET_KEY),
    configEvents: createConfigEventBus(),
    logger: { info: () => {}, warn: () => {}, error: () => {} },
  };
}

function formBody(values: Record<string, string>): URLSearchParams {
  return new URLSearchParams(values);
}

describe("平台设备登录流", () => {
  it("init → 登录页 → 授权 → poll 领取凭据 → user-config → 吊销后 401", async () => {
    const context = await createTestContext();
    const app = createApp(context);

    // 1. 客户端发起登录
    const initRes = await app.request("/api/v1/platform/auth/init", {
      method: "POST",
      headers: {
        authorization: `Bearer ${POLL_TOKEN}`,
        "content-type": "application/json",
      },
      body: JSON.stringify({ device_name: "测试 Mac", platform: "darwin-arm64" }),
    });
    expect(initRes.status).toBe(200);
    const init = (await initRes.json()) as {
      data: { flow_id: string; authorize_url: string; expires_at: number };
    };
    const flowId = init.data.flow_id;
    expect(init.data.authorize_url).toContain(`/app/platform/login?flow_id=${flowId}`);

    // 2. 浏览器打开登录页（未登录 → 登录表单）
    const loginPage = await app.request(`/app/platform/login?flow_id=${flowId}`);
    expect(loginPage.status).toBe(200);
    expect(await loginPage.text()).toContain("测试 Mac");

    // 3. 提交登录表单 → 会话 cookie
    const loginRes = await app.request("/app/platform/login", {
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded" },
      body: formBody({
        flow_id: flowId,
        email: "member@mendao.chat",
        password: "mendao-member",
      }),
    });
    expect(loginRes.status).toBe(303);
    const cookie = loginRes.headers.get("set-cookie")?.split(";")[0] ?? "";
    expect(cookie).toContain("mendao_session=");

    // 4. 授权页 → 批准
    const authorizeRes = await app.request("/app/platform/authorize", {
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded", cookie },
      body: formBody({ flow_id: flowId, decision: "approve" }),
    });
    expect(authorizeRes.status).toBe(303);

    // 5. 客户端轮询领取凭据（只签发一次）
    const pollRes = await app.request(`/api/v1/platform/auth/poll/${flowId}`, {
      headers: { authorization: `Bearer ${POLL_TOKEN}` },
    });
    const poll = (await pollRes.json()) as {
      data: { status: string; credentials?: { token: string; token_id: string } };
    };
    expect(poll.data.status).toBe("authorized");
    const deviceToken = poll.data.credentials?.token ?? "";
    expect(deviceToken.startsWith("mdp_")).toBe(true);

    const secondPoll = await app.request(`/api/v1/platform/auth/poll/${flowId}`, {
      headers: { authorization: `Bearer ${POLL_TOKEN}` },
    });
    const secondPollBody = (await secondPoll.json()) as { data: { status: string } };
    expect(secondPollBody.data.status).toBe("consumed");

    // 6. user-config：网关 provider（含设备 token 的 access.apiKey）+ 团队模型
    const cfgRes = await app.request("/api/v1/client/user-config", {
      headers: { authorization: `Bearer ${deviceToken}` },
    });
    expect(cfgRes.status).toBe(200);
    const cfg = (await cfgRes.json()) as {
      data: {
        envelope: {
          syncId: string;
          personalConfig: {
            providerConfigRules: {
              providerRules: Array<{
                providerId: string;
                config: {
                  api: {
                    type: string;
                    baseUrl: string;
                  };
                  access: { type: string; apiKey?: string };
                };
              }>;
            };
            defaultModelSelection?: { modelId: string };
          };
          credentials: Array<{ key: string; value: string }>;
        };
      };
    };
    const envelope = cfg.data.envelope;
    const providerRule = envelope.personalConfig.providerConfigRules.providerRules[0];
    expect(providerRule?.providerId).toBe("platform:gateway");
    // 下发的是 OpenAI chat 协议（默认），baseUrl 带 /v1 前缀（客户端拼 /chat/completions）。
    expect(providerRule?.config.api.type).toBe("openai-chat-completions");
    expect(providerRule?.config.api.baseUrl).toBe("https://gw.mendao.chat/v1");
    // 设备 token 必须放在 config.access.apiKey：客户端 Registry 完整校验只认这里，
    // credentials 通道对普通 personal provider 不生效（缺 apiKey 会导致整份信封回滚）。
    expect(providerRule?.config.access.apiKey).toBe(deviceToken);
    expect(envelope.credentials).toEqual([]);
    expect(envelope.personalConfig.defaultModelSelection?.modelId).toBeTruthy();

    // ETag 协商缓存：配置未变化时整包轮询返回 304 空体。
    const etag = cfgRes.headers.get("etag") ?? "";
    expect(etag).not.toBe("");
    const notModified = await app.request("/api/v1/client/user-config", {
      headers: { authorization: `Bearer ${deviceToken}`, "if-none-match": etag },
    });
    expect(notModified.status).toBe(304);

    // 7. 控制台吊销设备凭据 → user-config 401
    const consoleLogin = await app.request("/console/api/auth/login", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ email: "admin@mendao.chat", password: "mendao-admin" }),
    });
    expect(consoleLogin.status).toBe(200);
    const adminCookie = consoleLogin.headers.get("set-cookie")?.split(";")[0] ?? "";

    const devicesRes = await app.request("/console/api/devices", {
      headers: { cookie: adminCookie },
    });
    const devices = (await devicesRes.json()) as {
      data: { devices: Array<{ id: string; userEmail: string }> };
    };
    const target = devices.data.devices.find((device) => device.userEmail === "member@mendao.chat");
    expect(target).toBeTruthy();

    const revokeRes = await app.request(`/console/api/devices/${target?.id}/revoke`, {
      method: "POST",
      headers: { cookie: adminCookie },
    });
    expect(revokeRes.status).toBe(200);

    const afterRevoke = await app.request("/api/v1/client/user-config", {
      headers: { authorization: `Bearer ${deviceToken}` },
    });
    expect(afterRevoke.status).toBe(401);
  });

  it("未登录直接打开授权页会被重定向回登录表单", async () => {
    const context = await createTestContext();
    const app = createApp(context);
    const initRes = await app.request("/api/v1/platform/auth/init", {
      method: "POST",
      headers: { authorization: `Bearer ${POLL_TOKEN}` },
      body: JSON.stringify({ device_name: "X" }),
    });
    const init = (await initRes.json()) as { data: { flow_id: string } };
    const res = await app.request("/app/platform/authorize", {
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded" },
      body: formBody({ flow_id: init.data.flow_id, decision: "approve" }),
    });
    expect(res.status).toBe(303);
    expect(res.headers.get("location")).toContain("/app/platform/login");
  });
});

describe("公开客户端配置与 Release", () => {
  it("/api/v1/client/configs 返回兼容信封；http 环境不下发内置配置地址", async () => {
    const context = await createTestContext();
    const app = createApp(context);

    const res = await app.request("/api/v1/client/configs?app_version=1.0.0&platform=darwin");
    expect(res.status).toBe(200);
    const body = (await res.json()) as {
      code: number;
      data: { configs: Record<string, unknown> };
    };
    expect(body.code).toBe(0);
    expect(body.data.configs.builtin_provider_config_json).toBeUndefined();

    const nRes = await app.request("/n/configs");
    expect(nRes.status).toBe(200);
    expect(((await nRes.json()) as { code: number }).code).toBe(0);
  });

  it("发布 Release 后 latest.json 返回内容且 revision 递增", async () => {
    const context = await createTestContext();
    const app = createApp(context);
    const consoleLogin = await app.request("/console/api/auth/login", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ email: "admin@mendao.chat", password: "mendao-admin" }),
    });
    const adminCookie = consoleLogin.headers.get("set-cookie")?.split(";")[0] ?? "";

    const releaseContent = {
      schemaVersion: 1,
      revision: 100,
      config: {
        providerConfigRules: { templateRules: [], providerRules: [] },
        modelConfigRules: { modelRules: [], modelApiRules: [], providerSiteRules: [], templateModelRules: [], builtinProviderModelRules: [] },
      },
    };
    const publishRes = await app.request("/console/api/releases", {
      method: "POST",
      headers: { "content-type": "application/json", cookie: adminCookie },
      body: JSON.stringify({ content: releaseContent }),
    });
    expect(publishRes.status).toBe(200);
    const published = (await publishRes.json()) as { data: { revision: number } };
    expect(published.data.revision).toBe(1);

    const latest = await app.request("/api/v1/releases/builtin/latest.json");
    expect(latest.status).toBe(200);
    const latestBody = (await latest.json()) as { revision: number };
    expect(latestBody.revision).toBe(100);
  });
});

describe("后台用户管理", () => {
  async function loginAdmin(app: ReturnType<typeof createApp>): Promise<{ cookie: string; adminId: string }> {
    const consoleLogin = await app.request("/console/api/auth/login", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ email: "admin@mendao.chat", password: "mendao-admin" }),
    });
    expect(consoleLogin.status).toBe(200);
    const me = await app.request("/console/api/auth/me", {
      headers: { cookie: consoleLogin.headers.get("set-cookie")?.split(";")[0] ?? "" },
    });
    const meBody = (await me.json()) as { data: { user: { id: string } } };
    return {
      cookie: consoleLogin.headers.get("set-cookie")?.split(";")[0] ?? "",
      adminId: meBody.data.user.id,
    };
  }

  async function patchUser(
    app: ReturnType<typeof createApp>,
    cookie: string,
    id: string,
    body: Record<string, unknown>,
  ): Promise<Response> {
    return app.request(`/console/api/users/${id}`, {
      method: "PATCH",
      headers: { "content-type": "application/json", ...(cookie ? { cookie } : {}) },
      body: JSON.stringify(body),
    });
  }

  it("创建 → 禁用挡平台登录 → 启用并重置密码 → 自我保护与 404", async () => {
    const context = await createTestContext();
    const app = createApp(context);
    const { cookie, adminId } = await loginAdmin(app);

    // 创建成员用户
    const createRes = await app.request("/console/api/users", {
      method: "POST",
      headers: { "content-type": "application/json", cookie },
      body: JSON.stringify({
        email: "alice@mendao.chat",
        password: "alice-password",
        displayName: "Alice",
        role: "member",
      }),
    });
    expect(createRes.status).toBe(200);
    const created = (await createRes.json()) as { data: { user: { id: string } } };
    const aliceId = created.data.user.id;

    // 发起平台登录流：禁用前成员可正常提交登录表单
    const initRes = await app.request("/api/v1/platform/auth/init", {
      method: "POST",
      headers: { authorization: `Bearer ${POLL_TOKEN}`, "content-type": "application/json" },
      body: JSON.stringify({ device_name: "Alice 的手机", platform: "ios" }),
    });
    const flowId = ((await initRes.json()) as { data: { flow_id: string } }).data.flow_id;
    const loginOk = await app.request("/app/platform/login", {
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded" },
      body: formBody({ flow_id: flowId, email: "alice@mendao.chat", password: "alice-password" }),
    });
    expect(loginOk.status).toBe(303);

    // 禁用后平台登录立即被拒（登录、网关、user-config 都校验 disabledAt）
    const disableRes = await patchUser(app, cookie, aliceId, { disabled: true });
    expect(disableRes.status).toBe(200);
    const disabledBody = (await disableRes.json()) as {
      data: { user: { disabledAt: string | null } };
    };
    expect(disabledBody.data.user.disabledAt).toBeTruthy();
    const loginBlocked = await app.request("/app/platform/login", {
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded" },
      body: formBody({ flow_id: flowId, email: "alice@mendao.chat", password: "alice-password" }),
    });
    expect(loginBlocked.status).toBe(401);

    // 启用 + 重置密码后可重新登录
    const enableRes = await patchUser(app, cookie, aliceId, {
      disabled: false,
      password: "alice-password-2",
    });
    expect(enableRes.status).toBe(200);
    const reLogin = await app.request("/app/platform/login", {
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded" },
      body: formBody({ flow_id: flowId, email: "alice@mendao.chat", password: "alice-password-2" }),
    });
    expect(reLogin.status).toBe(303);

    // 自我保护：不能禁用自己、不能改自己的角色
    expect((await patchUser(app, cookie, adminId, { disabled: true })).status).toBe(400);
    expect((await patchUser(app, cookie, adminId, { role: "member" })).status).toBe(400);

    // 未登录不能调用；目标不存在时 404
    expect((await patchUser(app, "", aliceId, { disabled: true })).status).toBe(401);
    const missing = await patchUser(app, cookie, "ffffffff-ffff-ffff-ffff-ffffffffffff", {
      disabled: true,
    });
    expect(missing.status).toBe(404);
  });

  it("重置密码/禁用后吊销该用户的后台会话与设备凭据", async () => {
    const context = await createTestContext();
    const app = createApp(context);
    const { cookie } = await loginAdmin(app);

    async function issueDevice(userId: string): Promise<DeviceTokenRecord> {
      const token = `mdp_${"c".repeat(32)}`;
      const record: DeviceTokenRecord = {
        id: randomId(),
        userId,
        name: "测试设备",
        platform: null,
        tokenHash: hashToken(token),
        tokenCiphertext: "unused",
        tokenHint: "abcd",
        createdAt: new Date().toISOString(),
        lastUsedAt: null,
        revokedAt: null,
      };
      await context.store.createDeviceToken(record);
      return record;
    }

    // 创建第二管理员并登录（控制台仅 admin 可登录），得到一个存活的旧会话。
    const createRes = await app.request("/console/api/users", {
      method: "POST",
      headers: { "content-type": "application/json", cookie },
      body: JSON.stringify({
        email: "bob@mendao.chat",
        password: "bob-password",
        displayName: "Bob",
        role: "admin",
      }),
    });
    expect(createRes.status).toBe(200);
    const bob = (await createRes.json()) as { data: { user: { id: string } } };
    const bobId = bob.data.user.id;
    const bobDevice = await issueDevice(bobId);

    const bobLogin = await app.request("/console/api/auth/login", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ email: "bob@mendao.chat", password: "bob-password" }),
    });
    const bobCookie = bobLogin.headers.get("set-cookie")?.split(";")[0] ?? "";
    expect(
      (await app.request("/console/api/auth/me", { headers: { cookie: bobCookie } })).status,
    ).toBe(200);

    // 重置密码：旧后台会话立即 401，设备凭据同步吊销。
    expect((await patchUser(app, cookie, bobId, { password: "bob-password-2" })).status).toBe(200);
    expect(
      (await app.request("/console/api/auth/me", { headers: { cookie: bobCookie } })).status,
    ).toBe(401);
    const bobTokenAfter = await context.store.findDeviceTokenById(bobDevice.id);
    expect(bobTokenAfter?.revokedAt).toBeTruthy();

    // 禁用成员：设备凭据同样被吊销（网关/user-config 的 disabledAt 实时校验之外再断一层）。
    const createMemberRes = await app.request("/console/api/users", {
      method: "POST",
      headers: { "content-type": "application/json", cookie },
      body: JSON.stringify({
        email: "carol@mendao.chat",
        password: "carol-password",
        displayName: "Carol",
      }),
    });
    const carol = (await createMemberRes.json()) as { data: { user: { id: string } } };
    const carolDevice = await issueDevice(carol.data.user.id);
    expect((await patchUser(app, cookie, carol.data.user.id, { disabled: true })).status).toBe(200);
    const carolTokenAfter = await context.store.findDeviceTokenById(carolDevice.id);
    expect(carolTokenAfter?.revokedAt).toBeTruthy();

    // 仅改昵称（disabled=false 视为启用）不触发吊销性清理。
    const reNickRes = await patchUser(app, cookie, carol.data.user.id, {
      disabled: false,
      displayName: "Carol2",
    });
    expect(reNickRes.status).toBe(200);
    const carolTokenFinal = await context.store.findDeviceTokenById(carolDevice.id);
    expect(carolTokenFinal?.revokedAt).toBeTruthy(); // 吊销不可逆：启用不回补凭据
    const resurrect = (await reNickRes.json()) as { data: { user: { disabledAt: string | null } } };
    expect(resurrect.data.user.disabledAt).toBeNull();
  });
});

describe("上游供应商（多上游）", () => {
  it("创建（base_url 归一、密钥只回显掩码）→ 获取上游模型列表 → 绑定模型 → 未登录 401", async () => {
    const context = await createTestContext();
    const app = createApp(context);
    const consoleLogin = await app.request("/console/api/auth/login", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ email: "admin@mendao.chat", password: "mendao-admin" }),
    });
    const cookie = consoleLogin.headers.get("set-cookie")?.split(";")[0] ?? "";

    const fetchMock = vi.fn(
      async (_input: string | URL | Request, _init?: RequestInit) =>
        new Response(
          JSON.stringify({ object: "list", data: [{ id: "deepseek-v4-pro" }, { id: "deepseek-flash" }] }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
    );
    vi.stubGlobal("fetch", fetchMock);
    try {
      // 用户尾部的 /v1/ 会被归一剥掉（路径由平台按协议拼接）。
      const createRes = await app.request("/console/api/providers", {
        method: "POST",
        headers: { "content-type": "application/json", cookie },
        body: JSON.stringify({
          name: "DeepSeek",
          baseUrl: "https://api.deepseek.com/v1/",
          apiKey: "sk-test-1234",
          timeoutMs: 30000,
        }),
      });
      expect(createRes.status).toBe(200);
      const created = (await createRes.json()) as {
        data: { provider: { id: string; baseUrl: string; protocol: string; hasKey: boolean; apiKeyHint: string } };
      };
      expect(created.data.provider.baseUrl).toBe("https://api.deepseek.com");
      // 决策默认值：openai-chat-completions 优先。
      expect(created.data.provider.protocol).toBe("openai-chat-completions");
      expect(created.data.provider.hasKey).toBe(true);
      expect(created.data.provider.apiKeyHint).toBe("1234");
      // 密文/明文都不出后端。
      expect(JSON.stringify(created)).not.toContain("sk-test-1234");

      const listRes = await app.request(`/console/api/providers/${created.data.provider.id}/models`, {
        headers: { cookie },
      });
      expect(listRes.status).toBe(200);
      const list = (await listRes.json()) as { data: { models: string[] } };
      expect(list.data.models).toEqual(["deepseek-flash", "deepseek-v4-pro"]);
      // 代理请求：上游 /v1/models + Bearer 供应商密钥。
      const [calledUrl, calledInit] = fetchMock.mock.calls[0] ?? [];
      expect(String(calledUrl)).toBe("https://api.deepseek.com/v1/models");
      expect(((calledInit?.headers ?? {}) as Record<string, string>).authorization).toBe(
        "Bearer sk-test-1234",
      );

      // 模型绑定：显式下发 upstreamProviderId（null = 解绑回落 env）。
      const putRes = await app.request("/console/api/models/demo-model", {
        method: "PUT",
        headers: { "content-type": "application/json", cookie },
        body: JSON.stringify({
          displayName: "Demo",
          inputPricePerMtok: "1",
          outputPricePerMtok: "2",
          upstreamProviderId: created.data.provider.id,
        }),
      });
      expect(putRes.status).toBe(200);
      expect(
        ((await putRes.json()) as { data: { model: { upstreamProviderId: string | null } } }).data.model
          .upstreamProviderId,
      ).toBe(created.data.provider.id);

      // 未登录不能读供应商。
      expect((await app.request("/console/api/providers")).status).toBe(401);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("批量导入模型：按上游名建档并绑定供应商，默认停用且跳过已存在", async () => {
    const context = await createTestContext();
    const app = createApp(context);
    const consoleLogin = await app.request("/console/api/auth/login", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ email: "admin@mendao.chat", password: "mendao-admin" }),
    });
    const cookie = consoleLogin.headers.get("set-cookie")?.split(";")[0] ?? "";

    const provider = await context.store.createUpstreamProvider({
      name: "导入源",
      baseUrl: "https://import.test",
      protocol: "openai-chat-completions",
      apiKeyCiphertext: null,
      apiKeyHint: null,
    });
    // 演示种子已存在 claude-sonnet-4-5：导入时应被跳过（不覆盖已有配置）。
    const res = await app.request("/console/api/models/import", {
      method: "POST",
      headers: { "content-type": "application/json", cookie },
      body: JSON.stringify({
        providerId: provider.id,
        modelIds: ["claude-sonnet-4-5", "fresh-model-a", "fresh-model-a", "fresh-model-b"],
      }),
    });
    expect(res.status).toBe(200);
    const body = (await res.json()) as { data: { created: string[]; skipped: string[] } };
    expect(body.data.created.sort()).toEqual(["fresh-model-a", "fresh-model-b"]);
    expect(body.data.skipped).toEqual(["claude-sonnet-4-5"]);

    const created = await context.store.getModel("fresh-model-a");
    expect(created?.upstreamModel).toBe("fresh-model-a");
    expect(created?.upstreamProviderId).toBe(provider.id);
    expect(created?.enabled).toBe(false);
    expect(created?.inputPricePerMtok).toBe("0.000000");
    // 已存在的不被覆盖（价格保持种子原值；内存实现不做 numeric 归一）。
    const untouched = await context.store.getModel("claude-sonnet-4-5");
    expect(untouched?.inputPricePerMtok).toBe("3");
    expect(untouched?.upstreamProviderId).toBeNull();

    // 供应商不存在 404；未登录 401。
    expect(
      (
        await app.request("/console/api/models/import", {
          method: "POST",
          headers: { "content-type": "application/json", cookie },
          body: JSON.stringify({
            providerId: "ffffffff-ffff-ffff-ffff-ffffffffffff",
            modelIds: ["x"],
          }),
        })
      ).status,
    ).toBe(404);
    expect(
      (
        await app.request("/console/api/models/import", {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ providerId: provider.id, modelIds: ["x"] }),
        })
      ).status,
    ).toBe(401);
  });
});

describe("模型定价库", () => {
  it("默认种子可读；PUT 更新后导入按模型 ID 自动带价", async () => {
    const context = await createTestContext();
    const app = createApp(context);
    const consoleLogin = await app.request("/console/api/auth/login", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ email: "admin@mendao.chat", password: "mendao-admin" }),
    });
    const cookie = consoleLogin.headers.get("set-cookie")?.split(";")[0] ?? "";

    // 首次 GET 用内置默认（cc-switch 口径）初始化。
    const listRes = await app.request("/console/api/model-pricing", { headers: { cookie } });
    expect(listRes.status).toBe(200);
    const list = (await listRes.json()) as { data: { entries: Array<{ modelId: string; inputCostPerMillion: string }> } };
    expect(list.data.entries.length).toBeGreaterThan(50);
    const deepseek = list.data.entries.find((entry) => entry.modelId === "deepseek-flash");
    expect(deepseek?.inputCostPerMillion).toBe("0.150000");

    // PUT：全量替换 + 价格归一（缺省缓存字段补 0）。
    const putRes = await app.request("/console/api/model-pricing", {
      method: "PUT",
      headers: { "content-type": "application/json", cookie },
      body: JSON.stringify({
        entries: [
          {
            modelId: "auto-price-model",
            displayName: "Auto Model",
            inputCostPerMillion: "1.5",
            outputCostPerMillion: 3,
          },
        ],
      }),
    });
    expect(putRes.status).toBe(200);
    const putBody = (await putRes.json()) as { data: { entries: Array<{ cacheReadCostPerMillion: string }> } };
    expect(putBody.data.entries[0]?.cacheReadCostPerMillion).toBe("0.000000");

    // 导入命中定价库：自动带价并绑定供应商，默认停用。
    const provider = await context.store.createUpstreamProvider({
      name: "带价导入源",
      baseUrl: "https://priced.test",
      protocol: "openai-chat-completions",
      apiKeyCiphertext: null,
      apiKeyHint: null,
    });
    const importRes = await app.request("/console/api/models/import", {
      method: "POST",
      headers: { "content-type": "application/json", cookie },
      body: JSON.stringify({ providerId: provider.id, modelIds: ["auto-price-model"] }),
    });
    expect(importRes.status).toBe(200);
    const imported = ((await importRes.json()) as { data: { created: string[] } }).data.created;
    expect(imported).toEqual(["auto-price-model"]);

    const model = await context.store.getModel("auto-price-model");
    expect(model?.displayName).toBe("Auto Model");
    expect(model?.upstreamModel).toBe("auto-price-model");
    expect(model?.upstreamProviderId).toBe(provider.id);
    expect(model?.enabled).toBe(false);
    expect(model?.inputPricePerMtok).toBe("1.500000");
    expect(model?.outputPricePerMtok).toBe("3.000000");

    // 未登录 401。
    expect((await app.request("/console/api/model-pricing")).status).toBe(401);
  });
});

describe("客户端 SSE 推送", () => {
  it("事件总线：去抖窗口内多次 emit 合并为一次回调（domains 并集）", async () => {
    const bus = createConfigEventBus();
    const events: Array<Set<ConfigEventDomain>> = [];
    bus.subscribe((domains) => events.push(domains));
    bus.emit("models");
    bus.emit("models");
    bus.emit("providers");
    await new Promise((resolve) => setTimeout(resolve, 450));
    expect(events).toHaveLength(1);
    const merged = events[0];
    if (!merged) throw new Error("未收到合并事件");
    expect([...merged].sort()).toEqual(["models", "providers"]);
  });

  it("SSE 流：未登录 401；订阅后配置变更推送 config.changed（含合并 domains）", async () => {
    const context = await createTestContext();
    const app = createApp(context);
    expect((await app.request("/api/v1/client/events")).status).toBe(401);

    // 给演示成员直接签发设备凭据（等价于完成设备登录流）。
    const member = await context.store.findUserByEmail("member@mendao.chat");
    if (!member) throw new Error("缺少演示成员");
    const TOKEN = `mdp_${"d".repeat(32)}`;
    await context.store.createDeviceToken({
      id: randomId(),
      userId: member.id,
      name: "SSE 设备",
      platform: null,
      tokenHash: hashToken(TOKEN),
      tokenCiphertext: "unused",
      tokenHint: "abcd",
      createdAt: new Date().toISOString(),
      lastUsedAt: null,
      revokedAt: null,
    });

    const res = await app.request("/api/v1/client/events", {
      headers: { authorization: `Bearer ${TOKEN}` },
    });
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toContain("text/event-stream");

    const reader = (res.body as ReadableStream<Uint8Array>).getReader();
    const decoder = new TextDecoder();
    const readUntil = async (needle: string): Promise<string> => {
      let buffer = "";
      for (;;) {
        const { done, value } = await Promise.race([
          reader.read(),
          new Promise<never>((_, reject) =>
            setTimeout(() => reject(new Error("SSE 读取超时")), 5000),
          ),
        ]);
        if (done) throw new Error("流提前结束");
        buffer += decoder.decode(value, { stream: true });
        if (buffer.includes(needle)) return buffer;
      }
    };

    // 初始 hello 帧，携带断线重连建议间隔。
    const hello = await readUntil('"type":"hello"');
    expect(hello).toContain("retry: 5000");

    // 配置变更（走事件总线，与控制台 emit 同一通道）：合并 domain 后单条推送。
    context.configEvents.emit("models");
    context.configEvents.emit("providers");
    const eventText = await readUntil("event: config.changed");
    expect(eventText).toContain('"domains"');
    expect(eventText).toContain("models");
    expect(eventText).toContain("providers");
    await reader.cancel();
  });
});
