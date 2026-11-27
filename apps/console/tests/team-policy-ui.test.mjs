import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { runInNewContext } from "node:vm";
import ts from "typescript";

const source = await readFile(new URL("../src/pages/Tenant.tsx", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 },
}).outputText;
const snapshot = { teamId: "a", revision: 3, dailyBudgetCredits: "5.000000", monthlyBudgetCredits: "20.000000",
  allowedModelIds: ["paid-model"], availableModels: [], budgetAlertPercent: 80, rpmLimit: 5, tpmLimit: 0, concurrencyLimit: 2 };

function mount(api) {
  const slots = [], effects = [];
  let cursor = 0, teamId = "a";
  const jsx = (type, props) => ({ type, props });
  const same = (a, b) => a?.length === b?.length && b.every((value, i) => a[i] === value);
  const react = {
    useState(initial) {
      const i = cursor++;
      if (!(i in slots)) slots[i] = initial;
      return [slots[i], (value) => { slots[i] = typeof value === "function" ? value(slots[i]) : value; }];
    },
    useCallback(fn, deps) {
      const i = cursor++;
      if (!same(slots[i]?.deps, deps)) slots[i] = { fn, deps };
      return slots[i].fn;
    },
    useEffect(fn, deps) {
      const i = cursor++;
      if (same(slots[i]?.deps, deps)) return;
      const previous = slots[i];
      slots[i] = { deps };
      effects.push(() => { previous?.cleanup?.(); slots[i].cleanup = fn(); });
    },
  };
  const output = {};
  runInNewContext(compiled, {
    exports: output, Error, require(name) {
      if (name === "react") return react;
      if (name === "react/jsx-runtime") return { jsx, jsxs: jsx };
      if (name === "../auth.js") return { useAuth: () => ({ teamId }) };
      if (name === "../api.js") return { api };
      if (name === "../components/Feedback.js") return { toastOk() {} };
      return {};
    },
  });
  const render = () => {
    cursor = 0;
    const page = output.TenantPage({ section: "policy" });
    effects.splice(0).forEach((run) => run());
    return page;
  };
  return { render, settle: async () => { await new Promise(setImmediate); return render(); }, switchTeam(id) { teamId = id; } };
}

function nodes(tree, type) {
  if (!tree || typeof tree !== "object") return [];
  if (Array.isArray(tree)) return tree.flatMap(child => nodes(child, type));
  return [...(tree.type === type ? [tree] : []), ...nodes(tree.props?.children, type)];
}
const button = (tree, label) => nodes(tree, "button").find(node => node.props.children === label);
const submit = (tree) => nodes(tree, "form")[0].props.onSubmit({ preventDefault() {} });

test("slow policy load, failed sibling lookup and stale writes preserve the team limits and draft", async () => {
  const initial = Promise.withResolvers();
  let reads = 0, remote = snapshot;
  const writes = [];
  const ui = mount(async (path, options) => {
    if (path !== "/teams/a/policy") throw new Error("other resource unavailable");
    if (!options) return reads++ === 0 ? initial.promise : remote;
    const { expectedRevision, ...changes } = JSON.parse(options.body);
    writes.push({ expectedRevision, ...changes });
    if (expectedRevision !== remote.revision) throw Object.assign(new Error("stale"), { code: 409 });
    return { ...remote, ...changes, revision: expectedRevision + 1 };
  });
  ui.render();
  assert.equal(button(await ui.settle(), "保存消费策略"), undefined);
  initial.resolve(snapshot);
  let tree = await ui.settle();
  assert.equal(button(tree, "保存消费策略").props.disabled, true);
  nodes(tree, "input").find(input => input.props.value === "5.000000").props.onChange({ target: { value: "4" } });
  remote = { ...remote, revision: 4, rpmLimit: 10 };
  submit(ui.render());
  tree = await ui.settle();
  assert.deepEqual(writes, [{ expectedRevision: 3, dailyBudgetCredits: "4" }]);
  assert.equal(button(tree, "保存消费策略").props.disabled, true);
  assert.ok(nodes(tree, "input").some(input => input.props.value === "4"));
  button(tree, "读取最新版并保留本地修改").props.onClick();
  tree = await ui.settle();
  assert.ok(nodes(tree, "input").some(input => input.props.value === 10));
  submit(tree);
  await ui.settle();
  assert.deepEqual(writes[1], { expectedRevision: 4, dailyBudgetCredits: "4" });
});

test("late policy for the previous team cannot enable saves for the selected team", async () => {
  const a = Promise.withResolvers(), b = Promise.withResolvers();
  const ui = mount(async (path) => {
    if (path === "/teams/a/policy") return a.promise;
    if (path === "/teams/b/policy") return b.promise;
    throw new Error("other resource unavailable");
  });
  ui.render();
  ui.switchTeam("b");
  ui.render();
  a.resolve(snapshot);
  assert.equal(button(await ui.settle(), "保存消费策略"), undefined);
  b.resolve({ ...snapshot, teamId: "b" });
  assert.equal(button(await ui.settle(), "保存消费策略").props.disabled, true);
});
