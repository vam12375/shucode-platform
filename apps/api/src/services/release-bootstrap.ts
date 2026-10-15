import { readFile } from "node:fs/promises";
import { parseBuiltinReleaseLoose } from "@shucode-platform/contracts/release";
import type { AppContext } from "../context.js";

export const DEFAULT_SEED_RELEASE_PATH = new URL("../../seeds/shucode-builtin.seed.json", import.meta.url);

/**
 * 首次启动兜底：无任何 Release 时载入仓库内置的种子配置（复制自客户端当前
 * config/provider/shucode-builtin.json）。运行期发布由 Console 管理。
 */
export async function ensureSeedRelease(context: AppContext): Promise<void> {
  const active = await context.store.getActiveRelease();
  if (active) return;
  let raw: string;
  try {
    raw = await readFile(DEFAULT_SEED_RELEASE_PATH, "utf8");
  } catch {
    context.logger.warn("未找到种子内置配置，跳过载入");
    return;
  }
  try {
    const parsed = parseBuiltinReleaseLoose(JSON.parse(raw) as unknown);
    const existing = await context.store.getRelease(parsed.revision);
    if (!existing) {
      await context.store.insertRelease({
        revision: parsed.revision,
        content: parsed.content,
        publishedBy: null,
      });
      context.logger.info("已载入种子内置配置", { revision: parsed.revision });
    }
  } catch (error) {
    context.logger.warn("种子内置配置无效，跳过载入", { error });
  }
}

interface DemoModelSeed {
  modelId: string;
  displayName: string;
  inputPricePerMtok: string;
  outputPricePerMtok: string;
  cacheReadPricePerMtok: string;
  cacheWritePricePerMtok: string;
  sortOrder: number;
}

export const DEMO_MODELS: readonly DemoModelSeed[] = [
  {
    modelId: "claude-sonnet-4-5",
    displayName: "Claude Sonnet 4.5",
    inputPricePerMtok: "3",
    outputPricePerMtok: "15",
    cacheReadPricePerMtok: "0.3",
    cacheWritePricePerMtok: "3.75",
    sortOrder: 10,
  },
  {
    modelId: "claude-haiku-4-5",
    displayName: "Claude Haiku 4.5",
    inputPricePerMtok: "1",
    outputPricePerMtok: "5",
    cacheReadPricePerMtok: "0.1",
    cacheWritePricePerMtok: "1.25",
    sortOrder: 20,
  },
];

/** 模型目录为空时写入默认两条演示模型；管理员在 Console 中调整价格与启用状态。 */
export async function ensureDefaultModels(context: AppContext): Promise<void> {
  const models = await context.store.listModels({ includeDisabled: true });
  if (models.length > 0) return;
  for (const model of DEMO_MODELS) {
    await context.store.upsertModel(model);
  }
  context.logger.info("已写入默认模型目录", { count: DEMO_MODELS.length });
}
