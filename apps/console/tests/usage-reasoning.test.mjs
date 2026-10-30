import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { createRequire } from "node:module";
import { runInNewContext } from "node:vm";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import ts from "typescript";
import * as reasoning from "../src/usage-reasoning.ts";
import * as costs from "../src/usage-costs.ts";
import * as time from "../src/time.ts";
import * as ranges from "../src/usage-range.ts";

const require = createRequire(import.meta.url);
const chipSource = await readFile(new URL("../src/components/StatusChip.tsx", import.meta.url), "utf8");
const modelSource = await readFile(new URL("../src/components/UsageModel.tsx", import.meta.url), "utf8");
function compile(source, imports = {}) {
  const exports = {};
  runInNewContext(ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
  }).outputText, {
    exports,
    URLSearchParams,
    require(name) { return imports[name] ?? require(name); },
  });
  return exports;
}
const icons = { Glyph: () => null, PageTitle: (props) => props.children };
const chips = compile(chipSource, {
  "./Icon.js": icons,
  "lucide-react": new Proxy({}, { get: () => () => null }),
});
const { UsageModel } = compile(modelSource, {
  "../usage-reasoning.js": reasoning,
  "./StatusChip.js": chips,
});

test("request model shows its recorded effort without replacing the model identity", () => {
  const html = renderToStaticMarkup(createElement(UsageModel, {
    record: { model: "deepseek-flash", reasoningEffort: "max", reasoningBudgetTokens: null },
  }));
  assert.match(html, /deepseek-flash/);
  assert.match(html, />max<\/span>/);
  assert.match(html, /思考强度：max/);
  assert.doesNotMatch(html, />未记录<\/span>/);
});

