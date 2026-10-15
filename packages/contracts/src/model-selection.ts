import { z } from "zod";

/**
 * 与 Shucode 客户端 packages/shared/src/model-selection.ts 保持一字不差。
 * 修改必须同步两边；契约测试以本文件为平台侧唯一副本。
 */
export const modelSelectionSchema = z
  .object({
    providerId: z.string().trim().min(1),
    modelId: z.string().trim().min(1),
    options: z
      .object({
        reasoningLevel: z.string().trim().min(1).optional(),
      })
      .strict()
      .optional(),
  })
  .strict();

export type ModelSelection = z.infer<typeof modelSelectionSchema>;
