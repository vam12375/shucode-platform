import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";
import ts from "typescript";
import * as costs from "../src/usage-costs.ts";
import * as reasoning from "../src/usage-reasoning.ts";
import * as time from "../src/time.ts";
import * as ranges from "../src/usage-range.ts";

const sources = Object.fromEntries(await Promise.all(["Usage", "Audit"].map(async (name) => [name, ts.transpileModule(
  await readFile(new URL(`../src/pages/${name}.tsx`, import.meta.url), "utf8"),
  { compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 } },
).outputText])));
const usage = (id) => ({ id, requestId: id, userId: "user", teamId: "team", model: id, modelDisplayName: id,
  upstreamModel: "private-upstream", providerId: "private-provider", inputTokens: 1, outputTokens: 1,
  cacheReadTokens: 0, cacheCreationTokens: 0, creditsMicro: 0, status: "ok", createdAt: "2026-10-26T00:00:00Z" });
const audit = (id) => ({ id, action: id, targetType: "team", targetId: "team", detail: null, actorEmail: "fixture@example.test", createdAt: "2026-10-26T00:00:00Z" });
const response = (kind, id, nextCursor = null) => kind === "Usage"
  ? { records: [usage(id)], aggregates: [], nextCursor, costSummary: null }
  : { logs: [audit(id)], nextCursor };
