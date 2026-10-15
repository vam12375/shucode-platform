import { z } from "zod";
import { modelSelectionSchema } from "./model-selection.js";

/**
 * 与 Shucode 客户端 packages/shared/src/provider-provisioning.ts 及
 * packages/shared/src/provider-family-connection-selection.ts 保持同构。
 *
 * 客户端 Provider Provisioning 目标按此信封校验并写入 personal 层；
 * 平台下发的用户级配置（/api/v1/client/user-config）与 SSH/WSL 远端同步共用同一 schema，
 * 任何字段变化必须两边同步。
 */

const nonEmptyString = z.string().trim().min(1);

export const providerFamilyConnectionSelectionSchema = z.discriminatedUnion("kind", [
  z.object({ kind: z.literal("start-plan") }).strict(),
  z.object({ kind: z.literal("individual-coding-plan") }).strict(),
  z
    .object({
      kind: z.literal("team-coding-plan"),
      productId: nonEmptyString,
      organizationId: nonEmptyString,
      projectId: nonEmptyString,
    })
    .strict(),
]);

export const providerFamilyConnectionSelectionSettingsSchema = z
  .object({
    zai: providerFamilyConnectionSelectionSchema.optional(),
    bigmodel: providerFamilyConnectionSelectionSchema.optional(),
  })
  .partial();

/** Personal Config 的 Envelope；具体字段由客户端 @shucode/provider 在目标 Environment 再校验。 */
export const providerProvisioningPersonalConfigSchema = z
  .object({
    providerConfigRules: z.object({ providerRules: z.array(z.unknown()) }).strict(),
    modelConfigRules: z
      .object({
        providerModelRules: z.array(z.unknown()),
        manualProviderModelRules: z.array(z.unknown()),
      })
      .strict(),
    providerOrder: z.array(nonEmptyString).optional(),
    defaultModelSelection: modelSelectionSchema.optional(),
  })
  .strict();

export type ProviderProvisioningPersonalConfig = z.infer<
  typeof providerProvisioningPersonalConfigSchema
>;

export const providerProvisioningAccountSettingsSchema = z
  .object({
    providerFamilyDomain: z.enum(["zai", "bigmodel"]).nullable(),
    providerFamilyConnectionSelections: providerFamilyConnectionSelectionSettingsSchema,
  })
  .strict();

export type ProviderProvisioningAccountSettings = z.infer<
  typeof providerProvisioningAccountSettingsSchema
>;

export const providerProvisioningCredentialScopeSchema = z.enum([
  "oauth-session",
  "account-provider",
]);

export const providerProvisioningCredentialEntrySchema = z
  .object({
    scope: providerProvisioningCredentialScopeSchema,
    key: nonEmptyString,
    value: z.string(),
  })
  .strict();

export type ProviderProvisioningCredentialEntry = z.infer<
  typeof providerProvisioningCredentialEntrySchema
>;

/** 只允许同步 Account Provider 的请求期 API key，不同步账号身份或未来其它扩展字段。 */
export function isProviderProvisioningAccountCredentialKey(key: string): boolean {
  const normalized = key.trim();
  return normalized === key && /^account-provider:.+:api-key$/.test(normalized);
}

export const providerProvisioningEnvelopeSchema = z
  .object({
    schemaVersion: z.literal(1),
    syncId: nonEmptyString,
    personalConfig: providerProvisioningPersonalConfigSchema,
    accountSettings: providerProvisioningAccountSettingsSchema,
    credentials: z.array(providerProvisioningCredentialEntrySchema).max(256),
  })
  .strict();

export type ProviderProvisioningEnvelope = z.infer<typeof providerProvisioningEnvelopeSchema>;

export const providerProvisioningResultSchema = z
  .object({
    syncId: nonEmptyString,
    status: z.enum(["applied", "already-applied", "unsupported", "failed", "rollback_failed"]),
    personalProviderCount: z.number().int().nonnegative(),
    credentialCount: z.number().int().nonnegative(),
    configRevision: nonEmptyString.optional(),
    errorMessage: z.string().optional(),
    rolledBack: z.boolean(),
  })
  .strict();

export type ProviderProvisioningResult = z.infer<typeof providerProvisioningResultSchema>;
