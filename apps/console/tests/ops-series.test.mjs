import assert from "node:assert/strict";
import test from "node:test";
import { fillOpsSeries } from "../src/ops-series.ts";

test("ops fills empty five-minute buckets while preserving Shanghai API readings", () => {
  const actual = { bucketStart: "2026-11-22T19:25:00", requests: 37, errors: 3, creditsMicro: 418200, p50LatencyMs: 1320, p95LatencyMs: 7920 };
  const now = Date.parse("2026-11-22T13:47:00Z");
  const result = fillOpsSeries([actual], 6, now);
  assert.equal(result.length, 73);
  assert.equal(result[0].bucketStart, "2026-11-22T15:45:00");
  assert.equal(result.at(-1).bucketStart, "2026-11-22T21:45:00");
  assert.deepEqual(result.find((row) => row.bucketStart === actual.bucketStart), actual);
  assert.deepEqual(result.find((row) => row.bucketStart === "2026-11-22T19:30:00"), {
    bucketStart: "2026-11-22T19:30:00", requests: 0, errors: 0, creditsMicro: 0, p50LatencyMs: null, p95LatencyMs: null,
  });
});