function nodes(tree, predicate) {
  if (Array.isArray(tree)) return tree.flatMap((node) => nodes(node, predicate));
  if (!tree || typeof tree !== "object") return [];
  return [...(predicate(tree) ? [tree] : []), ...nodes(tree.props?.children, predicate)];
}
function text(tree) {
  if (Array.isArray(tree)) return tree.map(text).join("");
  if (!tree || typeof tree !== "object") return tree ?? "";
  return text(tree.props?.children);
}
async function mount(kind, loader, metadataLoader, seriesLoader) {
  const slots = [], effects = [], calls = [], seriesCalls = [];
  let cursor = 0, dirty = false;
  let auth = { teamId: "team-a", user: { id: "user", email: "fixture@example.test" }, isRoot: true, canManageTeam: true };
  const memo = (fn, deps) => {
    const i = cursor++;
    if (!(i in slots) || deps.some((dep, j) => dep !== slots[i].deps[j])) slots[i] = { deps, value: fn() };
    return slots[i].value;
  };
  const hooks = {
    useState(initial) { const i = cursor++; if (!(i in slots)) slots[i] = typeof initial === "function" ? initial() : initial;
      return [slots[i], (value) => { slots[i] = typeof value === "function" ? value(slots[i]) : value; dirty = true; }]; },
    useRef(initial) { const i = cursor++; if (!(i in slots)) slots[i] = { current: initial }; return slots[i]; },
    useMemo: memo,
    useCallback: (fn, deps) => memo(() => fn, deps),
    useEffect(effect, deps) { const i = cursor++;
      if (!(i in slots) || deps.some((dep, j) => dep !== slots[i].deps[j])) {
        slots[i]?.cleanup?.(); slots[i] = { deps };
        effects.push(() => { slots[i].cleanup = effect(); });
      }
    },
  };
  const page = {}, jsx = (type, props) => ({ type, props });
  runInNewContext(sources[kind], {
    exports: page, URLSearchParams,
    require(name) {
      if (name === "react") return hooks;
      if (name === "react/jsx-runtime") return { jsx, jsxs: jsx };
      if (name === "../auth.js") return { useAuth: () => auth };
      if (name === "../api.js") return { api: async (path) => {
        const query = new URL(path, "https://fixture.invalid");
        if (query.pathname === "/usage" || query.pathname === "/audit") {
          calls.push({ path, params: query.searchParams, teamId: auth.teamId, isRoot: auth.isRoot });
          return loader ? loader(query.searchParams, { ...auth }) : response(kind, query.searchParams.get("cursor") ? "page-two" : "page-one", query.searchParams.has("cursor") ? null : "cursor-next");
        }
        if (metadataLoader && ["/teams", "/users", "/providers", "/models", "/devices"].includes(query.pathname)) return metadataLoader(query.pathname, { ...auth });
        if (query.pathname === "/teams") return { teams: [] };
        if (query.pathname === "/users") return { users: [] };
        if (query.pathname === "/providers") return { providers: [] };
        if (query.pathname === "/models") return { models: [] };
        if (query.pathname === "/devices") return { devices: [] };
        if (query.pathname === "/usage/series") {
          seriesCalls.push(query.searchParams);
          return seriesLoader ? seriesLoader(query.searchParams) : { series: [] };
        }
        throw new Error(`Unexpected fixture API ${path}`);
      } };
      if (name === "@shucode-platform/contracts/credits") return { MICRO_CREDITS_PER_CREDIT: 1_000_000, formatCreditsDisplay: String };
      if (name === "../usage-costs.js") return costs;
      if (name === "../usage-reasoning.js") return reasoning;
      if (name === "../time.js") return time;
      if (name === "../usage-range.js") return ranges;
      if (name === "../components/UsageDateRange.js") return { UsageDateRange: "UsageDateRange" };
      if (name === "../components/UsageModel.js") return { UsageModel: "UsageModel" };
      if (name === "../components/charts.js") return { ComposedTrendChart: "chart", ModelDonutChart: "donut" };
      if (name === "../components/Feedback.js") return {};
      if (name === "../components/Gauge.js") return { formatPct: String };
      if (name === "../components/Select.js") return { Select: "Select" };
      if (name === "../components/StatusChip.js") return { Chip: "Chip", Notice: "Notice" };
      // 图标只做视觉强化：VM 测试按空组件桩渲染，不引入真实 SVG 输出。
      if (name === "lucide-react") return new Proxy({}, { get: () => () => null });
      if (name === "../components/Icon.js") return { Glyph: () => null, PageTitle: (props) => props.children };
      throw new Error(`Unexpected import ${name}`);
    },
  });
  const render = () => { cursor = 0; dirty = false; const tree = page[`${kind}Page`](); effects.splice(0).forEach((effect) => effect()); return tree; };
  const settle = async () => {
    for (let i = 0; i < 12; i++) { await new Promise(setImmediate); if (!dirty) break; render(); }
    return render();
  };
  const button = (direction) => nodes(render(), (node) => node.type === "button" && node.props["aria-label"] === `${kind === "Usage" ? "最近请求" : "审计日志"}${direction}页`)[0];
  const shown = () => kind === "Usage" ? nodes(render(), (node) => node.type === "UsageModel").map((node) => node.props.record.id)
    : nodes(render(), (node) => node.type === "tbody").flatMap((node) => nodes(node, (row) => row.type === "tr")).map((node) => text(node)).filter((value) => !value.includes("加载中"));
  render(); await settle();
  return { render, settle, calls, seriesCalls, button, shown, setAuth(patch) { auth = { ...auth, ...patch }; render(); },
    setPageSize(size) { nodes(render(), (node) => node.type === "Select" && node.props.ariaLabel?.endsWith("每页条数"))[0].props.onChange(String(size)); render(); },
    filterStatus(value) { nodes(render(), (node) => node.type === "Select" && node.props.ariaLabel === "按状态筛选")[0].props.onChange(value); render(); },
    rangeApply(preset, from, to) { nodes(render(), (node) => node.type === "UsageDateRange")[0].props.onApply(preset, from, to); render(); },
    filterModel(value) { nodes(render(), (node) => node.type === "Select" && node.props.ariaLabel === "按模型筛选")[0].props.onChange(value); render(); },
  };
}

