import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { runInNewContext } from "node:vm";
import ts from "typescript";
import {
  TURNSTILE_LOGIN_ACTION,
  normalizeTurnstileSiteKey,
  turnstileRequired,
} from "../src/turnstile.ts";

test("Turnstile is disabled unless the public config exposes a site key", () => {
  assert.equal(normalizeTurnstileSiteKey(undefined), "");
  assert.equal(normalizeTurnstileSiteKey(null), "");
  assert.equal(normalizeTurnstileSiteKey(123), "");
  assert.equal(normalizeTurnstileSiteKey("  1x00000000000000000000AA  "), "1x00000000000000000000AA");
  assert.equal(turnstileRequired(""), false);
  assert.equal(turnstileRequired("   "), false);
  assert.equal(turnstileRequired("1x00000000000000000000AA"), true);
  assert.equal(TURNSTILE_LOGIN_ACTION, "login");
});

test("Turnstile follows theme, clears old tokens and ignores removed widget callbacks", async () => {
  const code = ts.transpileModule(await readFile(new URL("../src/components/Turnstile.tsx", import.meta.url), "utf8"), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  const slots = [], effects = [], pending = [], renders = [], removed = [], tokens = [];
  let cursor = 0, theme = "dark";
  const container = { isConnected: true };
  const api = {
    render(_node, options) { renders.push(options); return `widget-${renders.length}`; },
    remove(id) { removed.push(id); }, reset() {},
  };
  const output = {};
  const onToken = (value) => tokens.push(value);
  runInNewContext(code, {
    exports: output, window: { turnstile: api }, document: {},
    require(name) {
      if (name === "react/jsx-runtime") return { jsx: (type, props) => ({ type, props }), jsxs: (type, props) => ({ type, props }) };
      if (name === "react") return {
        useRef(initial) { const i = cursor++; slots[i] ??= { current: initial }; return slots[i]; },
        useState(initial) { const i = cursor++; if (!(i in slots)) slots[i] = initial; return [slots[i], (value) => { slots[i] = value; }]; },
        useEffect(fn, deps) { const i = cursor++; const prev = effects[i]; if (!prev || deps.some((dep, index) => !Object.is(dep, prev.deps[index]))) {
          pending.push(() => { prev?.cleanup?.(); effects[i] = { deps, cleanup: fn() }; });
        } },
      };
      if (name === "../turnstile.js") return { TURNSTILE_LOGIN_ACTION: "login" };
      if (name === "../theme.js") return { useTheme: () => ({ resolved: theme }) };
      throw new Error(name);
    },
  });
  const render = async () => {
    cursor = 0; output.Turnstile({ siteKey: "test", onToken }); slots[0].current = container;
    pending.splice(0).forEach((fn) => fn()); await new Promise(setImmediate);
  };
  await render();
  assert.equal(renders[0].theme, "dark");
  renders[0].callback("dark-token");
  assert.equal(tokens.at(-1), "dark-token");
  theme = "light";
  await render();
  assert.equal(renders[1].theme, "light");
  assert.deepEqual(removed, ["widget-1"]);
  assert.equal(tokens.at(-1), null);
  const before = tokens.length;
  renders[0].callback("stale-dark-token"); renders[0]["expired-callback"](); renders[0]["error-callback"]();
  assert.equal(tokens.length, before);
  renders[1].callback("light-token");
  assert.equal(tokens.at(-1), "light-token");
  effects.forEach((effect) => effect.cleanup?.());
  assert.deepEqual(removed, ["widget-1", "widget-2"]);
});

for (const failure of ["error", "uninitialized load"]) {
  test(`Turnstile retries a ${failure} script after changing theme`, async () => {
    const code = ts.transpileModule(await readFile(new URL("../src/components/Turnstile.tsx", import.meta.url), "utf8"), {
      compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 },
    }).outputText;
    const slots = [], effects = [], pending = [], scripts = [], renders = [], tokens = [];
    let cursor = 0, theme = "dark";
    const container = { isConnected: true };
    const window = {};
    const api = {
      render(_node, options) { renders.push(options); return `widget-${renders.length}`; },
      remove() {}, reset() {},
    };
    const document = {
      querySelector() { return scripts.find((script) => script.isConnected) ?? null; },
      createElement() {
        const events = new Map();
        return {
          dataset: {}, isConnected: false,
          addEventListener(type, fn, options) {
            events.set(type, [...(events.get(type) ?? []), { fn, once: options?.once }]);
          },
          removeEventListener(type, fn) {
            events.set(type, (events.get(type) ?? []).filter((entry) => entry.fn !== fn));
          },
          remove() { this.isConnected = false; },
          dispatch(type) {
            for (const entry of events.get(type) ?? []) {
              if (entry.once) this.removeEventListener(type, entry.fn);
              entry.fn();
            }
          },
        };
      },
      head: { appendChild(script) { script.isConnected = true; scripts.push(script); } },
    };
    const output = {};
    const onToken = (value) => tokens.push(value);
    runInNewContext(code, {
      exports: output, window, document,
      require(name) {
        if (name === "react/jsx-runtime") return { jsx: (type, props) => ({ type, props }), jsxs: (type, props) => ({ type, props }) };
        if (name === "react") return {
          useRef(initial) { const i = cursor++; slots[i] ??= { current: initial }; return slots[i]; },
          useState(initial) { const i = cursor++; if (!(i in slots)) slots[i] = initial; return [slots[i], (value) => { slots[i] = value; }]; },
          useEffect(fn, deps) { const i = cursor++; const prev = effects[i]; if (!prev || deps.some((dep, index) => !Object.is(dep, prev.deps[index]))) {
            pending.push(() => { prev?.cleanup?.(); effects[i] = { deps, cleanup: fn() }; });
          } },
        };
        if (name === "../turnstile.js") return { TURNSTILE_LOGIN_ACTION: "login" };
        if (name === "../theme.js") return { useTheme: () => ({ resolved: theme }) };
        throw new Error(name);
      },
    });
    const render = async () => {
      cursor = 0; output.Turnstile({ siteKey: "test", onToken }); slots[0].current = container;
      pending.splice(0).forEach((fn) => fn()); await new Promise(setImmediate);
    };
    await render();
    assert.equal(scripts.length, 1);
    scripts[0].dispatch(failure === "error" ? "error" : "load");
    await new Promise(setImmediate);
    assert.equal(slots[3], true);
    theme = "light";
    await render();
    assert.equal(scripts.length, 2, "theme switch must actually retry the failed load");
    window.turnstile = api;
    scripts[1].dispatch("load");
    await new Promise(setImmediate);
    assert.equal(slots[3], false);
    assert.equal(renders.length, 1);
    assert.equal(renders[0].theme, "light");
    renders[0].callback("fresh-token");
    assert.equal(tokens.at(-1), "fresh-token");
    effects.forEach((effect) => effect.cleanup?.());
  });
}