test("request models display the root label without leaking their internal mapping", () => {
  const html = renderToStaticMarkup(createElement(UsageModel, {
    record: { model: "media:private-route-1k", modelDisplayName: "门道绘图", modelKind: "image" },
  }));
  assert.match(html, /门道绘图/);
  assert.match(html, /不适用/);
  assert.doesNotMatch(html, /private-route|title="media:/);
});

test("explicit none and zero budget remain recorded facts", () => {
  const record = { model: "example", reasoningEffort: "none", reasoningBudgetTokens: 0 };
  assert.equal(reasoning.reasoningEffortText(record), "none");
  assert.equal(reasoning.reasoningBudgetText(record), "0 tokens");
  assert.equal(reasoning.reasoningSummary(record), "none · 预算 0 tokens");
});

test("budget-only requests show their budget without inferring a named level", () => {
  const record = { model: "example", reasoningEffort: null, reasoningBudgetTokens: 8192 };
  assert.equal(reasoning.reasoningEffortText(record), "未记录");
  assert.equal(reasoning.reasoningBudgetText(record), "8,192 tokens");
  const html = renderToStaticMarkup(createElement(UsageModel, { record }));
  assert.match(html, /预算 8,192 tokens/);
  assert.doesNotMatch(html, /max|high/);
});

test("legacy model requests and media fees have distinct missing-value semantics", () => {
  for (const record of [
    { model: "deepseek-flash" },
    { model: "deepseek-flash", reasoningEffort: null, reasoningBudgetTokens: null },
  ]) {
    assert.equal(reasoning.reasoningEffortText(record), "未记录");
    assert.equal(reasoning.reasoningBudgetText(record), "未记录");
    assert.match(renderToStaticMarkup(createElement(UsageModel, { record })), /未记录/);
  }
  for (const model of ["media:reference-analysis", "media:source-import", "media:seedance"]) {
    const record = { model };
    assert.equal(reasoning.reasoningEffortText(record), "不适用");
    assert.equal(reasoning.reasoningBudgetText(record), "不适用");
    assert.match(renderToStaticMarkup(createElement(UsageModel, { record })), /不适用/);
  }
});

const usageSource = await readFile(new URL("../src/pages/Usage.tsx", import.meta.url), "utf8");
async function mountUsage(records, isRoot = true, usageOptions = {}, providerOptions = []) {
  const slots = [];
  const effects = [];
  const calls = [];
  let cursor = 0;
  const hooks = {
    useState(initial) {
      const index = cursor++;
      if (!(index in slots)) slots[index] = initial;
      return [slots[index], (value) => { slots[index] = typeof value === "function" ? value(slots[index]) : value; }];
    },
    useRef(initial) {
      const index = cursor++;
      if (!(index in slots)) slots[index] = { current: initial };
      return slots[index];
    },
    useEffect(effect) {
      const index = cursor++;
      if (!(index in slots)) { slots[index] = true; effects.push(effect); }
    },
    useMemo: (fn) => fn(),
    useCallback: (fn) => fn,
  };
  const output = compile(usageSource, {
    react: hooks,
    "@shucode-platform/contracts/credits": { MICRO_CREDITS_PER_CREDIT: 1_000_000, formatCreditsDisplay: String },
    "../api.js": {
      api: async (path) => {
        calls.push(path);
        if (path === "/teams") return { teams: [] };
        if (path === "/users") return { users: [] };
        if (path === "/providers") return { providers: providerOptions };
        if (path.startsWith("/models")) return { models: [] };
        if (path === "/devices") return { devices: [] };
        if (path.startsWith("/usage/series")) return { series: [] };
        if (path.split("?")[0] === "/usage") return { records, aggregates: [], nextCursor: null, costSummary: null, ...usageOptions };
        throw new Error(`Unexpected API ${path}`);
      },
    },
    "../auth.js": { useAuth: () => ({ isRoot, canManageTeam: isRoot, user: { id: "user" } }) },
    "../usage-costs.js": costs,
    "../usage-reasoning.js": reasoning,
    "../components/UsageModel.js": { UsageModel },
    "../components/Icon.js": icons,
    "../components/charts.js": { ComposedTrendChart: "chart", ModelDonutChart: "donut" },
    "../components/Feedback.js": {},
    "../components/Gauge.js": { formatPct: String },
    "../components/Select.js": { Select: "select" },
    "../components/StatusChip.js": chips,
    "../time.js": time,
    "../usage-range.js": ranges,
    "../components/UsageDateRange.js": { UsageDateRange: "UsageDateRange" },
  });
  const render = () => {
    cursor = 0;
    const tree = output.UsagePage();
    effects.splice(0).forEach((effect) => effect());
    return tree;
  };
  render();
  await new Promise(setImmediate);
  return { render, calls };
}

function elements(tree, type) {
  if (!tree || typeof tree !== "object") return [];
  if (Array.isArray(tree)) return tree.flatMap((node) => elements(node, type));
  return [...(type === undefined || tree.type === type ? [tree] : []), ...elements(tree.props?.children, type)];
}

test("usage list and request details consume each response's recorded effort and budget", async () => {
  const record = {
    id: "usage", requestId: "request", model: "deepseek-flash", upstreamModel: "deepseek-flash",
    reasoningEffort: "max", reasoningBudgetTokens: 8192,
    userId: "user", teamId: "team", status: "ok", providerId: null,
    inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheCreationTokens: 0,
    creditsMicro: 0, createdAt: "2026-10-22T00:00:00Z", latencyMs: null,
  };
  const ui = await mountUsage([record]);
  const tree = ui.render();
  assert.equal(elements(tree, UsageModel)[0].props.record, record);
  const button = elements(tree, "button").find((item) => item.props["aria-label"] === "查看请求详情 request");
  button.props.onClick();
  const dialog = elements(ui.render(), "div").find((item) => item.props.role === "dialog");
  const detailRows = elements(dialog, undefined).filter((item) => item.props?.k);
  assert.equal(detailRows.find((item) => item.props.k === "思考强度").props.children, "max");
  assert.equal(detailRows.find((item) => item.props.k === "思考预算").props.children, "8,192 tokens");
});

function nodeText(tree) {
  if (tree === null || tree === undefined) return "";
  if (Array.isArray(tree)) return tree.map(nodeText).join("");
  return typeof tree === "object" ? nodeText(tree.props?.children) : String(tree);
}

function costRecord(overrides = {}) {
  return {
    id: "usage-cost", requestId: "request-cost", model: "deepseek-flash", upstreamModel: "deepseek-flash",
    reasoningEffort: "high", userId: "user", teamId: "team", status: "ok", providerId: null,
    inputTokens: 100, outputTokens: 20, cacheReadTokens: 5, cacheCreationTokens: 0,
    creditsMicro: 0, createdAt: "2026-10-23T00:00:00Z", latencyMs: null,
    priceSnapshot: {
      schemaVersion: 1, capturedAt: "2026-10-22T14:06:00Z", sale: { currency: "CNY" },
      cost: {
        status: "estimated", estimatedMicro: 1, pricingVersion: "frozen-v1", modelId: "recorded-upstream-model",
        rates: { currency: "CNY", inputPerMtok: "0.15", outputPerMtok: "0.6", cacheReadPerMtok: "0.003", cacheWritePerMtok: "0", multiplier: "0.5" },
      },
    },
    ...overrides,
  };
}

test("root usage keeps zero user charges beside a nonzero frozen supplier cost and independent fixed fee", async () => {
  const ui = await mountUsage([
    costRecord(),
    costRecord({ id: "fixed", requestId: "fixed-fee", model: "media:reference-analysis", creditsMicro: 1_000_000, priceSnapshot: null }),
    costRecord({ id: "unknown", requestId: "unknown", priceSnapshot: { cost: { status: "unknown", reason: "model_not_configured" } } }),
  ]);
  const tree = ui.render();
  const headers = elements(tree, "th").map(nodeText);
  assert.ok(headers.includes("用户实扣（credit）"));
  assert.ok(headers.includes("供应商成本（估算）"));
  const requestRows = elements(tree, "tr").filter((row) => elements(row, "button").some((button) => button.props["aria-label"]?.startsWith("查看请求详情")));
  assert.equal(nodeText(elements(requestRows[0], "td")[6]), "0");
  assert.equal(nodeText(elements(requestRows[0], "td")[7]), "CNY 0.000001");
  assert.match(nodeText(requestRows[1]), /原片分镜分析（固定费）/);
  assert.equal(nodeText(elements(requestRows[1], "td")[6]), "1");
  assert.match(nodeText(requestRows[1]), /无历史快照/);
  assert.match(nodeText(requestRows[2]), /成本未知/);
  assert.match(nodeText(tree), /模型请求的用户实扣为 0/);
  assert.match(nodeText(tree), /成功后另有一笔固定分析费/);
});

test("root request detail exposes frozen procurement metadata and a future-pricing configuration link", async () => {
  const ui = await mountUsage([costRecord()]);
  elements(ui.render(), "button").find((button) => button.props["aria-label"] === "查看请求详情 request-cost").props.onClick();
  const dialog = elements(ui.render(), "div").find((node) => node.props.role === "dialog");
  const rows = new Map(elements(dialog).filter((node) => node.props?.k).map((node) => [node.props.k, nodeText(node)]));
  assert.equal(rows.get("用户实扣（credit）"), "0 credit");
  assert.equal(rows.get("供应商成本（估算）"), "CNY 0.000001");
  assert.equal(rows.get("成本价格版本"), "frozen-v1");
  assert.equal(rows.get("成本匹配模型"), "recorded-upstream-model");
  assert.equal(rows.get("成本快照时间（上海）"), "2026-10-22 22:06:00");
  assert.equal(rows.get("采购价 · 输入"), "CNY 0.15 / 百万 Token");
  assert.equal(rows.get("采购价 · 输出"), "CNY 0.6 / 百万 Token");
  assert.equal(rows.get("采购价 · 缓存读"), "CNY 0.003 / 百万 Token");
  assert.equal(rows.get("采购价 · 缓存写"), "CNY 0 / 百万 Token");
  assert.equal(rows.get("冻结采购价倍率"), "0.5");
  assert.ok(elements(dialog, "a").some((link) => link.props.href === "#/pricing"));
  assert.match(nodeText(dialog), /修改.*仅影响未来请求/);
});

test("non-root usage never renders procurement data even if an unexpected fixture contains it", async () => {
  const ui = await mountUsage([costRecord()], false);
  assert.ok(elements(ui.render(), "th").map(nodeText).includes("用户实扣（credit）"));
  assert.doesNotMatch(nodeText(ui.render()), /供应商成本|CNY 0.000001|frozen-v1/);
  elements(ui.render(), "button").find((button) => button.props["aria-label"] === "查看请求详情 request-cost").props.onClick();
  const dialog = elements(ui.render(), "div").find((node) => node.props.role === "dialog");
  assert.doesNotMatch(nodeText(dialog), /采购价|frozen-v1|recorded-upstream-model/);
  assert.equal(elements(dialog, "a").length, 0);
});

test("only root request logs distinguish suppliers and upstream aliases of one public model", async () => {
  const records = [
    costRecord({ id: "route-a", requestId: "request-a", modelDisplayName: "DeepSeek V4.1 Flash", providerId: "provider-a", upstreamModel: "private-channel-a" }),
    costRecord({ id: "route-b", requestId: "request-b", modelDisplayName: "DeepSeek V4.1 Flash", providerId: "provider-b", upstreamModel: "private-channel-b" }),
  ];
  const providers = [{ id: "provider-a", name: "采购渠道甲" }, { id: "provider-b", name: "采购渠道乙" }];
  const root = await mountUsage(records, true, {}, providers);
  const rootRows = elements(root.render(), "tr").filter((row) => elements(row, "button").some((button) => button.props["aria-label"]?.startsWith("查看请求详情")));
  assert.match(nodeText(rootRows[0]), /采购渠道甲 · private-channel-a/);
  assert.match(nodeText(rootRows[1]), /采购渠道乙 · private-channel-b/);
  assert.match(nodeText(rootRows[0]), /CNY 0.000001/);
  const member = await mountUsage(records, false, {}, providers);
  const memberTree = member.render();
  assert.ok(elements(memberTree, UsageModel).every((node) => node.props.record.modelDisplayName === "DeepSeek V4.1 Flash"));
  assert.doesNotMatch(nodeText(memberTree), /采购渠道|private-channel|供应商成本|CNY/);
  assert.ok(!member.calls.includes("/providers"));
  elements(memberTree, "button").find((button) => button.props["aria-label"] === "查看请求详情 request-a").props.onClick();
  const dialog = elements(member.render(), "div").find((node) => node.props.role === "dialog");
  assert.match(nodeText(dialog), /DeepSeek V4.1 Flash/);
  assert.doesNotMatch(nodeText(dialog), /采购渠道|private-channel|模型映射|供应商|采购价/);
});

test("usage filters and distribution labels use projected names while filter values remain opaque", async () => {
  const key = "media:opaque-group";
  const record = costRecord({ model: key, modelDisplayName: "门道绘图", modelKind: "image", upstreamModel: "", creditsMicro: 1000 });
  const aggregate = { model: key, modelDisplayName: "门道绘图", requests: 2, inputTokens: 1, outputTokens: 0, cacheReadTokens: 0, cacheCreationTokens: 0, creditsMicro: 1000, latencyMsSum: 0 };
  const ui = await mountUsage([record], false, { aggregates: [aggregate], modelOptions: [{ modelId: key, displayName: "门道绘图", enabled: true }] });
  const tree = ui.render();
  const filter = elements(tree, "select").find((node) => node.props.ariaLabel === "按模型筛选");
  assert.deepEqual(Array.from(filter.props.options, (option) => ({ ...option })), [{ value: "", label: "全部模型" }, { value: key, label: "门道绘图" }]);
  assert.match(nodeText(tree), /门道绘图/);
  const chart = elements(tree, "donut")[0];
  assert.equal(chart.props.segments[0].label, "门道绘图");
  elements(tree, "button").find((button) => button.props["aria-label"] === "查看请求详情 request-cost").props.onClick();
  const dialog = elements(ui.render(), "div").find((node) => node.props.role === "dialog");
  assert.equal(nodeText(elements(dialog, "h3")[0]), "门道绘图");
  assert.doesNotMatch(nodeText(dialog), /opaque-group/);
});

test("web tool details display request pricing without million-token procurement fields", async () => {
  const rates = { currency: "CNY", unit: "request", perRequest: "0.005000" };
  const record = costRecord({
    model: "deepseek:web-search", modelDisplayName: "网页搜索", modelKind: "tool",
    creditsMicro: 5000, inputTokens: 0, outputTokens: 0,
    priceSnapshot: {
      schemaVersion: 1, capturedAt: "2026-10-28T00:00:00Z", sale: rates,
      cost: { status: "estimated", estimatedMicro: 5000, pricingVersion: "deepseek-tools-2026-10-28", modelId: "deepseek:web-search", rates },
    },
  });
  assert.equal(reasoning.reasoningSummary(record), "high");
  assert.equal(reasoning.reasoningSummary({ model: record.model, modelKind: "tool" }), "不适用");
  const ui = await mountUsage([record]);
  elements(ui.render(), "button").find((button) => button.props["aria-label"] === "查看请求详情 request-cost").props.onClick();
  const dialog = elements(ui.render(), "div").find((node) => node.props.role === "dialog");
  const rows = new Map(elements(dialog).filter((node) => node.props?.k).map((node) => [node.props.k, nodeText(node)]));
  assert.equal(rows.get("计费单位"), "每次成功请求；失败及空结果不计费");
  assert.equal(rows.get("用户实扣（credit）"), "0.005 credit");
  assert.equal(rows.get("供应商成本（估算）"), "CNY 0.005");
  assert.equal(rows.get("采购价 · 每次成功请求"), "CNY 0.005000 / 次成功请求");
  assert.equal(rows.get("售价 · 每次成功请求"), "CNY 0.005000 / 次成功请求");
  assert.doesNotMatch(nodeText(dialog), /百万 Token|采购价 · 输入|按实际上游 Token/);
  assert.ok(!rows.has("Token（入 / 出 / 缓存读 / 缓存写）"));
});

test("root media details display frozen prices and quantities in images or output seconds", async () => {
  for (const [unit, kind, quantity, quantityText, label, perUnit, salePerUnit] of [
    ["image", "image", 2, "2 张", "每张", "0.125000", "0.250000"],
    ["second", "video", 10, "10 秒", "每秒输出", "0.025000", "0.050000"],
  ]) {
    const record = costRecord({
      model: "media:fixture-model", modelDisplayName: "媒体夹具", modelKind: kind,
      inputTokens: 0, outputTokens: 0, creditsMicro: 500_000,
      priceSnapshot: {
        schemaVersion: 1, capturedAt: "2026-10-28T00:00:00Z",
        sale: { unit, perUnit: salePerUnit, quantity, currency: "CNY" },
        cost: { status: "estimated", estimatedMicro: 250_000, pricingVersion: "frozen-media-v1", modelId: "fixture-model", rates: { unit, perUnit, quantity, currency: "CNY" } },
      },
    });
    const ui = await mountUsage([record]);
    assert.match(nodeText(ui.render()), /CNY 0.25/);
    elements(ui.render(), "button").find((button) => button.props["aria-label"] === "查看请求详情 request-cost").props.onClick();
    const dialog = elements(ui.render(), "div").find((node) => node.props.role === "dialog");
    const rows = new Map(elements(dialog).filter((node) => node.props?.k).map((node) => [node.props.k, nodeText(node)]));
    assert.equal(rows.get("计费单位"), unit === "image" ? "按张" : "按输出秒数");
    assert.equal(rows.get("计费数量"), quantityText);
    assert.equal(rows.get("供应商成本（估算）"), "CNY 0.25");
    assert.equal(rows.get(`采购价 · ${label}`), `CNY ${perUnit} / ${unit === "image" ? "张" : "秒输出"}`);
    assert.equal(rows.get(`售价 · ${label}`), `CNY ${salePerUnit} / ${unit === "image" ? "张" : "秒输出"}`);
    assert.equal(rows.get("成本价格版本"), "frozen-media-v1");
    assert.ok(elements(dialog, "a").some((link) => link.props.href === "#/media-models"));
    assert.doesNotMatch(nodeText(dialog), /百万 Token|采购价 · 输入|按实际上游 Token/);
    assert.ok(!rows.has("Token（入 / 出 / 缓存读 / 缓存写）"));

    const member = await mountUsage([record], false);
    assert.doesNotMatch(nodeText(member.render()), /供应商成本|CNY 0.25|frozen-media-v1/);
    elements(member.render(), "button").find((button) => button.props["aria-label"] === "查看请求详情 request-cost").props.onClick();
    const memberDialog = elements(member.render(), "div").find((node) => node.props.role === "dialog");
    assert.doesNotMatch(nodeText(memberDialog), /采购价|CNY|frozen-media-v1/);
    assert.equal(elements(memberDialog, "a").length, 0);
  }
});

test("historical media requests without a frozen price remain explicitly missing", async () => {
  const record = costRecord({ model: "media:old-image", modelKind: "image", priceSnapshot: null });
  const ui = await mountUsage([record]);
  elements(ui.render(), "button").find((button) => button.props["aria-label"] === "查看请求详情 request-cost").props.onClick();
  const dialog = elements(ui.render(), "div").find((node) => node.props.role === "dialog");
  const rows = new Map(elements(dialog).filter((node) => node.props?.k).map((node) => [node.props.k, nodeText(node)]));
  assert.equal(rows.get("供应商成本（估算）"), "无历史快照");
  assert.match(rows.get("成本说明"), /没有保存采购成本快照/);
  assert.equal(rows.get("成本价格版本"), "未记录");
});
