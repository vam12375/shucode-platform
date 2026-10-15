import { describe, expect, it } from "vitest";
import {
  computeUsageMicroCredits,
  estimateMaxMicroCredits,
  formatCreditsDisplay,
  formatMicroCredits,
  parseCreditsToMicro,
} from "./credits.js";

describe("credits", () => {
  it("按模型单价计算 micro-credits", () => {
    const micro = computeUsageMicroCredits(
      { inputTokens: 1000, outputTokens: 500, cacheReadTokens: 0, cacheCreationTokens: 0 },
      { inputPerMtok: 3, outputPerMtok: 15, cacheReadPerMtok: 0.3, cacheWritePerMtok: 3.75 },
    );
    expect(micro).toBe(10500);
  });

  it("缓存读写按各自单价计价", () => {
    const micro = computeUsageMicroCredits(
      { inputTokens: 1_000_000, outputTokens: 0, cacheReadTokens: 1_000_000, cacheCreationTokens: 1_000_000 },
      { inputPerMtok: 3, outputPerMtok: 15, cacheReadPerMtok: 0.3, cacheWritePerMtok: 3.75 },
    );
    expect(micro).toBe(7_050_000);
  });

  it("预留估算按 max_tokens × 输出单价", () => {
    expect(estimateMaxMicroCredits(4096, 15)).toBe(61440);
    expect(estimateMaxMicroCredits(-5, 15)).toBe(0);
  });

  it("micro 与定点字符串互转", () => {
    expect(formatMicroCredits(10500)).toBe("0.010500");
    expect(parseCreditsToMicro("0.010500")).toBe(10500);
    expect(formatCreditsDisplay("1000.5")).toBe("1,000.5");
  });
});
