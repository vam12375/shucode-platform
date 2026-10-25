import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";
import ts from "typescript";

const source = await readFile(new URL("../src/pages/ModelDisplay.tsx", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 },
}).outputText;
const entries = [
  { id: "text:example", kind: "text", displayName: "门道对话", modelIds: ["private-text"], resolutions: [], expectedVersion: "text-v1" },
  { id: "media-group-example", kind: "image", displayName: "门道绘图", modelIds: ["private-1k", "private-2k", "private-4k"], resolutions: ["1K", "2K", "4K"], expectedVersion: "catalog-v1" },
];
async function mount(isRoot = true, options = {}) {
  const slots = [], effects = [], calls = [];
  let cursor = 0, currentEntries = entries;
  let mutation = async (_path, init) => {
    const body = JSON.parse(init.body);
    return { entry: { ...entries.find((entry) => entry.id === body.id), displayName: body.displayName, expectedVersion: "saved-v2" } };
  };
  const jsx = (type, props) => ({ type, props });
  const output = {};
  runInNewContext(compiled, {
    exports: output, Error,
    require(name) {
      if (name === "react/jsx-runtime") return { jsx, jsxs: jsx };
      if (name === "react") return {
        useState(initial) { const i = cursor++; if (!(i in slots)) slots[i] = typeof initial === "function" ? initial() : initial; return [slots[i], (v) => { slots[i] = typeof v === "function" ? v(slots[i]) : v; }]; },
        useRef(initial) { const i = cursor++; if (!(i in slots)) slots[i] = { current: initial }; return slots[i]; },
        useEffect(effect) { const i = cursor++; if (!(i in slots)) { slots[i] = true; effects.push(effect); } },
        useCallback: (fn) => fn,
      };
      if (name === "../auth.js") return { useAuth: () => ({ isRoot }) };
      if (name === "../api.js") return { api: async (path, init) => {
        calls.push({ path, init });
        if (init) return mutation(path, init);
        if (options.loadError) throw options.loadError;
        return { entries: currentEntries };
      } };
      if (name === "../components/Feedback.js") return { toastOk() {} };
      if (name === "../components/StatusChip.js") return { Chip: "Chip", Notice: "Notice" };
      throw new Error(`Unexpected import ${name}`);
    },
  });
  const render = () => { cursor = 0; const tree = output.ModelDisplayPage(); effects.splice(0).forEach((effect) => effect()); return tree; };
  const settle = async () => { await new Promise(setImmediate); return render(); };
  render(); await settle();
  return { render, settle, calls, setMutation(fn) { mutation = fn; }, setEntries(value) { currentEntries = value; } };
}
function nodes(tree, type) {
  if (!tree || typeof tree !== "object") return [];
  if (Array.isArray(tree)) return tree.flatMap((item) => nodes(item, type));
  return [...(tree.type === type ? [tree] : []), ...nodes(tree.props?.children, type)];
}
function text(tree) {
  if (tree === null || tree === undefined) return "";
  if (Array.isArray(tree)) return tree.map(text).join("");
  return typeof tree === "object" ? text(tree.props?.children) : String(tree);
}
function edit(ui, index, value) { nodes(ui.render(), "input")[index].props.onChange({ target: { value } }); }
function save(ui, index) { return nodes(ui.render(), "button").filter((node) => text(node).includes("保存名称"))[index].props.onClick(); }

