import { describe, expect, it } from "vitest";
import { createSecretBox } from "@shucode-platform/db";
import { createMemoryStore } from "@shucode-platform/db/memory";
import { createApp } from "./app.js";
import type { AppContext } from "./context.js";
import { loadPlatformEnv } from "./env.js";

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

    // 6. user-config：网关 provider + 凭据 + 团队模型
    const cfgRes = await app.request("/api/v1/client/user-config", {
      headers: { authorization: `Bearer ${deviceToken}` },
    });
    expect(cfgRes.status).toBe(200);
    const cfg = (await cfgRes.json()) as {
      data: {
        envelope: {
          syncId: string;
          personalConfig: {
            providerConfigRules: { providerRules: Array<{ providerId: string; config: { api: { baseUrl: string } } }> };
            defaultModelSelection?: { modelId: string };
          };
          credentials: Array<{ key: string; value: string }>;
        };
      };
    };
    const envelope = cfg.data.envelope;
    expect(envelope.personalConfig.providerConfigRules.providerRules[0]?.providerId).toBe(
      "platform:gateway",
    );
    expect(envelope.personalConfig.providerConfigRules.providerRules[0]?.config.api.baseUrl).toBe(
      "https://gw.mendao.chat",
    );
    expect(envelope.credentials[0]?.value).toBe(deviceToken);
    expect(envelope.personalConfig.defaultModelSelection?.modelId).toBeTruthy();

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
