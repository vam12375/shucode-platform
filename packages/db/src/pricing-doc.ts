import { createHash } from "node:crypto";

/**
 * 模型定价库文档（New API 借鉴：价格配置的内容版本 + 乐观锁，并发编辑不再静默互覆盖）。
 *
 * 版本是内容寻址的（与 user-config syncId 同思路）：version = sha256(条目的稳定序列化)，
 * 不落库、每次由内容现算，因此旧的 `{ entries }` KV 数据无需迁移即可获得版本语义。
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

export interface PricingDocSnapshot {
  entries: ModelPricingEntry[];
  version: string;
}

export function computePricingDocVersion(entries: readonly ModelPricingEntry[]): string {
  const stable = [...entries]
    .map((entry) => ({
      modelId: entry.modelId,
      displayName: entry.displayName,
      input: entry.inputCostPerMillion,
      output: entry.outputCostPerMillion,
      cr: entry.cacheReadCostPerMillion,
      cw: entry.cacheCreationCostPerMillion,
    }))
    .sort((a, b) => a.modelId.localeCompare(b.modelId));
  return `prc_${createHash("sha256").update(JSON.stringify(stable), "utf8").digest("hex").slice(0, 40)}`;
}

/** 空定价文档的版本哨兵：KV 从未写入时它就是乐观锁的前置版本。 */
export const EMPTY_PRICING_VERSION = computePricingDocVersion([]);

/** 解析 KV 里的定价文档；非对象/缺 entries 视为不存在（由调用方决定初始化默认）。 */
export function parsePricingDoc(value: unknown): PricingDocSnapshot | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const rawEntries = (value as { entries?: unknown }).entries;
  if (!Array.isArray(rawEntries)) return null;
  const entries = rawEntries.filter(
    (entry): entry is ModelPricingEntry =>
      Boolean(entry) &&
      typeof entry === "object" &&
      typeof (entry as ModelPricingEntry).modelId === "string" &&
      typeof (entry as ModelPricingEntry).displayName === "string",
  );
  return { entries, version: computePricingDocVersion(entries) };
}
