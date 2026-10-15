import { createHash } from "node:crypto";
import {
  providerProvisioningEnvelopeSchema,
  type ProviderProvisioningEnvelope,
} from "@shucode-platform/contracts/provisioning";
import type { PlatformModelRecord } from "@shucode-platform/db";

/** 平台网关 Provider 的固定身份；客户端 personal 层以该 providerId 注册。 */
export const PLATFORM_PROVIDER_ID = "platform:gateway";
export const PLATFORM_PROVIDER_NAME = "Mendao Platform";
/** 需匹配客户端 isProviderProvisioningAccountCredentialKey: account-provider:<providerId>:api-key。 */
export const PLATFORM_GATEWAY_CREDENTIAL_KEY = "account-provider:platform:gateway:api-key";

export interface BuildUserConfigInput {
  userId: string;
  teamId: string | null;
  deviceTokenId: string;
  gatewayBaseUrl: string;
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
      // apiKey 明文不进入配置；请求凭据经 credentials 通道下发（见 attachPlatformCredential）。
      access: { type: "api-key" },
      api: { type: "anthropic-messages", baseUrl: input.gatewayBaseUrl },
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

/** 追加平台设备 token 凭据；同一信封可被同一设备反复应用（already-applied 由客户端去重）。 */
export function attachPlatformCredential(
  envelope: ProviderProvisioningEnvelope,
  deviceToken: string,
): ProviderProvisioningEnvelope {
  return providerProvisioningEnvelopeSchema.parse({
    ...envelope,
    credentials: [
      { scope: "account-provider", key: PLATFORM_GATEWAY_CREDENTIAL_KEY, value: deviceToken },
    ],
  });
}

/** 内容寻址的 syncId：同一设备 + 同一配置产出相同值，客户端即可跳过重复应用。 */
export function computeUserConfigSyncId(input: BuildUserConfigInput): string {
  const stable = JSON.stringify({
    userId: input.userId,
    teamId: input.teamId,
    deviceTokenId: input.deviceTokenId,
    gatewayBaseUrl: input.gatewayBaseUrl,
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