for (const kind of ["Usage", "Audit"]) {
  test(`${kind}: previous and next replace the fixed page and reuse its keyset cursor`, async () => {
    const ui = await mount(kind);
    assert.equal(ui.calls[0].params.get("limit"), kind === "Usage" ? "100" : "50");
    assert.equal(ui.button("上一").props.disabled, true);
    ui.button("下一").props.onClick(); await ui.settle();
    assert.equal(ui.calls.at(-1).params.get("cursor"), "cursor-next");
    assert.equal(ui.shown().length, 1, "page two replaces page one instead of appending rows");
    assert.match(ui.shown().join(""), /page-two/);
    assert.match(text(ui.render()), /第 2 页/);
    assert.equal(ui.button("下一").props.disabled, true);
    ui.button("上一").props.onClick(); await ui.settle();
    assert.equal(ui.calls.at(-1).params.has("cursor"), false);
    assert.match(ui.shown().join(""), /page-one/);
    assert.match(text(ui.render()), /第 1 页/);
    assert.ok(ui.calls.every((call) => !call.params.has("offset") && !call.params.has("page")));
  });

  test(`${kind}: changing page size clears the cursor stack and starts a bounded first page`, async () => {
    const ui = await mount(kind);
    ui.button("下一").props.onClick(); await ui.settle();
    ui.setPageSize(20); await ui.settle();
    assert.equal(ui.calls.at(-1).params.get("limit"), "20");
    assert.equal(ui.calls.at(-1).params.has("cursor"), false);
    assert.equal(ui.button("上一").props.disabled, true);
    assert.match(text(ui.render()), /第 1 页/);
  });

  test(`${kind}: a late old-team page cannot replace a new permission scope or restore its cursor`, async () => {
    let resolveOld;
    const ui = await mount(kind, (params, scope) => {
      if (params.has("cursor")) return new Promise((resolve) => { resolveOld = resolve; });
      return response(kind, scope.teamId === "team-b" ? "new-scope" : "old-scope", "cursor-next");
    });
    ui.button("下一").props.onClick();
    ui.setAuth({ teamId: "team-b", isRoot: false, canManageTeam: false });
    assert.deepEqual(ui.shown(), [], "previous scope is hidden before the new response");
    await ui.settle();
    resolveOld(response(kind, "late-private-scope", "wrong-cursor")); await ui.settle();
    assert.match(ui.shown().join(""), /new-scope/);
    assert.doesNotMatch(ui.shown().join(""), /private|old-scope/);
    assert.equal(ui.calls.at(-1).params.has("cursor"), false);
    assert.equal(ui.button("上一").props.disabled, true);
    assert.match(text(ui.render()), /第 1 页/);
  });
}

test("Usage: filters reset pagination and ignore an in-flight page from the old filter", async () => {
  let resolveOld;
  const ui = await mount("Usage", (params) => {
    if (params.has("cursor")) return new Promise((resolve) => { resolveOld = resolve; });
    return response("Usage", params.get("status") === "error" ? "filtered-error" : "unfiltered", "cursor-next");
  });
  ui.button("下一").props.onClick();
  ui.filterStatus("error"); await ui.settle();
  resolveOld(response("Usage", "late-old-filter", "wrong-cursor")); await ui.settle();
  assert.deepEqual(ui.shown(), ["filtered-error"]);
  assert.equal(ui.calls.at(-1).params.get("status"), "error");
  assert.equal(ui.calls.at(-1).params.has("cursor"), false);
  assert.equal(ui.button("上一").props.disabled, true);
});


test("Usage: pending scope changes hide old model and account options without clearing a valid selected model", async () => {
  for (const patch of [{ teamId: "team-b" }, { isRoot: false, canManageTeam: true }]) {
    let pending = false, resolveUsage;
    const metadata = [];
    const metadataResult = (path, label) => {
      if (path === "/teams") return { teams: [{ id: "old-team", name: `${label}团队`, quotaCredits: "10", usedCredits: "9.5", unlimited: false }] };
      if (path === "/users") return { users: [{ id: "account", email: `${label}@fixture.test` }] };
      if (path === "/providers") return { providers: [{ id: "provider", name: `${label}供应商`, enabled: true }] };
      if (path === "/models") return { models: [{ modelId: "stable-public", displayName: `${label}目录`, enabled: true }] };
      return { devices: [] };
    };
    const ui = await mount("Usage", () => pending
      ? new Promise((resolve) => { resolveUsage = resolve; })
      : { ...response("Usage", "old-row"), modelOptions: [{ modelId: "stable-public", displayName: "旧范围模型", enabled: true }] },
    (path) => pending ? new Promise((resolve) => metadata.push({ path, resolve })) : metadataResult(path, "旧范围"));
    ui.filterModel("stable-public"); await ui.settle();
    const modelSelect = () => nodes(ui.render(), (node) => node.type === "Select" && node.props.ariaLabel === "按模型筛选")[0];
    const userSelect = () => nodes(ui.render(), (node) => node.type === "Select" && node.props.ariaLabel === "按用户筛选")[0];
    assert.ok(Array.from(modelSelect().props.options).some((option) => option.label === "旧范围模型"));
    assert.ok(Array.from(userSelect().props.options).some((option) => option.label === "旧范围@fixture.test"));
    pending = true;
    ui.setAuth(patch);
    assert.deepEqual(Array.from(modelSelect().props.options, (option) => option.value), [""], "old response options and old root catalog fallback are immediately hidden");
    assert.deepEqual(Array.from(userSelect().props.options, (option) => option.value), [""], "old account metadata is not shown while the new scope loads");
    assert.equal(nodes(ui.render(), (node) => node.type === "Notice" && node.props.k === "旧范围团队").length, 0);
    assert.equal(modelSelect().props.value, "stable-public", "the selected model remains stable while its fresh options load");
    resolveUsage({ ...response("Usage", "new-row"), ...(patch.isRoot === false ? { modelOptions: [{ modelId: "stable-public", displayName: "新范围模型", enabled: true }] } : {}) });
    for (const { path, resolve } of metadata) resolve(metadataResult(path, "新范围"));
    await ui.settle();
    assert.equal(modelSelect().props.value, "stable-public");
    assert.ok(Array.from(modelSelect().props.options).some((option) => option.label.startsWith("新范围")));
    assert.ok(Array.from(modelSelect().props.options).every((option) => !option.label.includes("旧范围")));
    assert.ok(Array.from(userSelect().props.options).every((option) => !option.label.includes("旧范围")));
    assert.equal(ui.calls.at(-1).params.get("model"), "stable-public");
  }
});

