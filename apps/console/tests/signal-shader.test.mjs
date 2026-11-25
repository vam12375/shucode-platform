import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";
import { SIGNAL_FRAGMENT_SHADER, SIGNAL_UNIFORMS, SIGNAL_VERTEX_SHADER } from "../src/components/signal/signalShader.ts";
import { normalizeSignalVideoUrl } from "../src/signal-video.ts";

// 控制台认证页与设备登录网页是同一 Signal 视觉（specs/device-login-pages.md）：
// 两份着色器源码必须逐字一致，任何一边改动都要同步另一边。
const authJs = await readFile(new URL("../../../server/internal/apiapp/assets/device-auth/auth.js", import.meta.url), "utf8");

function extract(name, pattern) {
  const match = authJs.match(pattern);
  assert.ok(match, `auth.js 中找不到 ${name}`);
  return runInNewContext(match[1]);
}

test("控制台 Signal 着色器与设备登录页 auth.js 逐字一致", () => {
  const vs = extract("VS", /var VS = ("[^"\n]*");/);
  const fs = extract("FS", /var FS = (\[[\s\S]*?\]\.join\("\\n"\));/);
  assert.equal(SIGNAL_VERTEX_SHADER, vs);
  assert.equal(SIGNAL_FRAGMENT_SHADER, fs);
});

test("控制台读取的 uniform 与 auth.js 一致", () => {
  const list = extract("uniforms", /\n  (\[(?:"[a-zA-Z]+",\s*)+"[a-zA-Z]+"\])\.forEach\(function \(n\) \{ U\[n\]/);
  // vm 返回的数组来自另一个 realm，展开后再比较，避免原型不同导致误判。
  assert.deepEqual([...SIGNAL_UNIFORMS], [...list]);
  for (const name of SIGNAL_UNIFORMS) assert.match(SIGNAL_FRAGMENT_SHADER, new RegExp(`\\b${name}\\b`), name);
});

test("背景视频地址只接受同源路径或 http(s) 绝对地址", () => {
  assert.equal(normalizeSignalVideoUrl("/app/device-auth/signal.mp4?v=abc"), "/app/device-auth/signal.mp4?v=abc");
  assert.equal(normalizeSignalVideoUrl(" https://media.example.com/app/device-auth/signal.mp4?v=abc "), "https://media.example.com/app/device-auth/signal.mp4?v=abc");
  for (const bad of ["", "//evil.example/x.mp4", "javascript:alert(1)", "data:video/mp4;base64,AA", "/a b.mp4", 'https://x.example/"onerror', null, 42, undefined]) {
    assert.equal(normalizeSignalVideoUrl(bad), "", String(bad));
  }
});

test("所有控制台认证入口共用 Signal 外壳，不再各自绘制背景", async () => {
  const frame = await readFile(new URL("../src/components/PolarAuth.tsx", import.meta.url), "utf8");
  assert.match(frame, /<SignalBackdrop /);
  assert.match(frame, /loadPublicConfig\(\)/);
  for (const file of ["../src/pages/Login.tsx", "../src/pages/RootSetup.tsx", "../src/pages/Invite.tsx", "../src/Entry.tsx", "../src/App.tsx"]) {
    const source = await readFile(new URL(file, import.meta.url), "utf8");
    assert.match(source, /PolarAuthFrame/, file);
  }
  const login = await readFile(new URL("../src/pages/Login.tsx", import.meta.url), "utf8");
  assert.doesNotMatch(login, /api<[^>]*>\("\/public-config"\)/, "登录页应复用 loadPublicConfig，避免重复触发匿名限速");
});
