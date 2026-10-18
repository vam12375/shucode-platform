import { describe, expect, it } from "vitest";
import { randomId } from "./crypto.js";
import { createMemoryStore } from "./memory.js";
import { EMPTY_PRICING_VERSION } from "./pricing-doc.js";
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

describe("内存存储：用量诊断筛选", () => {
  /** 便捷造数：仅给出与筛选相关的维度，其余取默认成功请求。 */
  async function seedUsage(store: Awaited<ReturnType<typeof createMemoryStore>>): Promise<void> {
    const team = await store.createTeam({ name: "F", quotaCredits: "100", unlimited: true });
    const otherTeam = await store.createTeam({
      name: "F2",
      quotaCredits: "100",
      unlimited: true,
    });
    const user = await store.createUser({
      email: "diag@mendao.chat",
      passwordHash: "x",
      displayName: "D",
      role: "member",
    });
    const provider = await store.createUpstreamProvider({
      name: "P1",
      baseUrl: "https://p1.test",
      protocol: "openai-chat-completions",
      apiKeyCiphertext: null,
      apiKeyHint: null,
    });
    await store.createUpstreamProvider({
      name: "P2",
      baseUrl: "https://p2.test",
      protocol: "anthropic-messages",
      apiKeyCiphertext: null,
      apiKeyHint: null,
    });
    const base = {
      id: "",
      requestId: "",
      userId: user.id,
      teamId: team.id,
      deviceTokenId: null,
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      creditsMicro: 1000,
      clientIp: null,
      latencyMs: 5,
      createdAt: new Date(Date.UTC(2026, 9, 15, 12, 0, 0)).toISOString(),
    };
    const rows: UsageRecord[] = [
      { ...base, id: randomId(), requestId: "r-ok", model: "m-a", upstreamModel: "m-a", providerId: provider.id, status: "ok", errorType: null, createdAt: new Date(Date.UTC(2026, 9, 15, 12, 0, 1)).toISOString() },
      { ...base, id: randomId(), requestId: "r-err", model: "m-a", upstreamModel: "m-a", providerId: provider.id, status: "error", errorType: "upstream_502", createdAt: new Date(Date.UTC(2026, 9, 15, 12, 0, 2)).toISOString() },
      { ...base, id: randomId(), requestId: "r-abort", model: "m-b", upstreamModel: "m-b", providerId: null, status: "aborted", errorType: null, createdAt: new Date(Date.UTC(2026, 9, 15, 12, 0, 3)).toISOString() },
      { ...base, id: randomId(), requestId: "r-other-team", model: "m-a", upstreamModel: "m-a", providerId: null, status: "error", errorType: "upstream_502", teamId: otherTeam.id, createdAt: new Date(Date.UTC(2026, 9, 15, 12, 0, 4)).toISOString() },
    ];
    for (const row of rows) await store.insertUsage(row);
  }

  it("listUsage / aggregateUsage 都支持 model、providerId、status 组合筛选", async () => {
    const store = await createMemoryStore();
    await seedUsage(store);

    // 模型筛选：不带团队约束时跨团队全部命中（按时间倒序）。
    expect((await store.listUsage({ model: "m-a" })).map((r) => r.requestId)).toEqual([
      "r-other-team",
      "r-err",
      "r-ok",
    ]);

    // 供应商筛选：命中 provider 上的 ok + error；status=status=error 再收窄。
    const provider = (await store.listUpstreamProviders())[0];
    if (!provider) throw new Error("缺少供应商");
    expect((await store.listUsage({ providerId: provider.id })).length).toBe(2);
    const errors = await store.listUsage({ providerId: provider.id, status: "error" });
    expect(errors.map((r) => r.requestId)).toEqual(["r-err"]);
    expect(errors[0]?.errorType).toBe("upstream_502");

    // 聚合同样吃筛选：m-b 只剩 aborted 记录一条请求。
    const abortedAgg = await store.aggregateUsage({ status: "aborted" });
    expect(abortedAgg).toEqual([
      expect.objectContaining({ model: "m-b", requests: 1, creditsMicro: 1000 }),
    ]);
    // 组合：team + model + status 与 list 结果一致（r-other-team 属其它团队被排除）。
    const team = (await store.listTeams())[0]?.team;
    if (!team) throw new Error("缺少团队");
    const combined = await store.listUsage({ teamId: team.id, model: "m-a", status: "error" });
    expect(combined.map((r) => r.requestId)).toEqual(["r-err"]);
    const combinedAgg = await store.aggregateUsage({ teamId: team.id, model: "m-a", status: "error" });
    expect(combinedAgg).toHaveLength(1);
    expect(combinedAgg[0]?.requests).toBe(1);
  });
});

