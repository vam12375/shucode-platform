import { z } from "zod";

/**
 * /api/v1/client/configs 与 /n/configs 的响应信封。
 * 字段对齐 Shucode 客户端解析器：
 * - packages/shared/src/clientConfig.ts（pluginStoreOrder）
 * - packages/shared/src/helpAppConfig.ts（configs.feedbackUrl）
 * - packages/provider-node/src/shucode-builtin-download.ts（configs.builtin_provider_config_json）
 * - packages/shared/src/remoteAppConfig.ts（configs.forceUpdate.minimalVersion）
 */

export const helpConfigSchema = z
  .object({
    community_urls: z
      .object({
        "zh-CN": z.string().optional(),
        "en-US": z.string().optional(),
      })
      .partial()
      .optional(),
    feedback_url: z.string().optional(),
    feedback_use_external_form: z.boolean().optional(),
  })
  .strict()
  .nullable()
  .optional();

export type HelpConfig = z.infer<typeof helpConfigSchema>;

export const clientConfigsEnvelopeSchema = z
  .object({
    code: z.literal(0),
    data: z.object({
      configs: z
        .object({
          pluginStoreOrder: z.unknown().optional(),
          feedbackUrl: helpConfigSchema,
          builtin_provider_config_json: z.string().optional(),
          forceUpdate: z
            .object({ minimalVersion: z.string().optional() })
            .passthrough()
            .optional(),
        })
        .passthrough(),
    }),
  })
  .passthrough();

export type ClientConfigsEnvelope = z.infer<typeof clientConfigsEnvelopeSchema>;

export interface ClientConfigsEnvelopeInput {
  help?: HelpConfig;
  /** 必须是 https 绝对地址：客户端 download 边界拒绝非 https。 */
  builtinReleaseUrl?: string | null;
  forceUpdateMinimalVersion?: string | null;
  pluginStoreOrder?: unknown;
}

export function buildClientConfigsEnvelope(
  input: ClientConfigsEnvelopeInput = {},
): ClientConfigsEnvelope {
  const configs: Record<string, unknown> = {};
  if (input.help !== undefined && input.help !== null) configs.feedbackUrl = input.help;
  if (input.builtinReleaseUrl) configs.builtin_provider_config_json = input.builtinReleaseUrl;
  if (input.forceUpdateMinimalVersion) {
    configs.forceUpdate = { minimalVersion: input.forceUpdateMinimalVersion };
  }
  if (input.pluginStoreOrder !== undefined) configs.pluginStoreOrder = input.pluginStoreOrder;
  return { code: 0, data: { configs } } as ClientConfigsEnvelope;
}

/** 平台 KV 配置键（/api/v1/client/configs 的公开字段来源）。 */
export const PLATFORM_CONFIG_KEYS = {
  help: "help",
  forceUpdate: "forceUpdate",
  pluginStoreOrder: "pluginStoreOrder",
} as const;