test("Usage: one-day, multi-day and rolling ranges query automatic granularity and synchronize both charts", async () => {
  const ui = await mount("Usage");
  const listCalls = ui.calls.length;
  const charts = () => nodes(ui.render(), (node) => node.type === "chart");
  ui.rangeApply("custom", "2026-10-10", "2026-10-10"); await ui.settle();
  assert.equal(ui.seriesCalls.at(-1).get("granularity"), "hour");
  assert.equal(ui.seriesCalls.at(-1).get("from"), "2026-10-09T16:00:00");
  assert.deepEqual(charts().map((node) => node.props.data.length), [24, 24]);
  ui.rangeApply("custom", "2026-10-10", "2026-10-11"); await ui.settle();
  assert.equal(ui.seriesCalls.at(-1).get("granularity"), "day");
  assert.deepEqual(charts().map((node) => node.props.data.length), [2, 2]);
  ui.rangeApply("last24h", "", ""); await ui.settle();
  const rolling = ui.seriesCalls.at(-1);
  assert.equal(rolling.get("granularity"), "hour");
  assert.equal(Date.parse(`${rolling.get("to")}Z`) - Date.parse(`${rolling.get("from")}Z`), 86_400_000);
  assert.deepEqual(charts().map((node) => node.props.data.length), [25, 25]);
  const count = ui.seriesCalls.length;
  ui.render(); ui.render(); await ui.settle();
  assert.equal(ui.seriesCalls.length, count, "ordinary renders do not advance the rolling window or refetch");
  assert.equal(ui.calls.length, listCalls, "chart ranges never modify recent-request date filters or pagination");
  ui.rangeApply("custom", "2026-02-31", "2026-03-01"); await ui.settle();
  assert.equal(ui.seriesCalls.length, count, "an invalid custom date never issues a series query");
});

test("Usage: a pending previous range cannot repopulate either chart after a new range is applied", async () => {
  let resolveOld;
  const ui = await mount("Usage", undefined, undefined, (params) => {
    if (params.get("from") === "2026-10-09T16:00:00") return new Promise((resolve) => { resolveOld = resolve; });
    return { series: [] };
  });
  const chartSizes = () => nodes(ui.render(), (node) => node.type === "chart").map((node) => node.props.data.length);
  ui.rangeApply("custom", "2026-10-10", "2026-10-10");
  assert.deepEqual(chartSizes(), [0, 0], "old chart rows disappear immediately when the active window changes");
  ui.rangeApply("custom", "2026-10-12", "2026-10-13"); await ui.settle();
  assert.deepEqual(chartSizes(), [2, 2]);
  resolveOld({ series: [] }); await ui.settle();
  assert.deepEqual(chartSizes(), [2, 2], "late old rows cannot change the window or repopulate its 24 hour buckets");
});
