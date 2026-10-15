/**
 * 信用点计算。内部一律使用整数 micro-credits（1 credit = 1_000_000 micro）。
 *
 * 价格按「每 1M token 的 credit 数」表示（与仓库现有模型定价口径一致）：
 *   micro = Σ tokens_i × price_i
 * 即 price 以 1M token 计价、micro-credit 以 1e-6 credit 计价，两者恰好约掉 1e6 因子。
 */

export interface ModelPriceRates {
  readonly inputPerMtok: number;
  readonly outputPerMtok: number;
  readonly cacheReadPerMtok: number;
  readonly cacheWritePerMtok: number;
}

export interface UsageTokenCounts {
  readonly inputTokens: number;
  readonly outputTokens: number;
  readonly cacheReadTokens: number;
  readonly cacheCreationTokens: number;
}

export const MICRO_CREDITS_PER_CREDIT = 1_000_000;

export function computeUsageMicroCredits(
  usage: UsageTokenCounts,
  rates: ModelPriceRates,
): number {
  const raw =
    usage.inputTokens * rates.inputPerMtok +
    usage.outputTokens * rates.outputPerMtok +
    usage.cacheReadTokens * rates.cacheReadPerMtok +
    usage.cacheCreationTokens * rates.cacheWritePerMtok;
  if (!Number.isFinite(raw) || raw < 0) {
    throw new Error(`非法用量计价输入: ${raw}`);
  }
  return Math.round(raw);
}

/** 请求准入时的预留额：按请求声明的 max_tokens 全额预留输出侧成本。 */
export function estimateMaxMicroCredits(maxOutputTokens: number, outputPerMtok: number): number {
  const raw = Math.max(0, Math.floor(maxOutputTokens)) * outputPerMtok;
  return Math.round(raw);
}

/** micro-credits → 定点 credit 字符串（numeric(18,6) 口径）。 */
export function formatMicroCredits(micro: number): string {
  return (micro / MICRO_CREDITS_PER_CREDIT).toFixed(6);
}

export function parseCreditsToMicro(value: string | number): number {
  const numeric = typeof value === "number" ? value : Number.parseFloat(value);
  if (!Number.isFinite(numeric)) {
    throw new Error(`非法 credit 数值: ${String(value)}`);
  }
  return Math.round(numeric * MICRO_CREDITS_PER_CREDIT);
}

export function formatCreditsDisplay(value: string | number): string {
  const numeric = typeof value === "number" ? value : Number.parseFloat(value);
  if (!Number.isFinite(numeric)) return "0";
  return numeric.toLocaleString("en-US", { maximumFractionDigits: 4 });
}
