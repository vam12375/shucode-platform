import { USER_CONFIG_API_PATH } from "@shucode-platform/contracts/platform-auth";
import type { Hono } from "hono";
import type { AppContext } from "../context.js";
import { authenticateDevice } from "../services/sessions.js";
import {
  attachPlatformCredential,
  buildUserConfigEnvelope,
} from "../services/user-config-builder.js";

/**
 * GET /api/v1/client/user-config（Bearer 平台设备凭据）。
 *
 * 返回与客户端 Provider Provisioning 信封同构的载荷：网关 provider + 团队模型清单 +
 * 默认模型 + 设备凭据。客户端登录后立即拉取、之后按周期刷新（≈1h），
 * 应用语义（syncId 幂等 / CAS / 回滚）由客户端 Provisioning 目标负责。
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
      models,
      defaultModelId: models[0]?.modelId ?? null,
    });
    if (auth.plaintextToken) {
      envelope = attachPlatformCredential(envelope, auth.plaintextToken);
    }
    return c.json({ code: 0, data: { envelope } });
  });
}
