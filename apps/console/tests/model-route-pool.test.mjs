import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";
import ts from "typescript";

const compiled = ts.transpileModule(await readFile(new URL("../src/components/ModelRoutePool.tsx", import.meta.url), "utf8"), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 },
}).outputText;
const providers = [{ id: "a", name: "供应商 A", enabled: true }, { id: "b", name: "供应商 B", enabled: false }];
const existing = { id: "route-a", modelId: "model", providerId: "a", providerName: "供应商 A", providerEnabled: true, upstreamModel: "", priority: 100, weight: 1, enabled: true };
function nodes(tree, predicate) {
  if (Array.isArray(tree)) return tree.flatMap((node) => nodes(node, predicate));
  if (!tree || typeof tree !== "object") return [];
  return [...(predicate(tree) ? [tree] : []), ...nodes(tree.props?.children, predicate)];
}
function nodeText(tree) {
  if (Array.isArray(tree)) return tree.map(nodeText).join("");
  if (!tree || typeof tree !== "object") return tree ?? "";
  return nodeText(tree.props?.children);
}
function mount(options = {}) {
  const slots = [], effects = [], calls = [], errors = [];
  let cursor = 0;
  let props = { modelId: "model", upstreamModel: "real-model", providers };
  let load = options.load ?? (() => Promise.resolve({ routes: [existing] }));
  const page = {};
  const jsx = (type, props) => ({ type, props });
  runInNewContext(compiled, {
    exports: page,
    require(name) {
      if (name === "react/jsx-runtime") return { jsx, jsxs: jsx };
      if (name === "react") return {
        useState(initial) { const i = cursor++; if (!(i in slots)) slots[i] = initial; return [slots[i], (value) => { slots[i] = typeof value === "function" ? value(slots[i]) : value; }]; },
        useEffect(effect, deps) {
          const i = cursor++;
          if (!(i in slots) || deps.some((dep, index) => dep !== slots[i].deps[index])) {
            slots[i]?.cleanup?.();
            slots[i] = { deps };
            effects.push(() => { slots[i].cleanup = effect(); });
          }
        },
      };
      if (name === "../api.js") return { api: (path, init) => { calls.push({ path, init }); return init ? Promise.resolve() : load(path); } };
      if (name === "./Feedback.js") return { toastOk() {}, toastErr(value) { errors.push(value); }, confirmDialog: async () => ({ confirmed: true }) };
      if (name === "./Select.js") return { Select: "Select" };
      if (name === "./StatusChip.js") return { Chip: "Chip", Notice: "Notice" };
      // 图标只做视觉强化：VM 测试按空组件桩渲染，不引入真实 SVG 输出。
      if (name === "lucide-react") return new Proxy({}, { get: () => () => null });
      if (name === "../components/Icon.js") return { Glyph: () => null, PageTitle: (props) => props.children };
      throw new Error(`Unexpected import ${name}`);
    },
  });
  const render = () => { cursor = 0; const result = page.ModelRoutePool(props); effects.splice(0).forEach((effect) => effect()); return result; };
  render();
  return {
    render, calls, errors, exports: page,
    setModel(modelId) { props = { ...props, modelId }; },
    async settle() { await new Promise(setImmediate); return render(); },
  };
}
function input(ui, label, value) { nodes(ui.render(), (node) => node.props?.["aria-label"] === label)[0].props.onChange({ target: { value } }); }
function button(ui, label) { return nodes(ui.render(), (node) => node.type === "button" && nodeText(node) === label)[0]; }

test("route editor saves an existing mapping and strict integer weights without deleting routes", async () => {
  const ui = mount(); await ui.settle();
  input(ui, "供应商 A 上游模型", " mapped-model ");
  input(ui, "供应商 A 优先级", "0");
  input(ui, "供应商 A 权重", "3");
  button(ui, "保存路由").props.onClick();
  await ui.settle();
  const request = ui.calls.find((call) => call.init);
  assert.equal(request.path, "/routes/route-a");
  assert.equal(request.init.method, "PATCH");
  assert.deepEqual(JSON.parse(request.init.body), { upstreamModel: "mapped-model", priority: 0, weight: 3 });
  assert.match(nodeText(ui.render()), /real-model/);
});

test("route integer inputs reject fractions, exponent notation, suffixes and out-of-range values", async () => {
  const ui = mount(); await ui.settle();
  for (const value of ["", "0", "-1", "1.5", "1e2", "12a", "10001"]) {
    input(ui, "供应商 A 权重", value);
    button(ui, "保存路由").props.onClick();
  }
  assert.equal(ui.calls.filter((call) => call.init).length, 0);
  assert.equal(ui.errors.length, 7);
  assert.equal(ui.exports.routeInteger("0", 0, 1_000_000), 0);
  assert.equal(ui.exports.routeInteger("1000001", 0, 1_000_000), null);
});

test("bound suppliers are excluded from additions and configured disabled pools explain refusal", async () => {
  const ui = mount({ load: () => Promise.resolve({ routes: [{ ...existing, enabled: false, providerEnabled: false }] }) });
  await ui.settle();
  assert.match(nodeText(ui.render()), /不会回退到模型的单一供应商绑定/);
  assert.match(nodeText(ui.render()), /供应商已停用/);
  const choices = nodes(ui.render(), (node) => node.type === "Select")[0].props.options;
  assert.deepEqual(Array.from(choices, (option) => option.value), ["", "b"]);
});

test("a late route load cannot replace a newer model's result", async () => {
  let resolveFirst;
  const ui = mount({ load: (path) => path.includes("/new/")
    ? Promise.resolve({ routes: [{ ...existing, id: "new-route", providerName: "新模型供应商" }] })
    : new Promise((resolve) => { resolveFirst = resolve; }) });
  ui.setModel("new"); ui.render(); await ui.settle();
  resolveFirst({ routes: [{ ...existing, providerName: "旧模型供应商" }] });
  await ui.settle();
  assert.match(nodeText(ui.render()), /新模型供应商/);
  assert.doesNotMatch(nodeText(ui.render()), /旧模型供应商/);
});
