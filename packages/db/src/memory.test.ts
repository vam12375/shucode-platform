import { describe, expect, it } from "vitest";
import { randomId } from "./crypto.js";
import { createMemoryStore } from "./memory.js";
import type { UsageRecord } from "./ports.js";

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
        clientIp: null,
        latencyMs: 1,
        providerId: null,
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

  it("listUsage 游标翻页：新写入不影响已翻页位置，与 Postgres 元组比较语义一致", async () => {
    const store = await createMemoryStore();
    const team = await store.createTeam({ name: "P", quotaCredits: "100", unlimited: true });
    const createdAt = (offsetSeconds: number): string =>
      new Date(Date.UTC(2026, 9, 15, 12, 0, offsetSeconds)).toISOString();
    const ids: string[] = [];
    for (let i = 0; i < 5; i += 1) {
      const record: UsageRecord = {
        id: randomId(),
        requestId: `r${i}`,
        userId: "user",
        teamId: team.id,
        deviceTokenId: null,
        model: "m",
        upstreamModel: "m",
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        creditsMicro: 0,
        status: "ok",
        errorType: null,
        clientIp: null,
        latencyMs: 1,
        providerId: null,
        createdAt: createdAt(i),
      };
      ids.push(record.id);
      await store.insertUsage(record);
    }

    // 第一页是最新两条（r4, r3）。
    const page1 = await store.listUsage({ teamId: team.id, limit: 2 });
    expect(page1.map((record) => record.id)).toEqual([ids[4], ids[3]]);
    const cursor1 = page1[1];
    if (!cursor1) throw new Error("第一页缺少第二条记录");

    // 以第一页最后一条为游标取下一页：r2, r1。
    const page2 = await store.listUsage({
      teamId: team.id,
      limit: 2,
      cursorCreatedAt: cursor1.createdAt,
      cursorId: cursor1.id,
    });
    expect(page2.map((record) => record.id)).toEqual([ids[2], ids[1]]);
    const cursor2 = page2[1];
    if (!cursor2) throw new Error("第二页缺少第二条记录");

    const page3 = await store.listUsage({
      teamId: team.id,
      limit: 2,
      cursorCreatedAt: cursor2.createdAt,
      cursorId: cursor2.id,
    });
    expect(page3.map((record) => record.id)).toEqual([ids[0]]);

    // 翻页期间新写入的记录不会让任何页重复或漏掉已有记录。
    await store.insertUsage({
      id: randomId(),
      requestId: "r-new",
      userId: "user",
      teamId: team.id,
      deviceTokenId: null,
      model: "m",
      upstreamModel: "m",
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      creditsMicro: 0,
      status: "ok",
      errorType: null,
      clientIp: null,
      latencyMs: 1,
      providerId: null,
      createdAt: createdAt(100),
    });
    const replay = await store.listUsage({
      teamId: team.id,
      limit: 2,
      cursorCreatedAt: cursor1.createdAt,
      cursorId: cursor1.id,
    });
    expect(replay.map((record) => record.id)).toEqual([ids[2], ids[1]]);
  });
});

