import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { resolveUsageChartRange, USAGE_CHART_PRESETS } from "../src/usage-range.ts";

test("today and yesterday follow Shanghai midnight and include the full final second", () => {
  const now = new Date("2026-10-25T16:05:12.345Z");
  const today = resolveUsageChartRange("today", "", "", now);
  assert.equal(today.from, "2026-10-26");
  assert.equal(today.to, "2026-10-26");
  assert.equal(today.queryFrom, "2026-10-25T16:00:00");
  assert.equal(today.queryTo, "2026-10-26T15:59:59.999999999");
  assert.equal(today.granularity, "hour");
  assert.equal(today.bucketKeys.length, 24);
  assert.equal(today.bucketKeys[0], "2026-10-26T00:00:00");
  assert.equal(today.bucketKeys.at(-1), "2026-10-26T23:00:00");

  const yesterday = resolveUsageChartRange("yesterday", "", "", now);
  assert.equal(yesterday.from, "2026-10-25");
  assert.equal(yesterday.queryFrom, "2026-10-24T16:00:00");
  assert.equal(yesterday.queryTo, "2026-10-25T15:59:59.999999999");
  assert.equal(yesterday.granularity, "hour");
  assert.equal(yesterday.bucketKeys.length, 24);
});

test("last24h uses exact rolling instants across month boundaries and keeps both partial buckets", () => {
  const range = resolveUsageChartRange("last24h", "", "", new Date("2026-10-31T16:05:12.345Z"));
  assert.equal(range.from, "2026-10-31");
  assert.equal(range.to, "2026-11-01");
  assert.equal(range.queryFrom, "2026-10-30T16:05:12.345");
  assert.equal(range.queryTo, "2026-10-31T16:05:12.345");
  assert.equal(range.granularity, "hour");
  assert.equal(range.bucketKeys.length, 25);
  assert.equal(range.bucketKeys[0], "2026-10-31T00:00:00");
  assert.equal(range.bucketKeys.at(-1), "2026-11-01T00:00:00");
  assert.equal(new Set(range.bucketKeys).size, 25);
});

test("last24h includes the hour containing an exact inclusive endpoint", () => {
  const range = resolveUsageChartRange("last24h", "", "", new Date("2026-10-26T04:00:00.000Z"));
  assert.equal(range.queryFrom, "2026-10-25T04:00:00.000");
  assert.equal(range.queryTo, "2026-10-26T04:00:00.000");
  assert.equal(range.bucketKeys.length, 25);
  assert.equal(range.bucketKeys[0], "2026-10-25T12:00:00");
  assert.equal(range.bucketKeys.at(-1), "2026-10-26T12:00:00");
});

test("custom ranges choose hours for one natural day and days for longer ranges", () => {
  const now = new Date("2026-10-26T04:00:00Z");
  const single = resolveUsageChartRange("custom", "2024-02-29", "2024-02-29", now);
  assert.equal(single.label, "2024-02-29");
  assert.equal(single.granularity, "hour");
  assert.equal(single.bucketKeys.length, 24);
  assert.equal(single.queryFrom, "2024-02-28T16:00:00");
  assert.equal(single.queryTo, "2024-02-29T15:59:59.999999999");

  const multiple = resolveUsageChartRange("custom", "2024-02-28", "2024-03-01", now);
  assert.equal(multiple.label, "2024-02-28 至 2024-03-01");
  assert.equal(multiple.granularity, "day");
  assert.deepEqual(multiple.bucketKeys, ["2024-02-28T00:00:00", "2024-02-29T00:00:00", "2024-03-01T00:00:00"]);
});

