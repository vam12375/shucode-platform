import assert from "node:assert/strict";
import test from "node:test";
import * as costs from "../src/usage-costs.ts";
const { summarizeHistoricalCosts } = costs;

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

test("request costs retain micro precision and explicit independent currencies", () => {
  assert.equal(costs.historicalCostDisplay(record("CNY", 1, 0).priceSnapshot).text, "CNY 0.000001");
  assert.equal(costs.historicalCostDisplay(record("USD", 1_234_567, 0).priceSnapshot).text, "USD 1.234567");
  assert.equal(costs.historicalCostDisplay(record("CNY", Number.MAX_SAFE_INTEGER).priceSnapshot).text, "CNY 9,007,199,254.740991");
});

test("known zero cost, unknown cost and legacy missing snapshot stay distinct", () => {
  const zero = costs.historicalCostDisplay(record("CNY", 0).priceSnapshot);
  assert.equal(zero.kind, "estimated");
  assert.equal(zero.text, "CNY 0");
  const unknown = costs.historicalCostDisplay({ cost: { status: "unknown", reason: "model_not_configured" } });
  assert.equal(unknown.kind, "unknown");
  assert.equal(unknown.text, "成本未知");
  assert.match(unknown.reason, /未配置.*模型/);
  for (const snapshot of [null, undefined]) {
    const missing = costs.historicalCostDisplay(snapshot);
    assert.equal(missing.kind, "missing");
    assert.equal(missing.text, "无历史快照");
  }
});

test("invalid frozen costs are never displayed as zero or a valid supplier amount", () => {
  for (const snapshot of [
    record("CNY", -1).priceSnapshot,
    record("CNY", NaN).priceSnapshot,
    record("CNY", 1.5).priceSnapshot,
    record("CNY", Number.MAX_SAFE_INTEGER + 1).priceSnapshot,
    record("", 1).priceSnapshot,
    record("CNY", undefined).priceSnapshot,
  ]) {
    const display = costs.historicalCostDisplay(snapshot);
    assert.equal(display.kind, "unknown");
    assert.equal(display.text, "成本未知");
  }
});

test("supplier cost reasons explain missing data without inventing a price", () => {
  for (const [reason, expected] of [
    ["not_configured", /未配置.*成本价/],
    ["model_not_configured", /未配置.*模型/],
    ["pricing_unavailable", /成本价库.*读取失败/],
    ["usage_unavailable", /缺少.*用量/],
    ["unsupported_currency", /币种/],
    ["invalid_price", /采购价.*无效/],
    ["invalid_usage", /用量.*无效/],
    ["future_reason", /原因未记录/],
  ]) {
    assert.match(costs.historicalCostDisplay({ cost: { status: "unknown", reason } }).reason, expected);
  }
});

test("detail procurement rates use frozen strings and never current model prices", () => {
  const rates = { currency: "CNY", inputPerMtok: "0.0000003", outputPerMtok: "0", cacheReadPerMtok: "0.003", cacheWritePerMtok: "0", multiplier: "0.5" };
  assert.equal(costs.frozenCostRateText(rates, "inputPerMtok"), "CNY 0.0000003 / 百万 Token");
  assert.equal(costs.frozenCostRateText(rates, "outputPerMtok"), "CNY 0 / 百万 Token");
  assert.equal(costs.frozenCostRateText(undefined, "inputPerMtok"), "未记录");
});

test("fixed analysis fees have a readable label without classifying ordinary zero-sale requests", () => {
  assert.equal(costs.usageModelName("media:reference-analysis"), "原片分镜分析（固定费）");
  assert.equal(costs.usageModelName("deepseek-flash"), "deepseek-flash");
  assert.equal(costs.usageModelName("media:seedance"), "媒体模型");
  assert.equal(costs.usageModelName("media:private-route", "门道绘图"), "门道绘图");
});

test("web tools freeze per-request prices and never describe their costs as token usage", () => {
  const snapshot = record("CNY", 5000, 5000).priceSnapshot;
  snapshot.cost.rates = { currency: "CNY", unit: "request", perRequest: "0.005000" };
  assert.equal(costs.frozenCostRateText(snapshot.cost.rates, "perRequest"), "CNY 0.005000 / 次成功请求");
  assert.equal(costs.historicalCostDisplay(snapshot).text, "CNY 0.005");
  assert.match(costs.historicalCostDisplay(snapshot).reason, /成功请求次数/);
  assert.doesNotMatch(costs.historicalCostDisplay(snapshot).reason, /Token/);
  assert.equal(costs.usageModelName("deepseek:web-search"), "网页搜索");
  assert.equal(costs.usageModelName("deepseek:web-fetch"), "网页抓取");
});

test("media cost snapshots keep frozen image and output-second units", () => {
  for (const [unit, quantity, unitText, reason] of [
    ["image", 2, "张", /实际生成张数/],
    ["second", 10, "秒输出", /实际输出秒数/],
  ]) {
    const snapshot = record("CNY", 250_000, 500_000).priceSnapshot;
    snapshot.cost.rates = { currency: "CNY", unit, perUnit: "0.125000", quantity };
    assert.equal(costs.frozenCostRateText(snapshot.cost.rates, "perUnit"), `CNY 0.125000 / ${unitText}`);
    assert.equal(costs.historicalCostDisplay(snapshot).text, "CNY 0.25");
    assert.match(costs.historicalCostDisplay(snapshot).reason, reason);
    assert.doesNotMatch(costs.historicalCostDisplay(snapshot).reason, /Token/);
    assert.equal(costs.frozenCostRateText({ ...snapshot.cost.rates, perUnit: "" }, "perUnit"), "未记录");
    const summary = summarizeHistoricalCosts([{ model: "media:fixture", creditsMicro: 500_000, priceSnapshot: snapshot }]);
    assert.equal(summary.estimatedCount, 1);
    assert.equal(summary.totals[0][1].costMicro, 250_000);
  }
  assert.equal(costs.historicalCostDisplay(null).text, "无历史快照");
});
