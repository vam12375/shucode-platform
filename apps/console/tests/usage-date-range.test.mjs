import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";
import ts from "typescript";
import * as ranges from "../src/usage-range.ts";

const source = ts.transpileModule(await readFile(new URL("../src/components/UsageDateRange.tsx", import.meta.url), "utf8"), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 },
}).outputText;
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
function mount(width = 1440) {
  const slots = [], effects = [], calls = [], listeners = new Map();
  let cursor = 0, focused = 0;
  const target = {
    contains: (node) => node?.inside === true,
    focus: () => { focused += 1; },
    getBoundingClientRect: () => ({ top: 100, bottom: 136, right: width - 16 }),
  };
  const events = {
    addEventListener(type, listener) { if (!listeners.has(type)) listeners.set(type, new Set()); listeners.get(type).add(listener); },
    removeEventListener(type, listener) { listeners.get(type)?.delete(listener); },
  };
  const output = {}, jsx = (type, props) => ({ type, props });
  runInNewContext(source, {
    exports: output, Error,
    document: events,
    window: { ...events, innerWidth: width, innerHeight: 844 },
    require(name) {
      if (name === "react/jsx-runtime") return { jsx, jsxs: jsx };
      if (name === "../usage-range.js") return ranges;
      if (name === "react") return {
        useState(initial) { const i = cursor++; if (!(i in slots)) slots[i] = initial; return [slots[i], (value) => { slots[i] = typeof value === "function" ? value(slots[i]) : value; }]; },
        useRef(initial) { const i = cursor++; if (!(i in slots)) slots[i] = { current: initial }; return slots[i]; },
        useId() { const i = cursor++; return `date-range-${i}`; },
        useEffect(effect, deps) { const i = cursor++; if (!(i in slots) || deps.some((dep, j) => dep !== slots[i].deps[j])) { slots[i]?.cleanup?.(); slots[i] = { deps }; effects.push(() => { slots[i].cleanup = effect(); }); } },
      };
      throw new Error(`Unexpected import ${name}`);
    },
  });
  const render = () => {
    cursor = 0;
    const tree = output.UsageDateRange({ preset: "today", from: "2026-10-26", to: "2026-10-26", label: "今天", onApply: (...args) => calls.push(args) });
    for (const node of nodes(tree, (node) => node.props?.ref)) node.props.ref.current = target;
    effects.splice(0).forEach((effect) => effect());
    return tree;
  };
  const button = (label) => nodes(render(), (node) => node.type === "button" && (node.props["aria-label"] === label || text(node) === label))[0];
  render();
  return {
    render, calls, button,
    open() { button("趋势时间范围").props.onClick(); render(); },
    date(label, value) { nodes(render(), (node) => node.props?.["aria-label"] === label)[0].props.onChange({ target: { value } }); render(); },
    dispatch(type, event = {}) { for (const listener of listeners.get(type) ?? []) listener({ target: {}, preventDefault() {}, ...event }); render(); },
    focused: () => focused,
  };
}

test("date range presets remain drafts until apply and include rolling 24 hours", () => {
  const ui = mount(); ui.open();
  ui.button("近 24 小时").props.onClick();
  assert.equal(ui.calls.length, 0);
  ui.button("应用").props.onClick();
  assert.equal(ui.calls[0][0], "last24h");
  assert.equal(ui.button("趋势时间范围").props["aria-expanded"], false);
});

test("custom single and multiple dates apply without accepting missing, inverted or invalid calendar days", () => {
  const ui = mount();
  for (const [from, to] of [["", "2026-10-26"], ["2026-10-27", "2026-10-26"], ["2026-02-31", "2026-03-01"]]) {
    ui.open(); ui.date("趋势开始日期", from); ui.date("趋势结束日期", to); ui.button("应用").props.onClick();
    assert.equal(ui.calls.length, 0);
    assert.ok(nodes(ui.render(), (node) => node.props?.role === "alert").length > 0);
    ui.button("取消").props.onClick();
  }
  for (const to of ["2026-10-25", "2026-10-26"]) {
    ui.open(); ui.date("趋势开始日期", "2026-10-25"); ui.date("趋势结束日期", to); ui.button("应用").props.onClick();
  }
  assert.deepEqual(ui.calls, [["custom", "2026-10-25", "2026-10-25"], ["custom", "2026-10-25", "2026-10-26"]]);
});

test("Escape and outside pointer close the draft without applying and Escape restores focus", () => {
  const ui = mount(); ui.open();
  const focused = ui.focused();
  ui.dispatch("keydown", { key: "Escape" });
  assert.equal(ui.button("趋势时间范围").props["aria-expanded"], false);
  assert.ok(ui.focused() > focused);
  ui.open(); ui.dispatch("pointerdown", { target: { inside: true } });
  assert.equal(ui.button("趋势时间范围").props["aria-expanded"], true);
  ui.dispatch("pointerdown");
  assert.equal(ui.button("趋势时间范围").props["aria-expanded"], false);
  assert.equal(ui.calls.length, 0);
});

test("the picker stays inside desktop and narrow viewports with scrollable bounded height", () => {
  for (const width of [1440, 390, 320]) {
    const ui = mount(width); ui.open();
    const style = nodes(ui.render(), (node) => node.props?.role === "dialog")[0].props.style;
    assert.ok(style.left >= 16 && style.left + style.width <= width - 16);
    assert.ok(style.top >= 16 && style.top + style.maxHeight <= 844 - 16);
    assert.equal(style.overflow, "auto");
  }
});
