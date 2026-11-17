import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";
import ts from "typescript";

const compiled = ts.transpileModule(await readFile(new URL("../src/pages/MediaFailures.tsx", import.meta.url), "utf8"), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 },
}).outputText;
const row = (id, available = true) => ({ id, teamId: "fixture-team", kind: "video", product: "测试制作", status: "failed", error: "provider_failed", updatedAt: "2026-11-17T01:00:00Z", chargedMicro: 0, diagnosticsAvailable: available, shots: [{ attempt: 1, error: "provider_failed", providerError: { code: "RateLimitExceeded", category: "rate_limit" } }] });
async function mount(options = {}) {
  const slots = [], effects = [], calls = [];
  let cursor = 0;
  const output = {}, jsx = (type, props) => ({ type, props });
  runInNewContext(compiled, { exports: output, URLSearchParams, Error, Math, Set,
    require(name) {
      if (name === "react/jsx-runtime") return { jsx, jsxs: jsx };
      if (name === "react") return {
        useState(initial) { const i = cursor++; if (!(i in slots)) slots[i] = initial; return [slots[i], (v) => { slots[i] = typeof v === "function" ? v(slots[i]) : v; }]; },
        useRef(initial) { const i = cursor++; if (!(i in slots)) slots[i] = { current: initial }; return slots[i]; },
        useCallback: (fn) => fn,
        useEffect(effect, deps) { const i = cursor++; const prev = slots[i]; if (!prev || deps.some((v, n) => v !== prev[n])) { slots[i] = deps; effects.push(effect); } },
      };
      if (name === "../api.js") return { api: async (path) => { calls.push(path); if (path.includes("/fixture-team/")) return options.detail ? options.detail(path) : { project: row("old"), diagnostics: [] }; return { records: [row("old", false), row("new")], total: 2 }; } };
      if (name === "../components/StatusChip.js") return { Chip: "Chip", Notice: "Notice" };
      if (name === "../components/Select.js") return { Select: "Select" };
      if (name === "../time.js") return { formatShanghaiTime: (v) => v };
      if (name === "lucide-react") return new Proxy({}, { get: () => () => null });
      if (name === "../components/Icon.js") return { Glyph: () => null };
      throw new Error(`Unexpected import ${name}`);
    },
  });
  const render = () => { cursor = 0; const tree = output.MediaFailures({ hours: "24" }); effects.splice(0).forEach((effect) => effect()); return tree; };
  const settle = async () => { await new Promise(setImmediate); return render(); };
  render(); await settle();
  return { render, settle, calls };
}
function nodes(tree, type) { if (!tree || typeof tree !== "object") return []; if (Array.isArray(tree)) return tree.flatMap((item) => nodes(item, type)); return [...(tree.type === type ? [tree] : []), ...nodes(tree.props?.children, type)]; }
function text(tree) { if (tree === null || tree === undefined) return ""; if (Array.isArray(tree)) return tree.map(text).join(""); return typeof tree === "object" ? text(tree.props?.children) : String(tree); }
const buttons = (ui, label) => nodes(ui.render(), "button").filter((node) => text(node) === label);

test("lists media failures and opens explicit missing historical diagnostics", async () => {
  const ui = await mount();
  assert.match(ui.calls[0], /hours=24/);
  assert.match(text(ui.render()), /请求限流/);
  assert.match(text(ui.render()), /历史未采集/);
  assert.match(text(ui.render()), /请求限流/);
  await buttons(ui, "查看详情")[0].props.onClick(); await ui.settle();
  assert.match(text(ui.render()), /旧制作单的供应商原文未保存/);
  assert.match(text(ui.render()), /RateLimitExceeded/);
  assert.equal(ui.calls.some((path) => /generate|retry|reconcile/.test(path)), false);
});
test("filters use one list path and reset pagination", async () => {
  const ui = await mount();
  nodes(ui.render(), "Select")[0].props.onChange("image"); await ui.settle(); await ui.settle();
  assert.match(ui.calls.at(-1), /kind=image/);
  nodes(ui.render(), "input")[0].props.onChange({ target: { value: " task-fixture " } });
  nodes(ui.render(), "form")[0].props.onSubmit({ preventDefault() {} }); await ui.settle(); await ui.settle();
  assert.match(ui.calls.at(-1), /q=task-fixture/);
  assert.match(ui.calls.at(-1), /offset=0/);
});
test("late diagnostic response cannot overwrite the selected project or reopen a closed detail", async () => {
  const pending = [];
  const ui = await mount({ detail: (path) => new Promise((resolve) => pending.push({ path, resolve })) });
  buttons(ui, "查看详情")[0].props.onClick(); buttons(ui, "查看详情")[1].props.onClick();
  pending[1].resolve({ project: row("new"), diagnostics: [{ at: "2026-11-17T01:00:00Z", shot: 0, attempt: 2, stage: "video_query", message: "new diagnostic", requestId: "request-fixture" }] }); await ui.settle();
  pending[0].resolve({ project: row("old"), diagnostics: [] }); await ui.settle();
  assert.match(text(ui.render()), /new diagnostic/);
  assert.match(text(ui.render()), /request-fixture/);
  assert.doesNotMatch(text(ui.render()), /旧制作单的供应商原文未保存/);
  buttons(ui, "查看详情")[0].props.onClick(); buttons(ui, "关闭详情")[0].props.onClick();
  pending[2].resolve({ project: row("old"), diagnostics: [] }); await ui.settle();
  assert.equal(buttons(ui, "关闭详情").length, 0);
});
