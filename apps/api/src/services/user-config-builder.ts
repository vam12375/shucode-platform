import { createHash } from "node:crypto";
import {
  providerProvisioningEnvelopeSchema,
  type ProviderProvisioningEnvelope,
} from "@shucode-platform/contracts/provisioning";
import type { PlatformModelRecord } from "@shucode-platform/db";

/** 平台网关 Provider 的固定身份；客户端 personal 层以该 providerId 注册。 */
export const PLATFORM_PROVIDER_ID = "platform:gateway";
export const PLATFORM_PROVIDER_NAME = "Mendao Platform";

export interface BuildUserConfigInput {
  userId: string;
  teamId: string | null;
  deviceTokenId: string;
  gatewayBaseUrl: string;
  /** 下发给客户端的网关 API 类型与 baseUrl（openai 类型 baseUrl 带 /v1 前缀）。 */
  gatewayApi: { type: "openai-chat-completions" | "anthropic-messages"; baseUrl: string };
  models: ReadonlyArray<
    Pick<
      PlatformModelRecord,
      | "modelId"
      | "inputPricePerMtok"
      | "outputPricePerMtok"
      | "cacheReadPricePerMtok"
      | "cacheWritePricePerMtok"
    >
  >;
  defaultModelId: string | null;
  providerName?: string;
}

/**
 * 构造用户级配置信封。载荷与客户端 Provider Provisioning 信封同构：
 * 客户端按同一 schema 校验后写入 personal 层（syncId 幂等 + CAS + 回滚）。
 * 出站前必须通过本 schema，避免发出客户端会整份拒绝的配置。
 */
export function buildUserConfigEnvelope(
  input: BuildUserConfigInput,
): ProviderProvisioningEnvelope {
  const modelIds = input.models.map((model) => model.modelId);
  const providerRule = {
    providerId: PLATFORM_PROVIDER_ID,
    providerName: input.providerName ?? PLATFORM_PROVIDER_NAME,
    enabled: true,
    config: {
      group: "standard-personal",
      // apiKey 由 withPlatformAccessToken 在构造时填入（设备 token）。
      // 客户端 Registry 完整校验要求 Provider access 完整（缺 apiKey 时 Provider 会被整份丢弃），
      // 这与客户端 UI 创建 personal provider 时把 key 存在 config.access 的行为一致。
      access: { type: "api-key" },
      api: { type: input.gatewayApi.type, baseUrl: input.gatewayApi.baseUrl },
      personalModelIds: modelIds,
      modelOrder: modelIds,
      visibility: "visible",
    },
  };
  const providerModelRules = input.models.map((model) => ({
    providerId: PLATFORM_PROVIDER_ID,
    modelId: model.modelId,
    config: { enabled: true },
  }));
  const envelope = {
    schemaVersion: 1 as const,
    syncId: computeUserConfigSyncId(input),
    personalConfig: {
      providerConfigRules: { providerRules: [providerRule] },
      modelConfigRules: {
        providerModelRules,
        manualProviderModelRules: [] as unknown[],
      },
      providerOrder: [PLATFORM_PROVIDER_ID],
      ...(input.defaultModelId
        ? {
            defaultModelSelection: {
              providerId: PLATFORM_PROVIDER_ID,
              modelId: input.defaultModelId,
            },
          }
        : {}),
    },
    accountSettings: {
      providerFamilyDomain: null,
      providerFamilyConnectionSelections: {},
    },
    credentials: [] as Array<{ scope: "account-provider"; key: string; value: string }>,
  };
  return providerProvisioningEnvelopeSchema.parse(envelope);
}

/**
 * 把设备 token 写入网关 Provider 的 config.access.apiKey。
 *
 * 客户端 Registry 完整校验只认 config.access.apiKey：credentials 通道的
 * `account-provider:*:api-key` 仅用于 Account Provider，普通 personal provider 不消费它，
 * 不在此处填入会让 Provider 被整份丢弃（provider-not-found → 信封回滚）。
 * 同一信封可被同一设备反复应用（already-applied 由客户端去重）。
 */
export function withPlatformAccessToken(
  envelope: ProviderProvisioningEnvelope,
  deviceToken: string,
): ProviderProvisioningEnvelope {
  const providerRules = envelope.personalConfig.providerConfigRules.providerRules.map((rule) => {
    const typed = rule as { providerId?: unknown; config?: { access?: Record<string, unknown> } };
    if (typed.providerId !== PLATFORM_PROVIDER_ID || !typed.config?.access) {
      return rule;
    }
    return {
      ...(rule as object),
      config: {
        ...typed.config,
        access: { ...typed.config.access, apiKey: deviceToken },
      },
    };
  });
  return providerProvisioningEnvelopeSchema.parse({
    ...envelope,
    personalConfig: {
      ...envelope.personalConfig,
      providerConfigRules: { providerRules },
    },
  });
}

/** 内容寻址的 syncId：同一设备 + 同一配置产出相同值，客户端即可跳过重复应用。 */
export function computeUserConfigSyncId(input: BuildUserConfigInput): string {
  const stable = JSON.stringify({
    userId: input.userId,
    teamId: input.teamId,
    deviceTokenId: input.deviceTokenId,
    gatewayApi: input.gatewayApi,
    defaultModelId: input.defaultModelId,
    models: [...input.models]
      .map((model) => ({
        id: model.modelId,
        in: model.inputPricePerMtok,
        out: model.outputPricePerMtok,
        cr: model.cacheReadPricePerMtok,
        cw: model.cacheWritePricePerMtok,
      }))
      .sort((a, b) => a.id.localeCompare(b.id)),
  });
  return `ucfg_${createHash("sha256").update(stable, "utf8").digest("hex").slice(0, 40)}`;
}
