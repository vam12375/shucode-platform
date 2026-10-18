import { describe, expect, it } from "vitest";
import { randomId } from "./crypto.js";
import { createMemoryStore } from "./memory.js";
import type { UsageRecord } from "./ports.js";

/** 造一条用量记录：latencyMs 可为 null（未记录延迟的桶不参与平均耗时）。 */
function usageRecord(overrides: Partial<UsageRecord>): UsageRecord {
  return {
    id: randomId(),
    requestId: randomId(),
    userId: "user",
    teamId: "team",
    deviceTokenId: null,
    model: "m",
    upstreamModel: "m",
    providerId: null,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheCreationTokens: 0,
    creditsMicro: 0,
    status: "ok",
    errorType: null,
    clientIp: null,
    latencyMs: null,
    createdAt: new Date().toISOString(),
    ...overrides,
  };
}

describe("用量时间桶聚合（aggregateUsageSeries）", () => {
  it("按天分桶走上海墙钟：UTC 前一天 17:00 已属于上海次日", async () => {
    const store = await createMemoryStore();
    const team = await store.createTeam({ name: "T", quotaCredits: "0", unlimited: true });
    await store.insertUsage(
      usageRecord({
        teamId: team.id,
        // UTC 2026-10-17T17:00Z = 上海 2026-10-18 01:00 → 桶 10-18。
        createdAt: "2026-10-17T17:00:00.000Z",
        inputTokens: 100,
        cacheReadTokens: 40,
        creditsMicro: 1500,
        latencyMs: 800,
      }),
    );
    await store.insertUsage(
      usageRecord({
        teamId: team.id,
        // UTC 2026-10-18T02:00Z = 上海 10:00 → 同属桶 10-18。
        createdAt: "2026-10-18T02:00:00.000Z",
        inputTokens: 30,
        outputTokens: 70,
        creditsMicro: 500,
        latencyMs: null,
      }),
    );
    await store.insertUsage(
      usageRecord({
        teamId: team.id,
        // UTC 2026-10-16T20:00Z = 上海 2026-10-17 04:00 → 桶 10-17。
        createdAt: "2026-10-16T20:00:00.000Z",
        outputTokens: 10,
        creditsMicro: 100,
      }),
    );

    const series = await store.aggregateUsageSeries(
      { teamId: team.id },
      "day",
    );
    expect(series.map((row) => row.bucketStart)).toEqual([
      "2026-10-17T00:00:00",
      "2026-10-18T00:00:00",
    ]);
    const day18 = series[1]!;
    expect(day18.requests).toBe(2);
    expect(day18.inputTokens).toBe(130);
    expect(day18.outputTokens).toBe(70);
    expect(day18.cacheReadTokens).toBe(40);
    expect(day18.creditsMicro).toBe(2000);
    // latency null 的不累计，平均耗时 = 800 / 2。
    expect(day18.latencyMsSum).toBe(800);
  });

  it("按小时分桶给出上海墙钟小时起点，升序返回", async () => {
    const store = await createMemoryStore();
    const team = await store.createTeam({ name: "T", quotaCredits: "0", unlimited: true });
    await store.insertUsage(
      usageRecord({ teamId: team.id, createdAt: "2026-10-17T17:10:00.000Z", creditsMicro: 1 }),
    );
    await store.insertUsage(
      usageRecord({ teamId: team.id, createdAt: "2026-10-18T02:30:00.000Z", creditsMicro: 2 }),
    );

    const series = await store.aggregateUsageSeries(
      { teamId: team.id },
      "hour",
    );
    expect(series.map((row) => row.bucketStart)).toEqual([
      "2026-10-18T01:00:00",
      "2026-10-18T10:00:00",
    ]);
    expect(series.map((row) => row.creditsMicro)).toEqual([1, 2]);
  });

  it("aggregateUsage 同步携带 latencyMsSum（null 不计入），供平均耗时卡使用", async () => {
    const store = await createMemoryStore();
    const team = await store.createTeam({ name: "T", quotaCredits: "0", unlimited: true });
    await store.insertUsage(
      usageRecord({ teamId: team.id, creditsMicro: 1, latencyMs: 150 }),
    );
    await store.insertUsage(
      usageRecord({ teamId: team.id, creditsMicro: 1, latencyMs: null }),
    );

    const rows = await store.aggregateUsage({ teamId: team.id });
    expect(rows).toHaveLength(1);
    expect(rows[0]!.requests).toBe(2);
    expect(rows[0]!.latencyMsSum).toBe(150);
  });
});
