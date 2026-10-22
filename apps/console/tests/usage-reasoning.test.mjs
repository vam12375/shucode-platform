import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { createRequire } from "node:module";
import { runInNewContext } from "node:vm";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import ts from "typescript";
import * as reasoning from "../src/usage-reasoning.ts";
import * as time from "../src/time.ts";

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
const chips = compile(chipSource);
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
async function mountUsage(records) {
  const slots = [];
  const effects = [];
  let cursor = 0;
  const hooks = {
    useState(initial) {
      const index = cursor++;
      if (!(index in slots)) slots[index] = initial;
      return [slots[index], (value) => { slots[index] = typeof value === "function" ? value(slots[index]) : value; }];
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
        if (path === "/teams") return { teams: [] };
        if (path === "/users") return { users: [] };
        if (path === "/providers") return { providers: [] };
        if (path.startsWith("/models")) return { models: [] };
        if (path === "/devices") return { devices: [] };
        if (path.startsWith("/usage/series")) return { series: [] };
        if (path === "/usage") return { records, aggregates: [], nextCursor: null, costSummary: null };
        throw new Error(`Unexpected API ${path}`);
      },
    },
    "../auth.js": { useAuth: () => ({ isRoot: true, canManageTeam: true, user: { id: "user" } }) },
    "../usage-reasoning.js": reasoning,
    "../components/UsageModel.js": { UsageModel },
    "../components/charts.js": { ComposedTrendChart: "chart", ModelDonutChart: "donut" },
    "../components/Feedback.js": {},
    "../components/Gauge.js": { formatPct: String },
    "../components/Select.js": { Select: "select" },
    "../components/StatusChip.js": chips,
    "../time.js": time,
  });
  const render = () => {
    cursor = 0;
    const tree = output.UsagePage();
    effects.splice(0).forEach((effect) => effect());
    return tree;
  };
  render();
  await new Promise(setImmediate);
  return { render };
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
