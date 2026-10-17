import { createHash } from "node:crypto";
import { USER_CONFIG_API_PATH } from "@shucode-platform/contracts/platform-auth";
import type { Hono } from "hono";
import type { AppContext } from "../context.js";
import { authenticateDevice } from "../services/sessions.js";
import {
  buildUserConfigEnvelope,
  withPlatformAccessToken,
} from "../services/user-config-builder.js";

/**
 * GET /api/v1/client/user-config（Bearer 平台设备凭据）。
 *
 * 返回与客户端 Provider Provisioning 信封同构的载荷：网关 provider（含设备 token 的
 * config.access.apiKey）+ 团队模型清单 + 偏好默认模型。客户端登录后立即拉取、
 * 之后按周期刷新（≈1h），应用语义（syncId 幂等 / CAS / 回滚）由客户端 Provisioning 目标负责；
 * 偏好默认模型由客户端依据 Registry 档位事实补全后写入。
 */
export function registerUserConfigRoutes(app: Hono, context: AppContext): void {
  app.get(USER_CONFIG_API_PATH, async (c) => {
    const auth = await authenticateDevice(context, c);
    if (!auth) {
      return c.json({ code: 401, msg: "凭据无效或已吊销" }, 401);
    }
    const teams = await context.store.listTeamsForUser(auth.user.id);
    const primary = teams.find((entry) => !entry.team.disabledAt) ?? null;
    // 无团队即无额度归属：不下发模型，网关侧也会拒绝（避免展示必然失败的模型）。
    const models = primary ? await context.store.listModels() : [];
    let envelope = buildUserConfigEnvelope({
      userId: auth.user.id,
      teamId: primary?.team.id ?? null,
      deviceTokenId: auth.device.id,
      gatewayBaseUrl: context.env.gatewayBaseUrl,
      gatewayApi: { type: context.env.gatewayApiType, baseUrl: context.env.gatewayApiBaseUrl },
      models,
      defaultModelId: models[0]?.modelId ?? null,
    });
    if (auth.plaintextToken) {
      envelope = withPlatformAccessToken(envelope, auth.plaintextToken);
    }
    const body = JSON.stringify({ code: 0, data: { envelope } });
    // 协商缓存（ETag）：客户端按周期轮询本端点时，配置未变化即返回 304 空响应，
    // 让「更短的轮询间隔」在带宽与写放大上都可承受（实时性的服务端托底部分）。
    const etag = `"${createHash("sha256").update(body).digest("base64url").slice(0, 27)}"`;
    const ifNoneMatch = c.req.header("if-none-match");
    if (ifNoneMatch?.split(/,\s*/u).includes(etag)) {
      return new Response(null, {
        status: 304,
        headers: { etag, "cache-control": "private, no-cache" },
      });
    }
    return new Response(body, {
      status: 200,
      headers: {
        "content-type": "application/json; charset=utf-8",
        etag,
        "cache-control": "private, no-cache",
      },
    });
  });
}