describe("内存存储：供应商熔断与定价乐观锁", () => {
  it("reportUpstreamFailure：连败到阈值自动停用；人工停用后不再参与；成功重置计数", async () => {
    const store = await createMemoryStore();
    const provider = await store.createUpstreamProvider({
      name: "Circuit",
      baseUrl: "https://circuit.test",
      protocol: "openai-chat-completions",
      apiKeyCiphertext: null,
      apiKeyHint: null,
    });

    // 阈值 2：第一次失败计数 1 不停用；第二次触发 auto 停用。
    expect(await store.reportUpstreamFailure(provider.id, "upstream_503", 2)).toBe(false);
    const once = await store.getUpstreamProvider(provider.id);
    expect(once?.consecutiveFailures).toBe(1);
    expect(once?.enabled).toBe(true);
    expect(await store.reportUpstreamFailure(provider.id, "upstream_503", 2)).toBe(true);
    const tripped = await store.getUpstreamProvider(provider.id);
    expect(tripped?.enabled).toBe(false);
    expect(tripped?.disabledMode).toBe("auto");
    expect(tripped?.autoDisabledReason).toBe("upstream_503");
    expect(tripped?.autoDisabledAt).toBeTruthy();

    // 已停用（含人工）的供应商不再参与熔断统计。
    expect(await store.reportUpstreamFailure(provider.id, "upstream_503", 2)).toBe(false);

    // 管理员恢复：清除全部停用现场；成功转发重置计数。
    await store.updateUpstreamProvider(provider.id, { enabled: true });
    await store.reportUpstreamFailure(provider.id, "upstream_503", 99);
    await store.reportUpstreamSuccess(provider.id);
    const recovered = await store.getUpstreamProvider(provider.id);
    expect(recovered?.consecutiveFailures).toBe(0);
    expect(recovered?.enabled).toBe(true);
    expect(recovered?.disabledMode).toBeNull();

    // 人工停用后：失败不再覆盖人工停用决定。
    await store.updateUpstreamProvider(provider.id, { enabled: false });
    expect(await store.reportUpstreamFailure(provider.id, "upstream_503", 1)).toBe(false);
    const manual = await store.getUpstreamProvider(provider.id);
    expect(manual?.disabledMode).toBe("manual");
    expect(manual?.autoDisabledReason).toBeNull();
  });

  it("saveModelPricing：版本一致才写入，冲突返回 null 且库不变；版本按内容现算", async () => {
    const store = await createMemoryStore();

    // 空库版本作为首次写入前置（内容寻址，无迁移依赖）。
    const empty = await store.getModelPricing();
    expect(empty).toBeNull();
    const entries = [
      {
        modelId: "m-a",
        displayName: "A",
        inputCostPerMillion: "1.000000",
        outputCostPerMillion: "2.000000",
        cacheReadCostPerMillion: "0.000000",
        cacheCreationCostPerMillion: "0.000000",
      },
    ];
    const saved = await store.saveModelPricing(entries, EMPTY_PRICING_VERSION);
    expect(saved?.version.startsWith("prc_")).toBe(true);

    // 同内容 + 当前版本幂等；改内容后旧版本即冲突，库保持原值。
    expect(
      (await store.saveModelPricing([...entries], saved?.version ?? ""))?.version,
    ).toBe(saved?.version);
    // 用过期版本（空文档版本）改内容 → 冲突返回 null，库保持原值。
    const stale = await store.saveModelPricing(
      [
        {
          modelId: "m-a",
          displayName: "A",
          inputCostPerMillion: "1.000000",
          outputCostPerMillion: "9.000000",
          cacheReadCostPerMillion: "0.000000",
          cacheCreationCostPerMillion: "0.000000",
        },
      ],
      EMPTY_PRICING_VERSION,
    );
    expect(stale).toBeNull();
    const afterConflict = await store.getModelPricing();
    expect(afterConflict?.entries[0]?.outputCostPerMillion).toBe("2.000000");
    expect(afterConflict?.version).toBe(saved?.version);
  });
});