test("multi-day presets preserve inclusive day counts and month means month to date", () => {
  const now = new Date("2026-10-25T16:05:00Z");
  for (const days of [7, 14, 30]) {
    const range = resolveUsageChartRange(`${days}d`, "", "", now);
    assert.equal(range.bucketKeys.length, days);
    assert.equal(range.to, "2026-10-26");
    assert.equal(range.granularity, "day");
  }
  const month = resolveUsageChartRange("month", "", "", now);
  assert.equal(month.from, "2026-10-01");
  assert.equal(month.to, "2026-10-26");
  assert.equal(month.bucketKeys.length, 26);
  assert.equal(month.granularity, "day");

  const firstDay = resolveUsageChartRange("month", "", "", new Date("2026-10-31T16:00:00Z"));
  assert.equal(firstDay.from, "2026-11-01");
  assert.equal(firstDay.to, "2026-11-01");
  assert.equal(firstDay.granularity, "hour");
  assert.equal(firstDay.bucketKeys.length, 24);
});

test("previous month handles leap years and year transitions", () => {
  const leap = resolveUsageChartRange("lastMonth", "", "", new Date("2024-03-01T00:00:00Z"));
  assert.equal(leap.from, "2024-02-01");
  assert.equal(leap.to, "2024-02-29");
  assert.equal(leap.bucketKeys.length, 29);
  const january = resolveUsageChartRange("lastMonth", "", "", new Date("2026-01-01T00:00:00Z"));
  assert.equal(january.from, "2025-12-01");
  assert.equal(january.to, "2025-12-31");
  assert.equal(january.bucketKeys.length, 31);
});

test("invalid, missing and reversed custom dates fail instead of becoming valid ranges", () => {
  for (const [from, to] of [
    ["", "2026-10-26"],
    ["2026-10-26", ""],
    ["2026-02-30", "2026-03-01"],
    ["2026-02-29", "2026-03-01"],
    ["2026-13-01", "2026-13-01"],
    ["2026-10-00", "2026-10-01"],
    ["2026-1-01", "2026-10-01"],
    ["2026-10-27", "2026-10-26"],
    ["2026-10-26T00:00:00", "2026-10-26"],
  ]) assert.throws(() => resolveUsageChartRange("custom", from, to), RangeError);
  assert.throws(() => resolveUsageChartRange("today", "", "", new Date("invalid")), RangeError);
  assert.throws(() => resolveUsageChartRange("unknown"), RangeError);
});

test("the preset catalogue uses the public keys and exposes the rolling window", () => {
  assert.deepEqual(USAGE_CHART_PRESETS.map(({ value }) => value), ["today", "yesterday", "last24h", "7d", "14d", "30d", "month", "lastMonth"]);
});

test("custom ranges allow 3660 inclusive days and reject 3661 before bucket generation", () => {
  const from = "2016-01-01";
  const start = Date.parse(`${from}T00:00:00Z`);
  const lastDay = (days) => new Date(start + (days - 1) * 86_400_000).toISOString().slice(0, 10);
  const allowed = resolveUsageChartRange("custom", from, lastDay(3660));
  assert.equal(allowed.bucketKeys.length, 3660);
  assert.equal(allowed.bucketKeys[0], `${from}T00:00:00`);
  assert.equal(allowed.bucketKeys.at(-1), `${lastDay(3660)}T00:00:00`);
  assert.throws(() => resolveUsageChartRange("custom", from, lastDay(3661)), /最多 3660 天/);
  assert.throws(() => resolveUsageChartRange("custom", "0001-01-01", "9999-12-31"), /最多 3660 天/);
});

test("range resolution does not depend on the browser or host local timezone", () => {
  const moduleUrl = new URL("../src/usage-range.ts", import.meta.url).href;
  const source = `import { resolveUsageChartRange } from ${JSON.stringify(moduleUrl)}; console.log(JSON.stringify(resolveUsageChartRange("last24h", "", "", new Date("2026-10-25T16:05:12.345Z"))));`;
  const outputs = ["UTC", "America/Los_Angeles", "Asia/Shanghai"].map((tz) => {
    const child = spawnSync(process.execPath, ["--input-type=module", "--eval", source], {
      env: { ...process.env, TZ: tz }, encoding: "utf8",
    });
    assert.equal(child.status, 0, child.stderr);
    return JSON.parse(child.stdout);
  });
  assert.deepEqual(outputs[0], outputs[1]);
  assert.deepEqual(outputs[1], outputs[2]);
});