test("root loads one entry per logical model with an admin/user preview", async () => {
  const ui = await mount();
  assert.equal(ui.calls.length, 1);
  assert.equal(ui.calls[0].path, "/model-display");
  assert.equal(nodes(ui.render(), "form").length, 2);
  assert.match(text(ui.render()), /admin \/ user 预览/);
  assert.match(text(ui.render()), /1K · 2K · 4K/);
  const preview = nodes(ui.render(), "section").find((node) => node.props["aria-label"] === "admin / user 预览");
  assert.match(text(preview), /门道绘图/);
  assert.doesNotMatch(text(preview), /private-|media-group/);
});
test("non-root mounting never requests management data", async () => {
  const ui = await mount(false);
  assert.equal(ui.calls.length, 0);
  assert.equal(nodes(ui.render(), "input").length, 0);
  assert.match(text(ui.render()), /仅限 root/);
});
test("save trims a single name with its version and accepts only the authority response", async () => {
  const ui = await mount();
  edit(ui, 1, "  新绘图名称  ");
  await save(ui, 1); await ui.settle();
  assert.deepEqual(JSON.parse(ui.calls[1].init.body), { id: entries[1].id, displayName: "新绘图名称", expectedVersion: "catalog-v1" });
  assert.equal(ui.calls[1].init.method, "PUT");
  assert.equal(nodes(ui.render(), "input")[1].props.value, "新绘图名称");
  edit(ui, 1, "二次改名"); await save(ui, 1); await ui.settle();
  assert.equal(JSON.parse(ui.calls[2].init.body).expectedVersion, "saved-v2");
});
test("CAS conflict retains the draft; explicit reload updates its version before retry", async () => {
  const ui = await mount();
  edit(ui, 1, "保留草稿");
  ui.setMutation(async () => { throw Object.assign(new Error("conflict"), { code: 409 }); });
  await save(ui, 1); await ui.settle();
  assert.equal(nodes(ui.render(), "input")[1].props.value, "保留草稿");
  assert.match(text(ui.render()), /刷新目录.*草稿/);
  ui.setEntries(entries.map((entry) => ({ ...entry, expectedVersion: "catalog-v3" })));
  nodes(ui.render(), "button").find((node) => text(node) === "刷新目录").props.onClick();
  await ui.settle();
  assert.equal(nodes(ui.render(), "input")[1].props.value, "保留草稿");
  ui.setMutation(async (_path, init) => ({ entry: { ...entries[1], ...JSON.parse(init.body), expectedVersion: "v4" } }));
  await save(ui, 1); await ui.settle();
  assert.equal(JSON.parse(ui.calls.at(-1).init.body).expectedVersion, "catalog-v3");
});
test("consecutive media saves share the new catalog version and busy Enter cannot submit twice", async () => {
  const ui = await mount();
  const second = { ...entries[1], id: "media-group-two", displayName: "门道视频", kind: "video" };
  ui.setEntries([...entries, second]);
  nodes(ui.render(), "button").find((node) => text(node) === "刷新目录").props.onClick();
  await ui.settle();
  edit(ui, 1, "绘图新名称");
  edit(ui, 2, "视频新名称");
  let resolveSave;
  ui.setMutation(async () => new Promise((resolve) => { resolveSave = resolve; }));
  save(ui, 1);
  const callsBefore = ui.calls.length;
  nodes(ui.render(), "form")[2].props.onSubmit({ preventDefault() {} });
  assert.equal(ui.calls.length, callsBefore);
  resolveSave({ entry: { ...entries[1], displayName: "绘图新名称", expectedVersion: "catalog-v2" } });
  await ui.settle();
  ui.setMutation(async (_path, init) => ({ entry: { ...second, ...JSON.parse(init.body), expectedVersion: "catalog-v3" } }));
  await save(ui, 2); await ui.settle();
  assert.equal(JSON.parse(ui.calls.at(-1).init.body).expectedVersion, "catalog-v2");
  assert.equal(nodes(ui.render(), "input")[2].props.value, "视频新名称");
});
test("blank and oversized names do not mutate; loading errors have a retry", async () => {
  const ui = await mount();
  for (const value of [" ", "x".repeat(121)]) {
    edit(ui, 0, value); await save(ui, 0); await ui.settle();
  }
  assert.equal(ui.calls.length, 1);
  const failed = await mount(true, { loadError: new Error("目录暂不可用") });
  assert.match(text(failed.render()), /目录暂不可用/);
  assert.ok(nodes(failed.render(), "button").some((node) => text(node) === "刷新目录"));
});
