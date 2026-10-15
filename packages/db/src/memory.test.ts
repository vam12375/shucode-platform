import { describe, expect, it } from "vitest";
import { createMemoryStore } from "./memory.js";

describe("内存存储：配额临界区语义", () => {
  it("预留受配额约束，结算将预留转为实扣", async () => {
    const store = await createMemoryStore();
    const team = await store.createTeam({
      name: "T",
      quotaCredits: "0.020000",
      unlimited: false,
    });

    // 两次各预留 0.015：第二次必然超限。
    expect(await store.tryReserveQuota(team.id, 15000)).toBe(true);
    expect(await store.tryReserveQuota(team.id, 15000)).toBe(false);

    const reserved = await store.getTeam(team.id);
    expect(reserved?.reservedCredits).toBe("0.015000");

    await store.settleUsage(
      {
        id: "u1",
        requestId: "r1",
        userId: "user",
        teamId: team.id,
        deviceTokenId: null,
        model: "m",
        upstreamModel: "m",
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        creditsMicro: 5000,
        status: "ok",
        errorType: null,
        latencyMs: 1,
        createdAt: new Date().toISOString(),
      },
      15000,
    );
    const settled = await store.getTeam(team.id);
    expect(settled?.reservedCredits).toBe("0.000000");
    expect(settled?.usedCredits).toBe("0.005000");

    // 释放语义：失败路径不得遗留预留。
    expect(await store.tryReserveQuota(team.id, 5000)).toBe(true);
    await store.releaseReservation(team.id, 5000);
    const released = await store.getTeam(team.id);
    expect(released?.reservedCredits).toBe("0.000000");
  });

  it("unlimited 团队不参与配额检查", async () => {
    const store = await createMemoryStore();
    const team = await store.createTeam({ name: "U", quotaCredits: "0", unlimited: true });
    expect(await store.tryReserveQuota(team.id, 999_999_999)).toBe(true);
  });

  it("禁用团队拒绝预留", async () => {
    const store = await createMemoryStore();
    const team = await store.createTeam({ name: "D", quotaCredits: "100", unlimited: false });
    await store.updateTeam(team.id, { disabledAt: new Date().toISOString() });
    expect(await store.tryReserveQuota(team.id, 1)).toBe(false);
  });
});
