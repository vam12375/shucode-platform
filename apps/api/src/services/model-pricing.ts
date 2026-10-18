import { readFile } from "node:fs/promises";
import {
  computePricingDocVersion,
  type ModelPricingEntry,
  type PlatformStore,
  type PricingDocSnapshot,
} from "@shucode-platform/db";

/**
 * 模型定价库（口径对齐 cc-switch 的 model-pricing.json / models.dev 同步）：
 * 价格为「每 1M token 的 USD 数」，与平台「每 1M token 的 credit 数」同值直填。
 *
 * 存储：platform_config KV（key=model_pricing_v1）里的可编辑副本；
 * 首次访问用内置默认资产（apps/api/src/assets/default-model-pricing.json）初始化。
 * 管理台「模型定价库」页与「模型目录」导入/添加时按 modelId 自动带价。
 *
 * 并发保护（New API 借鉴）：文档带内容版本，PUT 走存储层乐观锁，
 * 版本不符返回 409 提示刷新 —— 多管理员同时编辑不再静默互覆盖。
 */

export { MODEL_PRICING_KV_KEY } from "@shucode-platform/db";
export type { ModelPricingEntry, PricingDocSnapshot } from "@shucode-platform/db";

/** 数字/字符串价格归一成 toFixed(6) 字符串；非法或缺关键字段返回 null。 */
export function normalizePricingEntry(raw: unknown): ModelPricingEntry | null {
  if (!raw || typeof raw !== "object") return null;
  const record = raw as Record<string, unknown>;
  const modelId = typeof record.modelId === "string" ? record.modelId.trim() : "";
  const displayName = typeof record.displayName === "string" ? record.displayName.trim() : "";
  if (!modelId || !displayName) return null;
  const price = (value: unknown): string | null => {
    const numeric = typeof value === "number" ? value : Number.parseFloat(String(value ?? ""));
    if (!Number.isFinite(numeric) || numeric < 0) return null;
    return numeric.toFixed(6);
  };
  const input = price(record.inputCostPerMillion);
  const output = price(record.outputCostPerMillion);
  if (input === null || output === null) return null;
  return {
    modelId,
    displayName,
    inputCostPerMillion: input,
    outputCostPerMillion: output,
    cacheReadCostPerMillion: price(record.cacheReadCostPerMillion) ?? "0.000000",
    cacheCreationCostPerMillion: price(record.cacheCreationCostPerMillion) ?? "0.000000",
  };
}

let defaultEntries: ModelPricingEntry[] | null = null;

async function loadDefaultPricingEntries(): Promise<ModelPricingEntry[]> {
  if (!defaultEntries) {
    const raw = JSON.parse(
      await readFile(new URL("../assets/default-model-pricing.json", import.meta.url), "utf8"),
    ) as { models?: unknown[] };
    defaultEntries = (raw.models ?? [])
      .map(normalizePricingEntry)
      .filter((entry): entry is ModelPricingEntry => entry !== null);
  }
  return defaultEntries;
}

function normalizeStored(stored: ModelPricingEntry[]): ModelPricingEntry[] {
  return stored
    .map((entry) => normalizePricingEntry(entry))
    .filter((entry): entry is ModelPricingEntry => entry !== null);
}

/** 读取定价库：KV 无副本（或为空/损坏）时用内置默认初始化；返回条目与内容版本。 */
export async function loadPricingDoc(store: PlatformStore): Promise<PricingDocSnapshot> {
  const stored = await store.getModelPricing();
  if (stored) {
    const entries = normalizeStored(stored.entries);
    if (entries.length > 0) return { entries, version: computePricingDocVersion(entries) };
  }
  const seeded = await loadDefaultPricingEntries();
  // 空文档（EMPTY 版本）作为首次写入的前置；并发初始化时保存失败说明别处已写入，
  // 以库中现状为准，不覆盖。
  const saved = await store.saveModelPricing(seeded, computePricingDocVersion([]));
  const finalEntries = saved ? saved.entries : normalizeStored((await store.getModelPricing())?.entries ?? []);
  return { entries: finalEntries, version: computePricingDocVersion(finalEntries) };
}

/** 兼容旧调用方（模型导入带价）：只要条目。 */
export async function loadPricingEntries(store: PlatformStore): Promise<ModelPricingEntry[]> {
  return (await loadPricingDoc(store)).entries;
}
