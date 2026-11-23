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

function environment({ dark = false, saved = null, blocked = false, now = "2026-11-23T01:00:00.000Z" } = {}) {
  const values = new Map(saved === null ? [] : [["console-theme", saved]]);
  const mediaListeners = new Set(), visibilityListeners = new Set();
  const windowListeners = new Map(["storage", "focus", "pageshow"].map((type) => [type, new Set()]));
  const storageListeners = windowListeners.get("storage");
  const timers = new Map();
  let currentTime = Date.parse(now), timerId = 0;
  class ClockDate extends Date {
    constructor(...args) { super(...(args.length ? args : [currentTime])); }
    static now() { return currentTime; }
  }
  const storage = {
    getItem(key) { if (blocked) throw new Error("Storage denied"); return values.get(key) ?? null; },
    setItem(key, value) { if (blocked) throw new Error("Storage denied"); values.set(key, value); },
  };
  const media = {
    matches: dark,
    addEventListener(type, fn) { assert.equal(type, "change"); mediaListeners.add(fn); },
    removeEventListener(type, fn) { assert.equal(type, "change"); mediaListeners.delete(fn); },
  };
  const document = {
    documentElement: { dataset: {}, style: {} }, hidden: false,
    addEventListener(type, fn) { assert.equal(type, "visibilitychange"); visibilityListeners.add(fn); },
    removeEventListener(type, fn) { assert.equal(type, "visibilitychange"); visibilityListeners.delete(fn); },
  };
  const window = {
    get localStorage() { if (blocked) throw new Error("Storage denied"); return storage; },
    matchMedia(query) { assert.equal(query, "(prefers-color-scheme: dark)"); return media; },
    addEventListener(type, fn) { assert.ok(windowListeners.has(type)); windowListeners.get(type).add(fn); },
    removeEventListener(type, fn) { assert.ok(windowListeners.has(type)); windowListeners.get(type).delete(fn); },
    setTimeout(fn, delay) { const id = ++timerId; timers.set(id, { fn, at: currentTime + delay }); return id; },
    clearTimeout(id) { timers.delete(id); },
  };
  let notifications = 0;
  const exports = {};
  const context = { exports, document, window, Date: ClockDate, localStorage: storage, require(name) {
    assert.equal(name, "react");
    return { useSyncExternalStore(subscribe, getSnapshot) {
      subscribe(() => notifications++);
      return getSnapshot();
    } };
  } };
  runInNewContext(compiled, context);
  return {
    exports, context, values, storage, document, mediaListeners, storageListeners, visibilityListeners, windowListeners, timers,
    notifications: () => notifications,
    setTime(value) { currentTime = Date.parse(value); },
    advance(milliseconds) {
      const end = currentTime + milliseconds;
      while (true) {
        const next = [...timers].filter(([, timer]) => timer.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
        if (!next) break;
        const [id, timer] = next;
        currentTime = Math.max(currentTime, timer.at);
        timers.delete(id);
        timer.fn();
      }
      currentTime = end;
    },
    resume(type = "focus") { windowListeners.get(type).forEach((fn) => fn()); },
    visibilityChange(hidden) { document.hidden = hidden; visibilityListeners.forEach((fn) => fn()); },
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

test("pre-paint and runtime agree at Shanghai dawn and dusk for all preferences and blocked storage", () => {
  for (const [now, automatic] of [
    ["2026-11-22T21:59:59.999Z", "dark"],
    ["2026-11-22T22:00:00.000Z", "light"],
    ["2026-11-23T09:59:59.999Z", "light"],
    ["2026-11-23T10:00:00.000Z", "dark"],
  ]) {
    for (const dark of [false, true]) {
      for (const saved of [null, "auto", "system", "light", "dark", "invalid"]) {
        for (const blocked of [false, true]) {
          const env = environment({ dark, saved, blocked, now });
          const preference = !blocked && ["auto", "light", "dark", "system"].includes(saved) ? saved : "auto";
          const resolved = preference === "auto" ? automatic : preference === "system" ? (dark ? "dark" : "light") : preference;
          runInNewContext(bootstrap, env.context);
          assert.equal(env.document.documentElement.dataset.theme, resolved);
          assert.equal(env.document.documentElement.style.colorScheme, resolved);
          const stop = env.exports.initializeTheme();
          assertTheme(env, preference, resolved);
          stop();
        }
      }
    }
  }
});

test("automatic mode crosses both Shanghai boundaries while an open page stays mounted", () => {
  for (const [now, before, after] of [
    ["2026-11-22T21:59:59.900Z", "dark", "light"],
    ["2026-11-23T09:59:59.900Z", "light", "dark"],
  ]) {
    const env = environment({ now, dark: true });
    env.exports.initializeTheme();
    env.exports.useTheme();
    assertTheme(env, "auto", before);
    env.advance(99);
    assertTheme(env, "auto", before);
    env.advance(1);
    assertTheme(env, "auto", after);
    assert.equal(env.notifications(), 1);
    env.advance(120_000);
    assert.equal(env.notifications(), 1, "unchanged minutes must not rerender subscribers");
    assert.equal(env.timers.size, 1);
  }
});

test("automatic mode follows Shanghai time independently of the operating system palette", () => {
  const env = environment({ dark: true });
  env.exports.initializeTheme();
  assertTheme(env, "auto", "light");
  env.systemChange(false);
  env.systemChange(true);
  assertTheme(env, "auto", "light");
});

test("visibility, focus and bfcache restoration recalibrate after clock jumps or sleep", () => {
  const env = environment({ now: "2026-11-22T21:00:00.000Z" });
  env.exports.initializeTheme();
  assertTheme(env, "auto", "dark");
  env.visibilityChange(true);
  env.setTime("2026-11-23T01:00:00.000Z");
  env.visibilityChange(true);
  assertTheme(env, "auto", "dark");
  env.visibilityChange(false);
  assertTheme(env, "auto", "light");
  env.setTime("2026-11-23T11:00:00.000Z");
  env.resume("pageshow");
  assertTheme(env, "auto", "dark");
  env.setTime("2026-11-23T23:00:00.000Z");
  env.resume();
  assertTheme(env, "auto", "light");
  assert.equal(env.timers.size, 1);
});

test("system changes update the live page and subscribers without a reload", () => {
  const env = environment({ saved: "system" });
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

test("manual choice persists and overrides clock and system until automatic or system is restored", () => {
  const env = environment();
  let stop = env.exports.initializeTheme();
  env.exports.setThemePreference("dark");
  assert.equal(env.values.get("console-theme"), "dark");
  env.systemChange(true);
  env.systemChange(false);
  env.setTime("2026-11-23T02:00:00.000Z");
  env.advance(60_000);
  assertTheme(env, "dark", "dark");
  assert.equal(env.timers.size, 0);
  stop();
  stop = env.exports.initializeTheme();
  assertTheme(env, "dark", "dark");
  env.exports.setThemePreference("light");
  env.systemChange(true);
  env.setTime("2026-11-23T10:00:00.000Z");
  env.resume();
  assertTheme(env, "light", "light");
  env.exports.setThemePreference("system");
  assertTheme(env, "system", "dark");
  assert.equal(env.values.get("console-theme"), "system");
  env.systemChange(false);
  assertTheme(env, "system", "light");
  env.exports.setThemePreference("auto");
  assertTheme(env, "auto", "dark");
  assert.equal(env.values.get("console-theme"), "auto");
  assert.equal(env.timers.size, 1);
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
  assertTheme(env, "auto", "light");
  env.storageChange("light");
  env.storageChange(null, null);
  assertTheme(env, "auto", "light");
});

test("storage denial still allows automatic, manual and system switching", () => {
  const env = environment({ blocked: true, dark: true });
  env.exports.initializeTheme();
  assertTheme(env, "auto", "light");
  env.exports.setThemePreference("light");
  assertTheme(env, "light", "light");
  env.exports.setThemePreference("system");
  env.systemChange(false);
  assertTheme(env, "system", "light");
  env.setTime("2026-11-23T11:00:00.000Z");
  env.exports.setThemePreference("auto");
  assertTheme(env, "auto", "dark");
});

test("reinitializing replaces listeners and leaves one automatic timer", () => {
  const env = environment();
  const first = env.exports.initializeTheme();
  const second = env.exports.initializeTheme();
  first();
  assert.equal(env.timers.size, 1);
  assert.equal(env.mediaListeners.size, 1);
  assert.equal(env.storageListeners.size, 1);
  assert.equal(env.visibilityListeners.size, 1);
  for (const listeners of env.windowListeners.values()) assert.equal(listeners.size, 1);
  second();
  assert.equal(env.timers.size, 0);
});

test("cleanup removes browser listeners and the automatic timer", () => {
  const env = environment();
  const stop = env.exports.initializeTheme();
  assert.equal(env.mediaListeners.size, 1);
  assert.equal(env.storageListeners.size, 1);
  assert.equal(env.visibilityListeners.size, 1);
  assert.equal(env.timers.size, 1);
  stop();
  assert.equal(env.mediaListeners.size, 0);
  assert.equal(env.storageListeners.size, 0);
  assert.equal(env.visibilityListeners.size, 0);
  assert.equal(env.timers.size, 0);
  for (const listeners of env.windowListeners.values()) assert.equal(listeners.size, 0);
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
  const dark = tokens(":root {");
  for (const [theme, palette] of [["light", { ...dark, ...tokens(':root[data-theme="light"] {') }], ["dark", dark]]) {
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
