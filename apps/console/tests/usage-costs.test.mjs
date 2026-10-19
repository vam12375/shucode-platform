import assert from "node:assert/strict";
import test from "node:test";
import { summarizeHistoricalCosts } from "../src/usage-costs.ts";

function record(currency, estimatedMicro, creditsMicro = 20_000_000) {
  return {
    model: "example-model",
    creditsMicro,
    priceSnapshot: {
      schemaVersion: 1,
      capturedAt: "2026-10-18T00:00:00Z",
      sale: { currency: "CNY" },
      cost: {
        status: "estimated",
        pricingVersion: "v1",
        modelId: "example-model",
        rates: { currency },
        estimatedMicro,
      },
    },
  };
}

test("frozen cost records retain independent currencies and corresponding revenue", () => {
  const result = summarizeHistoricalCosts([
    record("CNY", 3_000_000),
    record("USD", 1_000_000, 8_000_000),
    record("CNY", 2_000_000),
  ]);
  assert.equal(result.estimatedCount, 3);
  assert.equal(result.missingCount, 0);
  assert.deepEqual(result.totals, [
    ["CNY", { count: 2, costMicro: 5_000_000, chargedMicro: 40_000_000 }],
    ["USD", { count: 1, costMicro: 1_000_000, chargedMicro: 8_000_000 }],
  ]);
  assert.equal(result.groups.length, 2);
});

test("legacy and unknown snapshots are counted as missing rather than zero cost", () => {
  const result = summarizeHistoricalCosts([
    { model: "old", creditsMicro: 10 },
    { model: "null", creditsMicro: 10, priceSnapshot: null },
    {
      ...record("CNY", 4),
      priceSnapshot: { cost: { status: "unknown", reason: "no price" } },
    },
    record("CNY", 0),
  ]);
  assert.equal(result.missingCount, 3);
  assert.equal(result.estimatedCount, 1);
  assert.equal(result.totals[0][1].costMicro, 0);
  assert.equal(result.totals[0][1].chargedMicro, 20_000_000);
});

test("invalid estimated values do not poison cost totals", () => {
  const result = summarizeHistoricalCosts([
    record("CNY", -1),
    record("CNY", NaN),
    record("", 1),
    record("CNY", undefined),
  ]);
  assert.equal(result.missingCount, 4);
  assert.deepEqual(result.totals, []);
});
