import { z } from "zod";

/**
 * 全局 Built-in Release（内置 Provider/模型规则）。
 *
 * 客户端解码器（packages/provider-node/src/shucode-builtin-release.ts）是唯一权威校验；
 * 平台侧只做结构粗检，发布时 Console 应以一份已知可用的客户端配置为模板。
 * 完整 schema 校验失败会让客户端整份拒绝并回落旧缓存，因此发布前必须用客户端验证过。
 */
export const builtinReleaseLooseSchema = z
  .object({
    schemaVersion: z.literal(1),
    revision: z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER),
    config: z
      .object({
        providerConfigRules: z
          .object({
            templateRules: z.array(z.unknown()),
            providerRules: z.array(z.unknown()),
          })
          .passthrough(),
        modelConfigRules: z.unknown(),
      })
      .passthrough(),
  })
  .passthrough();

export interface ParsedBuiltinRelease {
  readonly revision: number;
  readonly content: unknown;
}

export function parseBuiltinReleaseLoose(input: unknown): ParsedBuiltinRelease {
  const parsed = builtinReleaseLooseSchema.safeParse(input);
  if (!parsed.success) {
    const issue = parsed.error.issues[0];
    throw new Error(
      `内置配置结构不正确：${issue ? `${issue.path.join(".") || "root"} (${issue.code})` : "unknown"}`,
    );
  }
  return { revision: parsed.data.revision, content: parsed.data };
}

export const BUILTIN_RELEASE_JSON_PATH_PREFIX = "/api/v1/releases/builtin";

export function buildBuiltinReleaseDownloadUrl(baseUrl: string, revision: number): string {
  return `${baseUrl.replace(/\/+$/u, "")}${BUILTIN_RELEASE_JSON_PATH_PREFIX}/${revision}.json`;
}
