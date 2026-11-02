import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";
import ts from "typescript";

const source = await readFile(new URL("../src/pages/CreditPackages.tsx", import.meta.url), "utf8");
const helperSource = await readFile(new URL("../src/creditPackages.ts", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 },
}).outputText;
const helperCompiled = ts.transpileModule(helperSource, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText;
const helperExports = {};
runInNewContext(helperCompiled, { exports: helperExports, Set, Map, JSON, Number, BigInt, RegExp });
const packages = [
  { sku: "personal-lite", name: "轻量包", description: "偶尔使用", targetType: "personal", amountYuan: "29.00", creditAmount: "29.00", status: "draft", recommended: false, sortOrder: 10 },
  { sku: "personal-standard", name: "常用包", description: "日常使用", targetType: "personal", amountYuan: "99.00", creditAmount: "99.00", status: "draft", recommended: true, sortOrder: 20 },
  { sku: "personal-creator", name: "创作包", description: "持续创作", targetType: "personal", amountYuan: "299.00", creditAmount: "299.00", status: "draft", recommended: false, sortOrder: 30 },
  { sku: "team-collaboration", name: "协作包", description: "团队共享", targetType: "team", amountYuan: "999.00", creditAmount: "999.00", status: "draft", recommended: true, sortOrder: 10 },
  { sku: "team-studio", name: "工作室包", description: "多人并行", targetType: "team", amountYuan: "2999.00", creditAmount: "2999.00", status: "draft", recommended: false, sortOrder: 20 },
];

async function mount({ root = true, catalog = packages, revision = "r1" } = {}) {
  const slots = [], scheduled = [], calls = [];
  let cursor = 0;
  let current = catalog;
  let currentRevision = revision;
  let mutation = async (_path, init) => ({ revision: "r2", packages: JSON.parse(init.body).packages });
  const jsx = (type, props) => ({ type, props });
  const same = (a, b) => a?.length === b?.length && a.every((value, index) => Object.is(value, b[index]));
  const output = {};
  runInNewContext(compiled, {
    exports: output, Error, AbortController, crypto: { randomUUID: () => "22222222-2222-4222-8222-222222222222" },
    require(name) {
      if (name === "react/jsx-runtime") return { jsx, jsxs: jsx };
      if (name === "react") return {
        useState(initial) { const index = cursor++; if (!(index in slots)) slots[index] = typeof initial === "function" ? initial() : initial; return [slots[index], (value) => { slots[index] = typeof value === "function" ? value(slots[index]) : value; }]; },
        useRef(initial) { const index = cursor++; if (!(index in slots)) slots[index] = { current: initial }; return slots[index]; },
        useMemo(fn, deps) { const index = cursor++; if (!(index in slots) || !same(slots[index].deps, deps)) slots[index] = { deps, value: fn() }; return slots[index].value; },
        useCallback(fn, deps) { const index = cursor++; if (!(index in slots) || !same(slots[index].deps, deps)) slots[index] = { deps, fn }; return slots[index].fn; },
        useEffect(effect, deps) { const index = cursor++; if (!(index in slots) || !same(slots[index].deps, deps)) { slots[index] = { deps }; scheduled.push(() => { slots[index].cleanup = effect(); }); } },
      };
      if (name === "../auth.js") return { useAuth: () => ({ isRoot: root }) };
      if (name === "../api.js") return { api: async (path, init = {}) => { calls.push({ path, init }); if (init.method) return mutation(path, init); return { revision: currentRevision, packages: current }; } };
      if (name === "../components/Feedback.js") return { toastOk() {} };
      if (name === "../components/Icon.js") return { Glyph: () => null, PageTitle: (props) => props.children };
      if (name === "../components/StatusChip.js") return { Chip: "Chip", Notice: "Notice" };
      if (name === "../creditPackages.js") return helperExports;
      if (name === "lucide-react") return new Proxy({}, { get: () => () => null });
      throw new Error(`Unexpected import ${name}`);
    },
  });
  const render = () => { cursor = 0; const tree = output.CreditPackagesPage(); scheduled.splice(0).forEach((effect) => effect()); return tree; };
  const settle = async () => { await new Promise(setImmediate); return render(); };
  render(); await settle();
  return { render, settle, calls, setMutation(fn) { mutation = fn; }, setCatalog(value, nextRevision = currentRevision) { current = value; currentRevision = nextRevision; } };
}

function nodes(tree, type) { if (!tree || typeof tree !== "object") return []; if (Array.isArray(tree)) return tree.flatMap((item) => nodes(item, type)); return [...(tree.type === type ? [tree] : []), ...nodes(tree.props?.children, type)]; }
function text(tree) { if (tree === null || tree === undefined) return ""; if (Array.isArray(tree)) return tree.map(text).join(""); return typeof tree === "object" ? text(tree.props?.children) : String(tree); }
function button(tree, label) { return nodes(tree, "button").find((item) => text(item).includes(label)); }

test("non-root cannot read or render the root-only package catalog", async () => {
  const ui = await mount({ root: false });
  assert.equal(ui.calls.length, 0);
  assert.match(text(ui.render()), /仅限 root/);
});

test("root reads five draft packages and shows draft client preview without publishing", async () => {
  const ui = await mount();
  assert.equal(ui.calls.filter((call) => !call.init.method).length, 1);
  assert.equal(nodes(ui.render(), "tbody")[0].props.children.length, 5);
  assert.match(text(ui.render()), /预览草稿/);
  assert.match(text(ui.render()), /29/);
  assert.doesNotMatch(text(ui.render()), /已上架 5 档/);
});

test("editing price derives 1:1 credit and saves the catalog revision", async () => {
  const ui = await mount();
  const amountInput = nodes(ui.render(), "input").find((item) => item.props["aria-label"] === "personal-standard 售价");
  amountInput.props.onChange({ target: { value: "100.50" } });
  assert.match(text(ui.render()), /100\.50/);
  button(ui.render(), "保存目录").props.onClick();
  await ui.settle();
  const write = ui.calls.find((call) => call.init.method === "PUT");
  assert.deepEqual(JSON.parse(write.init.body).expectedRevision, "r1");
  assert.equal(JSON.parse(write.init.body).packages.find((item) => item.sku === "personal-standard").creditAmount, "100.50");
});

test("409 blocks overwrite, then refresh preserves the local draft for a new revision", async () => {
  const ui = await mount();
  const nameInput = nodes(ui.render(), "input").find((item) => item.props["aria-label"] === "personal-standard 套餐名称");
  nameInput.props.onChange({ target: { value: "本地草稿" } });
  ui.setMutation(async () => { throw Object.assign(new Error("目录已变化"), { code: 409 }); });
  button(ui.render(), "保存目录").props.onClick();
  await ui.settle();
  assert.match(text(ui.render()), /版本已变化/);
  ui.setCatalog(packages.map((item) => item.sku === "personal-standard" ? { ...item, description: "远端说明" } : item), "r2");
  button(ui.render(), "刷新并保留草稿").props.onClick();
  await ui.settle();
  assert.equal(nodes(ui.render(), "input").find((item) => item.props["aria-label"] === "personal-standard 套餐名称").props.value, "本地草稿");
  assert.equal(text(ui.render()).includes("已读取最新目录并保留本地修改"), true);
});
