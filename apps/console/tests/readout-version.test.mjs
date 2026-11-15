import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import test from "node:test";
import { runInNewContext } from "node:vm";
import { renderToStaticMarkup } from "react-dom/server";
import ts from "typescript";
import * as time from "../src/time.ts";

const require = createRequire(import.meta.url);
const source = await readFile(new URL("../src/components/ReadoutRail.tsx", import.meta.url), "utf8");
async function renderRail(canManagePlatform, canManageTeam, failUsage = false) {
  const slots = [], effects = [], calls = [];
  let cursor = 0;
  const hooks = {
    useState(initial) {
      const index = cursor++;
      if (!(index in slots)) slots[index] = initial;
      return [slots[index], (value) => { slots[index] = value; }];
    },
    useEffect(effect) {
      const index = cursor++;
      if (!(index in slots)) { slots[index] = true; effects.push(effect); }
    },
    useCallback: (fn) => fn,
  };
  const imports = {
    react: hooks,
    "../auth.js": { useAuth: () => ({
      canManagePlatform, canManageTeam, teamId: "team", teams: [],
      team: { name: "测试团队", quotaCredits: "100", usedCredits: "25" },
      user: { email: "test@example.test" }, refreshSession: async () => {},
    }) },
    "../api.js": { api: async (path) => {
      calls.push(path);
      // 即使响应意外带标题，非平台视图也不能渲染到正文或悬浮提示。
      if (path === "/version") return { tag: "v0.102.5", subject: "修复控制台版本显示" };
      if (path.startsWith("/devices")) return { devices: [] };
      if (path.startsWith("/usage")) {
        if (failUsage) throw new Error("暂时不可用");
        return { aggregates: [] };
      }
      throw new Error(`Unexpected API ${path}`);
    } },
    "../time.js": time,
    "./Gauge.js": { useCountUp: String },
    "./Icon.js": { Glyph: () => null },
    "./Select.js": { Select: () => null },
    "@shucode-platform/contracts/credits": { formatCreditsDisplay: String },
  };
  const exports = {};
  runInNewContext(ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
  }).outputText, {
    exports,
    window: { setInterval: () => 1, clearInterval: () => {} },
    require: (name) => imports[name] ?? require(name),
  });
  exports.ReadoutRail();
  effects.splice(0).forEach((effect) => effect());
  await new Promise(setImmediate);
  cursor = 0;
  return { html: renderToStaticMarkup(exports.ReadoutRail()), calls };
}

test("root sees the software tag and update title without querying config releases", async () => {
  const { html, calls } = await renderRail(true, true);
  assert.match(html, /v0\.102\.5/);
  assert.match(html, /class="note"[^>]*>修复控制台版本显示/);
  assert.ok(!calls.includes("/releases"));
});

test("members and team admins see the tag, retain balance and never render update titles", async () => {
  for (const manageTeam of [false, true]) {
    const { html } = await renderRail(false, manageTeam);
    assert.match(html, /v0\.102\.5/);
    assert.match(html, /团队余额/);
    assert.match(html, />75</);
    assert.doesNotMatch(html, /修复控制台版本显示|class="note"/);
  }
});

test("usage errors do not replace the software version with a config revision", async () => {
  const { html } = await renderRail(true, true, true);
  assert.match(html, /读数加载失败/);
  assert.match(html, /v0\.102\.5/);
  assert.doesNotMatch(html, />R\d+</);
});
