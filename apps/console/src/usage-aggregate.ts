import {
  MICRO_CREDITS_PER_CREDIT,
  formatCreditsDisplay,
  type UsageTokenCounts,
} from "@shucode-platform/contracts/credits";

export interface UsageTotals extends UsageTokenCounts {
  requests: number;
  creditsMicro: number;
  latencyMsSum: number;
}

export interface UsageAggregate extends UsageTotals {
  model: string;
  modelDisplayName?: string;
  modelKind?: "text" | "image" | "video" | "media" | "tool";
}

export function sumUsage(rows: UsageAggregate[]): UsageTotals {
  return rows.reduce<UsageTotals>((sum, row) => ({
    requests: sum.requests + row.requests,
    creditsMicro: sum.creditsMicro + row.creditsMicro,
    inputTokens: sum.inputTokens + row.inputTokens,
    outputTokens: sum.outputTokens + row.outputTokens,
    cacheReadTokens: sum.cacheReadTokens + row.cacheReadTokens,
    cacheCreationTokens: sum.cacheCreationTokens + row.cacheCreationTokens,
    latencyMsSum: sum.latencyMsSum + row.latencyMsSum,
  }), {
    requests: 0, creditsMicro: 0, inputTokens: 0, outputTokens: 0,
    cacheReadTokens: 0, cacheCreationTokens: 0, latencyMsSum: 0,
  });
}

export function promptTokens(row: UsageTokenCounts): number {
  return row.inputTokens + row.cacheReadTokens + row.cacheCreationTokens;
}

export function usageCredits(micro: number): string {
  return formatCreditsDisplay(micro / MICRO_CREDITS_PER_CREDIT);
}

/** 缓存命中率以总 prompt 为分母。无 prompt 时保留缺测状态。 */
export function cacheRate(cacheReadTokens: number, prompt: number): number | null {
  return prompt > 0 ? cacheReadTokens / prompt : null;
}
