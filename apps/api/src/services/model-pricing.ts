import { readFile } from "node:fs/promises";
import type { PlatformStore } from "@shucode-platform/db";

/**
 * 模型定价库（口径对齐 cc-switch 的 model-pricing.json / models.dev 同步）：
 * 价格为「每 1M token 的 USD 数」，与平台「每 1M token 的 credit 数」同值直填。
 *
 * 存储：platform_config KV（key=model_pricing_v1）里的可编辑副本；
 * 首次访问用内置默认资产（apps/api/src/assets/default-model-pricing.json）初始化。
 * 管理台「模型定价库」页与「模型目录」导入/添加时按 modelId 自动带价。
 */

export const MODEL_PRICING_KV_KEY = "model_pricing_v1";

export interface ModelPricingEntry {
  modelId: string;
  displayName: string;
  inputCostPerMillion: string;
  outputCostPerMillion: string;
  cacheReadCostPerMillion: string;
  cacheCreationCostPerMillion: string;
}

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

/** 读取定价库：KV 无副本（或损坏）时用内置默认初始化。 */
export async function loadPricingEntries(store: PlatformStore): Promise<ModelPricingEntry[]> {
  const stored = await store.getConfigValue(MODEL_PRICING_KV_KEY);
  const storedEntries =
    stored && typeof stored === "object" && Array.isArray((stored as { entries?: unknown }).entries)
      ? (stored as { entries: unknown[] }).entries
      : null;
  if (storedEntries) {
    const entries = storedEntries
      .map(normalizePricingEntry)
      .filter((entry): entry is ModelPricingEntry => entry !== null);
    if (entries.length > 0) return entries;
  }
  const seeded = await loadDefaultPricingEntries();
  await store.setConfigValue(MODEL_PRICING_KV_KEY, { entries: seeded });
  return seeded;
}
