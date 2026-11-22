import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";
import ts from "typescript";

const source = await readFile(new URL("../src/theme.ts", import.meta.url), "utf8");
const bootstrap = await readFile(new URL("../public/theme.js", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText;

function environment({ dark = false, saved = null, blocked = false } = {}) {
  const values = new Map(saved === null ? [] : [["console-theme", saved]]);
  const mediaListeners = new Set(), storageListeners = new Set();
  const storage = {
    getItem(key) { if (blocked) throw new Error("Storage denied"); return values.get(key) ?? null; },
    setItem(key, value) { if (blocked) throw new Error("Storage denied"); values.set(key, value); },
  };
  const media = {
    matches: dark,
    addEventListener(type, fn) { assert.equal(type, "change"); mediaListeners.add(fn); },
    removeEventListener(type, fn) { assert.equal(type, "change"); mediaListeners.delete(fn); },
  };
  const document = { documentElement: { dataset: {}, style: {} } };
  const window = {
    get localStorage() { if (blocked) throw new Error("Storage denied"); return storage; },
    matchMedia(query) { assert.equal(query, "(prefers-color-scheme: dark)"); return media; },
    addEventListener(type, fn) { assert.equal(type, "storage"); storageListeners.add(fn); },
    removeEventListener(type, fn) { assert.equal(type, "storage"); storageListeners.delete(fn); },
  };
  let notifications = 0;
  const exports = {};
  const context = { exports, document, window, localStorage: storage, require(name) {
    assert.equal(name, "react");
    return { useSyncExternalStore(subscribe, getSnapshot) {
      subscribe(() => notifications++);
      return getSnapshot();
    } };
  } };
  runInNewContext(compiled, context);
  return {
    exports, context, values, storage, document, mediaListeners, storageListeners,
    notifications: () => notifications,
    systemChange(next) { media.matches = next; mediaListeners.forEach((fn) => fn()); },
    storageChange(value, key = "console-theme", area = storage) {
      storageListeners.forEach((fn) => fn({ key, newValue: value, storageArea: area }));
    },
  };
}

function assertTheme(env, preference, resolved) {
  assert.equal(env.exports.getThemeSnapshot().preference, preference);
  assert.equal(env.exports.getThemeSnapshot().resolved, resolved);
  assert.equal(env.document.documentElement.dataset.theme, resolved);
  assert.equal(env.document.documentElement.style.colorScheme, resolved);
}

test("pre-paint script and runtime agree for saved, system, invalid and blocked storage", () => {
  for (const dark of [false, true]) {
    for (const saved of [null, "system", "light", "dark", "invalid"]) {
      for (const blocked of [false, true]) {
        const env = environment({ dark, saved, blocked });
        const preference = !blocked && ["light", "dark"].includes(saved) ? saved : "system";
        const resolved = preference === "system" ? (dark ? "dark" : "light") : preference;
        runInNewContext(bootstrap, env.context);
        assert.equal(env.document.documentElement.dataset.theme, resolved);
        assert.equal(env.document.documentElement.style.colorScheme, resolved);
        const stop = env.exports.initializeTheme();
        assertTheme(env, preference, resolved);
        stop();
      }
    }
  }
});

test("system changes update the live page and subscribers without a reload", () => {
  const env = environment();
  env.exports.initializeTheme();
  env.exports.useTheme();
  env.systemChange(true);
  assertTheme(env, "system", "dark");
  assert.equal(env.notifications(), 1);
  env.systemChange(true);
  assert.equal(env.notifications(), 1);
  env.systemChange(false);
  assertTheme(env, "system", "light");
  assert.equal(env.notifications(), 2);
});

test("manual choice persists and overrides system until follow-system is restored", () => {
  const env = environment();
  let stop = env.exports.initializeTheme();
  env.exports.setThemePreference("dark");
  assert.equal(env.values.get("console-theme"), "dark");
  env.systemChange(true);
  env.systemChange(false);
  assertTheme(env, "dark", "dark");
  stop();
  stop = env.exports.initializeTheme();
  assertTheme(env, "dark", "dark");
  env.exports.setThemePreference("light");
  env.systemChange(true);
  assertTheme(env, "light", "light");
  env.exports.setThemePreference("system");
  assertTheme(env, "system", "dark");
  assert.equal(env.values.get("console-theme"), "system");
  env.systemChange(false);
  assertTheme(env, "system", "light");
  stop();
});

test("other tabs synchronize theme, invalid values and clear; unrelated storage is ignored", () => {
  const env = environment({ dark: true });
  env.exports.initializeTheme();
  env.storageChange("light");
  assertTheme(env, "light", "light");
  env.storageChange("dark", "unrelated");
  env.storageChange("dark", "console-theme", {});
  assertTheme(env, "light", "light");
  env.storageChange("dark");
  assertTheme(env, "dark", "dark");
  env.storageChange("invalid");
  assertTheme(env, "system", "dark");
  env.storageChange("light");
  env.storageChange(null, null);
  assertTheme(env, "system", "dark");
});

test("storage denial still allows manual choice and system switching", () => {
  const env = environment({ blocked: true, dark: true });
  env.exports.initializeTheme();
  env.exports.setThemePreference("light");
  assertTheme(env, "light", "light");
  env.exports.setThemePreference("system");
  env.systemChange(false);
  assertTheme(env, "system", "light");
});

test("cleanup removes both browser listeners", () => {
  const env = environment();
  const stop = env.exports.initializeTheme();
  assert.equal(env.mediaListeners.size, 1);
  assert.equal(env.storageListeners.size, 1);
  stop();
  assert.equal(env.mediaListeners.size, 0);
  assert.equal(env.storageListeners.size, 0);
});

const css = await readFile(new URL("../src/styles.css", import.meta.url), "utf8");
function tokens(selector) {
  const block = css.slice(css.indexOf(selector)).split("}")[0];
  return Object.fromEntries([...block.matchAll(/(--[\w-]+):\s*(#[\da-f]+);/g)].map((match) => [match[1], match[2]]));
}
function luminance(hex) {
  if (hex.length === 4) hex = "#" + hex.slice(1).split("").map((char) => char + char).join("");
  const channels = hex.slice(1).match(/../g).map((pair) => parseInt(pair, 16) / 255)
    .map((value) => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4);
  return channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722;
}
function contrast(first, second) {
  const [high, low] = [luminance(first), luminance(second)].sort((a, b) => b - a);
  return (high + 0.05) / (low + 0.05);
}
test("both palettes retain accessible body, status, action and warning text", () => {
  const light = tokens(":root {");
  for (const [theme, palette] of [["light", light], ["dark", { ...light, ...tokens(':root[data-theme="dark"] {') }]]) {
    for (const fg of ["--ink", "--ink-soft", "--amber-text", "--signal", "--ok-text", "--info-text"]) {
      for (const bg of ["--surface", "--surface-2", "--control", "--surface-hover", "--row-hover", ...(theme === "dark" ? ["--panel"] : [])]) {
        const ratio = contrast(palette[fg], palette[bg]);
        assert.ok(ratio >= 4.5, `${theme} ${fg} on ${bg}: ${ratio.toFixed(2)}`);
      }
    }
    for (const [fg, bg] of [["--ink", "--notice"], ["--amber-text", "--notice"], ["--signal", "--notice-stop"], ["--ink-on-accent", "--amber"], ["--ink-on-signal", "--signal-fill"]]) {
      assert.ok(contrast(palette[fg], palette[bg]) >= 4.5, `${theme} ${fg} on ${bg}`);
    }
  }
});
